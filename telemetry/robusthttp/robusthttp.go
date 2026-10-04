// Package robusthttp is an HTTP client for talking to other services: sane
// timeouts and connection pooling, retries with exponential backoff for
// safe requests, and an OpenTelemetry span per attempt.
//
// Only GET, HEAD and OPTIONS are retried, on network errors, 429 and 5xx
// (not 501). A server's wait (Retry-After, or atproto's RateLimit-Reset)
// within MaxDelay is honoured; a longer one is returned to the caller to
// deal with. Other 4xx aren't retried: XRPC reports errors like
// RepoNotFound as 400s, and they won't change on a second try.
package robusthttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Options tune a client. The zero value of a field means its default.
type Options struct {
	// Timeout bounds a whole request, retries and all. Default 30s.
	Timeout time.Duration
	// MaxRetries after the first attempt. Default 3; negative for none.
	MaxRetries int
	// BaseDelay and MaxDelay bound the backoff between attempts.
	// Defaults 500ms and 10s.
	BaseDelay, MaxDelay time.Duration
	// MaxIdleConnsPerHost to keep for reuse. Default 16.
	MaxIdleConnsPerHost int
	// UserAgent for requests that don't set one.
	UserAgent string
	// Logger for retries (at debug). Default slog.Default().
	Logger *slog.Logger
	// Dialer for new connections, e.g. one that refuses private addresses
	// for hosts taken from user data. Default: a plain dialer with timeouts.
	Dialer *net.Dialer
}

type Option func(*Options)

func WithTimeout(d time.Duration) Option { return func(o *Options) { o.Timeout = d } }
func WithMaxRetries(n int) Option        { return func(o *Options) { o.MaxRetries = n } }
func WithBackoff(base, max time.Duration) Option {
	return func(o *Options) { o.BaseDelay, o.MaxDelay = base, max }
}
func WithMaxIdleConnsPerHost(n int) Option { return func(o *Options) { o.MaxIdleConnsPerHost = n } }
func WithUserAgent(ua string) Option       { return func(o *Options) { o.UserAgent = ua } }
func WithLogger(l *slog.Logger) Option     { return func(o *Options) { o.Logger = l } }
func WithDialer(d *net.Dialer) Option      { return func(o *Options) { o.Dialer = d } }

func options(opts []Option) Options {
	o := Options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 3
	} else if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	if o.BaseDelay == 0 {
		o.BaseDelay = 500 * time.Millisecond
	}
	if o.MaxDelay == 0 {
		o.MaxDelay = 10 * time.Second
	}
	if o.MaxIdleConnsPerHost == 0 {
		o.MaxIdleConnsPerHost = 16
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Dialer == nil {
		o.Dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	return o
}

// NewClient returns a client with a pooled transport, retries and tracing.
func NewClient(opts ...Option) *http.Client {
	o := options(opts)
	return &http.Client{
		Timeout:   o.Timeout,
		Transport: wrap(NewTransport(opts...), o),
	}
}

// NewTransport returns a pooled transport with timeouts on each stage, and
// no retries or tracing (see Wrap).
func NewTransport(opts ...Option) *http.Transport {
	o := options(opts)
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           o.Dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   o.MaxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// Wrap adds retries and tracing to a transport, for clients whose transport
// must stay as it is (one with SSRF protections, say).
func Wrap(base http.RoundTripper, opts ...Option) http.RoundTripper {
	return wrap(base, options(opts))
}

// Instrument adds tracing alone to a transport.
func Instrument(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base, otelhttp.WithSpanNameFormatter(spanName))
}

func wrap(base http.RoundTripper, o Options) http.RoundTripper {
	// Retries outside tracing: each attempt is its own span.
	return &retrier{next: Instrument(base), o: o}
}

// "GET /xrpc/com.atproto.repo.listRecords": the path, not the host, which
// for atproto can be any of thousands of PDSes.
func spanName(_ string, r *http.Request) string {
	return r.Method + " " + r.URL.Path
}

var retries = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "robusthttp_retries_total",
	Help: "HTTP requests retried, by why.",
}, []string{"reason"})

type retrier struct {
	next http.RoundTripper
	o    Options
}

func (t *retrier) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.o.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.o.UserAgent)
	}
	if !safe(req.Method) {
		return t.next.RoundTrip(req)
	}
	ctx := req.Context()
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 && req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, errors.New("robusthttp: can't retry a request whose body can't be re-read")
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r = req.Clone(ctx)
			r.Body = body
		}
		resp, err := t.next.RoundTrip(r)
		if attempt >= t.o.MaxRetries {
			return resp, err
		}
		reason, wait, ok := t.retry(ctx, resp, err, attempt)
		if !ok {
			return resp, err
		}
		if resp != nil {
			// Drain a little so the connection can be reused.
			_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
			resp.Body.Close()
		}
		retries.WithLabelValues(reason).Inc()
		t.o.Logger.Debug("retrying request", "method", req.Method, "host", req.URL.Host, "path", req.URL.Path,
			"reason", reason, "attempt", attempt+1, "wait", wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func safe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// retry says whether (and after how long) to try again.
func (t *retrier) retry(ctx context.Context, resp *http.Response, err error, attempt int) (reason string, wait time.Duration, ok bool) {
	if ctx.Err() != nil {
		return "", 0, false
	}
	if err != nil {
		if permanent(err) {
			return "", 0, false
		}
		return "network", t.backoff(attempt), true
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return "", 0, false
	}
	reason = strconv.Itoa(resp.StatusCode)
	if after, ok := serverWait(resp.Header); ok {
		if after > t.o.MaxDelay {
			return "", 0, false // longer than we'll wait: the caller decides
		}
		return reason, after, true
	}
	return reason, t.backoff(attempt), true
}

// backoff is exponential with jitter: between half and all of
// BaseDelay·2^attempt, capped at MaxDelay.
func (t *retrier) backoff(attempt int) time.Duration {
	d := t.o.BaseDelay << attempt
	if d <= 0 || d > t.o.MaxDelay {
		d = t.o.MaxDelay
	}
	return d/2 + rand.N(d/2+1)
}

// serverWait is how long the server asked us to wait: Retry-After (seconds
// or a date), or else RateLimit-Reset, which atproto services send with a
// 429 as a Unix time (and the IETF draft as seconds).
func serverWait(h http.Header) (time.Duration, bool) {
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseInt(v, 10, 64); err == nil && s >= 0 {
			return time.Duration(s) * time.Second, true
		}
		if at, err := http.ParseTime(v); err == nil {
			return max(time.Until(at), 0), true
		}
	}
	if v := h.Get("RateLimit-Reset"); v != "" {
		if s, err := strconv.ParseInt(v, 10, 64); err == nil && s >= 0 {
			if s > 1_000_000_000 { // a Unix time, not a number of seconds
				return max(time.Until(time.Unix(s, 0)), 0), true
			}
			return time.Duration(s) * time.Second, true
		}
	}
	return 0, false
}

// permanent errors won't go away by trying again.
func permanent(err error) bool {
	var unknownCA x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var tlsRecord tls.RecordHeaderError
	var verify *tls.CertificateVerificationError
	return errors.As(err, &unknownCA) || errors.As(err, &invalid) || errors.As(err, &hostname) ||
		errors.As(err, &tlsRecord) || errors.As(err, &verify)
}

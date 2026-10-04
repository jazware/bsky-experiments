package postcard

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("postcard-renderer")

const (
	// CardWidth is the CSS pixel width of the rendered card
	CardWidth = 600
	// renderScale is the device scale factor (2 = retina-crisp PNGs)
	renderScale = 2
	// renderTimeout bounds a single render, including remote image fetches
	renderTimeout = 20 * time.Second
	// imageWaitTimeout bounds how long we wait for embedded images before
	// screenshotting anyway
	imageWaitTimeout = 8 * time.Second
)

// Renderer screenshots card HTML using a shared headless Chromium browser
type Renderer struct {
	logger      *slog.Logger
	browserCtx  context.Context
	allocCancel context.CancelFunc
	ctxCancel   context.CancelFunc
	sem         chan struct{}
}

// NewRenderer launches a headless Chromium and verifies it is usable.
// browserPath overrides the browser binary ($PATH lookup when empty).
// Returns an error if no browser is available (callers should degrade
// gracefully to static OG images).
func NewRenderer(ctx context.Context, logger *slog.Logger, concurrency int, browserPath string) (*Renderer, error) {
	if concurrency < 1 {
		concurrency = 1
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("mute-audio", true),
	)
	if browserPath != "" {
		opts = append(opts, chromedp.ExecPath(browserPath))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, ctxCancel := chromedp.NewContext(allocCtx)

	// Start the browser now so a missing Chromium fails fast at startup.
	// The browser's lifetime is tied to the context its first Run uses, so
	// this must be browserCtx itself — a timeout wrapper would kill the
	// browser when it expires. Bound startup with a timer instead.
	startErr := make(chan error, 1)
	go func() { startErr <- chromedp.Run(browserCtx) }()
	select {
	case err := <-startErr:
		if err != nil {
			ctxCancel()
			allocCancel()
			return nil, fmt.Errorf("failed to launch headless chromium: %w", err)
		}
	case <-time.After(30 * time.Second):
		ctxCancel()
		allocCancel()
		return nil, fmt.Errorf("timed out launching headless chromium")
	}

	return &Renderer{
		logger:      logger.With("component", "postcard-renderer"),
		browserCtx:  browserCtx,
		allocCancel: allocCancel,
		ctxCancel:   ctxCancel,
		sem:         make(chan struct{}, concurrency),
	}, nil
}

// Close shuts down the browser
func (r *Renderer) Close() {
	r.ctxCancel()
	r.allocCancel()
}

// RenderPNG renders card HTML in a fresh browser tab and screenshots the
// #card element, returning PNG bytes at 2x device scale.
func (r *Renderer) RenderPNG(ctx context.Context, html string) ([]byte, error) {
	ctx, span := tracer.Start(ctx, "RenderPNG")
	defer span.End()

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Tab lifetime is bound to the shared browser, not the request context,
	// so a client disconnect can't wedge the browser; the timeout bounds it.
	tabCtx, cancelTab := chromedp.NewContext(r.browserCtx)
	defer cancelTab()
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, renderTimeout)
	defer cancelTimeout()

	dataURL := "data:text/html;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(html))

	if err := chromedp.Run(tabCtx,
		chromedp.EmulateViewport(CardWidth, 400, chromedp.EmulateScale(renderScale)),
		// Transparent page background so the card's rounded corners keep
		// their alpha instead of showing white
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetDefaultBackgroundColorOverride().
				WithColor(&cdp.RGBA{R: 0, G: 0, B: 0, A: 0}).Do(ctx)
		}),
		chromedp.Navigate(dataURL),
		chromedp.WaitVisible("#card", chromedp.ByID),
	); err != nil {
		return nil, fmt.Errorf("failed to load card page: %w", err)
	}

	// Best-effort wait for remote images (avatars, post media) to finish
	// loading; screenshot anyway if some hang or fail. Requiring decoded
	// pixels (naturalWidth > 0) means transiently-failed loads keep us
	// waiting for the timeout instead of being screenshotted as broken.
	waitCtx, cancelWait := context.WithTimeout(tabCtx, imageWaitTimeout)
	err := chromedp.Run(waitCtx,
		chromedp.Poll(`Array.from(document.images).every((i) => i.complete && i.naturalWidth > 0)`, nil,
			chromedp.WithPollingInterval(100*time.Millisecond)),
	)
	cancelWait()
	if err != nil {
		r.logger.Debug("timed out waiting for card images, screenshotting anyway", "error", err)
	}

	var png []byte
	if err := chromedp.Run(tabCtx,
		chromedp.Screenshot("#card", &png, chromedp.ByID),
	); err != nil {
		return nil, fmt.Errorf("failed to screenshot card: %w", err)
	}

	return png, nil
}

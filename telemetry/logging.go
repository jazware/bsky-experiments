package telemetry

import (
	"log/slog"
	"os"

	"github.com/urfave/cli/v2"
)

var CLIFlagDebug = &cli.BoolFlag{
	Name:    "debug",
	Usage:   "Enable debug mode",
	Value:   false,
	EnvVars: []string{"DEBUG"},
}

type Logging struct {
	level      slog.Level
	logger     *slog.Logger
	withSource bool
	handler    slog.Handler
}

func StartLogger(cctx *cli.Context, opts ...LoggingOption) *slog.Logger {
	l := &Logging{
		level:      slog.LevelInfo,
		withSource: true,
	}

	if cctx.Bool("debug") {
		l.level = slog.LevelDebug
	}

	for _, opt := range opts {
		opt(l)
	}

	if l.handler == nil {
		l.handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     l.level,
			AddSource: l.withSource,
		})
	}

	l.logger = slog.New(l.handler)
	slog.SetDefault(l.logger)

	return l.logger
}

type LoggingOption func(*Logging)

// WithSource sets whether the logger should include source information.
// Defaults to true.
func WithSource(includeSource bool) LoggingOption {
	return func(l *Logging) {
		l.withSource = includeSource
	}
}

// WithHandler sets a custom slog.Handler for the logger.
func WithHandler(handler slog.Handler) LoggingOption {
	return func(l *Logging) {
		l.handler = handler
	}
}

// WithLevel sets the logging level for the logger.
// Defaults to slog.LevelInfo or slog.LevelDebug if debug is enabled.
func WithLevel(level slog.Level) LoggingOption {
	return func(l *Logging) {
		l.level = level
	}
}

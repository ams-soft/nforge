package logs

import (
	"context"
	"errors"
	"log/slog"
)

type multiHandler struct {
	handlers []slog.Handler
}

func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle delivers the record to all handlers; a failure in one (e.g. otel)
// does not prevent the others (e.g. terminal) from receiving the log.
func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return NewMultiHandler(hs...)
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithGroup(name)
	}
	return NewMultiHandler(hs...)
}

type leveledHandler struct {
	slog.Handler
	level slog.Level
}

func (l *leveledHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= l.level
}

// WithAttrs/WithGroup must re-wrap the handler, otherwise logger.With(...)
// returns the inner handler and the level filter is lost.
func (l *leveledHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &leveledHandler{Handler: l.Handler.WithAttrs(attrs), level: l.level}
}

func (l *leveledHandler) WithGroup(name string) slog.Handler {
	return &leveledHandler{Handler: l.Handler.WithGroup(name), level: l.level}
}

func SetLogLevelByEnv(env string) slog.Level {
	switch env {
	case "local":
		return slog.LevelDebug
	case "dev":
		return slog.LevelInfo
	case "stg":
		return slog.LevelWarn
	case "prd":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

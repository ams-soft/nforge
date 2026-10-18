package logs

import (
	"context"
	"log/slog"

	"github.com/ams-soft/go-ams-router/middleware"
)

type contextHandler struct {
	slog.Handler
}

func (c *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := middleware.GetRequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return c.Handler.Handle(ctx, r)
}

func (c *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: c.Handler.WithAttrs(attrs)}
}

func (c *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: c.Handler.WithGroup(name)}
}

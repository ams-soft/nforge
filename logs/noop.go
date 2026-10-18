package logs

import "log/slog"

func NewNoop() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

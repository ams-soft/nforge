package main

import "log/slog"

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

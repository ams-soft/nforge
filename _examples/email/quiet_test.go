package main

import (
	"log/slog"

	"github.com/ams-soft/nforge/app"
)

func quietOption() app.Option { return app.WithLogger(slog.New(slog.DiscardHandler)) }

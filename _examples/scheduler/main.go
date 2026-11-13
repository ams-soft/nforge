// Command scheduler shows the scheduler module in an application with no HTTP
// server, no listener and no broker: a stock module registers one Cron job
// that counts the rows of a PostgreSQL table every day at 07:00 in
// America/Sao_Paulo, using the database and the logger resolved through DI.
//
//	docker compose -f _examples/scheduler/docker-compose.yml up -d
//	go run ./_examples/scheduler -config _examples/scheduler/config.yaml
//
// To see it run without waiting for 07:00, override the expression for one run:
//
//	go run ./_examples/scheduler -config _examples/scheduler/config.yaml -cron "* * * * *"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/scheduler"

	_ "time/tzdata" // America/Sao_Paulo must resolve in minimal containers too
)

// dailyAtSeven is the example's schedule: minute 0, hour 7, every day.
const dailyAtSeven = "0 7 * * *"

func main() {
	frequency := flag.String("cron", dailyAtSeven, "five-field Cron expression of the stock job (default: every day at 07:00)")
	cfg, err := config.Load() // parses the flags, -cron included
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR] unable to load config:", err)
		os.Exit(1)
	}
	application, err := newApp(cfg, *frequency)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
	// Run starts the application, blocks until SIGINT/SIGTERM and shuts it
	// down: the scheduler stops first (running jobs are canceled and awaited),
	// then the database closes. No HTTP server keeps the process alive: Run does.
	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "[Fatal ERROR]", err)
		os.Exit(1)
	}
}

// newApp registers the modules; the tests reuse it.
func newApp(cfg *config.Document, frequency string, opts ...app.Option) (*app.Application, error) {
	mods := app.Modules(logs.Module(), database.Module(), scheduler.Module(), stockModule(frequency))
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}

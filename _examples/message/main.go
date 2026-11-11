package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/module"
)

func main() {
	mode := flag.String("mode", "consumer", "producer | consumer | reprocess")
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a, err := newApp(cfg, *mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *mode == "consumer" {
		if err = a.Run(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err = a.Start(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, ok := app.Value(a, message.Capability)
	if !ok {
		fmt.Fprintln(os.Stderr, message.ErrDisabled)
		os.Exit(1)
	}
	switch *mode {
	case "producer":
		_, err = client.Publish(context.Background(), "notifications", map[string]string{"event": "notification"})
		if err == nil {
			_, err = client.Publish(context.Background(), "payments", map[string]string{"event": "payment"}, message.WithPriority(8))
		}
		if err == nil {
			_, err = client.Publish(context.Background(), "audit", map[string]string{"event": "audit"})
		}
		if err == nil {
			_, err = client.Publish(context.Background(), "audit", map[string]string{"event": "invalid-priority"}, message.WithPriority(1))
			if errors.Is(err, message.ErrInvalidPriority) {
				fmt.Println("Quorum priority correctly rejected")
				err = nil
			}
		}
	case "reprocess":
		var result message.ReprocessResult
		result, err = client.Reprocess(context.Background(), "webhooks", 0)
		fmt.Printf("reprocessed=%d failed=%d\n", result.Reprocessed, result.Failed)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	stopErr := a.Shutdown(context.Background())
	if err == nil {
		err = stopErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newApp(cfg *config.Document, mode string) (*app.Application, error) {
	worker := module.New("message-example", module.Definition{Requires: []module.Handle{message.Capability}, Optional: []module.Handle{module.Logger}, Init: func(ctx context.Context, s *module.Scope) (module.Hooks, error) {
		if mode != "consumer" {
			return module.Hooks{}, nil
		}
		logger, ok := module.Lookup(s, module.Logger)
		if !ok {
			logger = slog.New(slog.DiscardHandler)
		}
		client := module.Require(s, message.Capability)
		if err := client.AddConsumer("notifications", func(ctx context.Context, d *message.Delivery) error {
			logger.InfoContext(ctx, "[MessageExample] notification received", "message_id", d.ID)
			return nil
		}); err != nil {
			return module.Hooks{}, err
		}
		if err := client.AddConsumer("audit", func(ctx context.Context, d *message.Delivery) error {
			logger.InfoContext(ctx, "[MessageExample] audit received", "message_id", d.ID)
			return d.Ack()
		}, message.WithManualAck()); err != nil {
			return module.Hooks{}, err
		}
		return module.Hooks{}, nil
	}})
	return app.New(cfg, app.Modules(logs.Module(), codec.Module(), message.Module(), message.Consumers(), worker))
}

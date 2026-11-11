package main

import (
	"context"
	"log/slog"

	"github.com/ams-soft/nforge/message"
)

func registerMessageConsumers(client *message.Client, logger *slog.Logger) error {
	for _, queue := range []string{"user.created", "audit"} {
		q := queue
		if err := client.AddConsumer(q, func(ctx context.Context, d *message.Delivery) error {
			logger.InfoContext(ctx, "[BasicExample] received message", "queue", q, "message_id", d.ID)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

package sse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ams-soft/nforge/message"
)

// rabbitDriver distributes through one fanout exchange. Every instance has an
// exclusive, auto-delete, non-durable queue bound to it (created and restored
// by the Messaging client, which owns the connection), receives every
// publication and delivers to the local connections of the target user only.
// Nothing is retained: an event published while an instance is disconnected or
// rebinding is lost for it, which is the best-effort contract of this module.
type rabbitDriver struct {
	client   *message.Client
	exchange string
	prefetch int
	routing  string // RoutingFanout or RoutingUser
	m        *Manager
	sub      *message.Subscription
}

func (d *rabbitDriver) start(ctx context.Context, m *Manager) error {
	d.m = m
	subscribe := d.client.SubscribeFanout
	if d.routing == RoutingUser {
		subscribe = d.client.SubscribeRouted
	}
	sub, err := subscribe(ctx, d.exchange, d.handle,
		message.WithFanoutPrefetch(d.prefetch),
		message.WithFanoutMaxBytes(int(m.cfg.Limits.MaxPayloadBytes)+4096),
		message.WithFanoutTimeout(2*time.Second))
	if err != nil {
		return fmt.Errorf("sse: subscribe to the fanout exchange: %w", err)
	}
	d.sub = sub
	return nil
}

// handle runs for each broker message. A malformed or invalid message is
// rejected without requeue (dropped once, never looped) and counted; a valid
// one is acknowledged after it entered the local dispatch path, which is the
// only guarantee: no browser acknowledgment exists.
func (d *rabbitDriver) handle(_ context.Context, body []byte) error {
	d.m.stats.received.Add(1)
	var w wire
	if err := d.m.codec.DecodeAs("json", body, &w); err != nil || w.Version != wireVersion || d.m.validateWire(w) != nil || w.ID == "" {
		d.m.stats.malformed.Add(1)
		return errors.New("sse: invalid distribution message")
	}
	d.m.deliver(w)
	return nil
}

func (d *rabbitDriver) publish(ctx context.Context, w wire) error {
	body, err := d.m.codec.EncodeAs("json", w)
	if err != nil {
		return fmt.Errorf("%w: encode", ErrInvalidEvent)
	}
	var err2 error
	if d.routing == RoutingUser {
		err2 = d.client.PublishRouted(ctx, d.exchange, routingKey(w.User), body)
	} else {
		err2 = d.client.PublishFanout(ctx, d.exchange, body)
	}
	if err := err2; err != nil {
		switch {
		case errors.Is(err, message.ErrPublishUncertain):
			return fmt.Errorf("%w: %w", ErrPublishUncertain, err)
		case errors.Is(err, message.ErrPayloadTooLarge):
			return ErrPayloadTooLarge
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err
		default:
			return fmt.Errorf("%w: %w", ErrTransportUnavailable, err)
		}
	}
	return nil
}

func (d *rabbitDriver) ready(context.Context) error {
	if d.sub == nil || !d.sub.Active() || !d.client.Status().Ready {
		return fmt.Errorf("%w: RabbitMQ distribution is not active", ErrTransportUnavailable)
	}
	return nil
}

func (d *rabbitDriver) reconnects() uint64 {
	if d.sub == nil {
		return 0
	}
	if n := d.sub.Attachments(); n > 1 {
		return n - 1
	}
	return 0
}

func (d *rabbitDriver) stop(ctx context.Context) error {
	if d.sub == nil {
		return nil
	}
	return d.sub.Close(ctx)
}

// routingKey is the binding key of a user: a fixed-size digest, so any user key
// (length, characters) fits the 255-byte limit and the key never leaks one.
func routingKey(user string) string {
	sum := sha256.Sum256([]byte(user))
	return hex.EncodeToString(sum[:])
}

// sync binds (or unbinds) the user on this instance's queue in user routing.
func (d *rabbitDriver) sync(_ context.Context, user string, present func() bool) error {
	if d.routing != RoutingUser || d.sub == nil {
		return nil
	}
	return d.sub.Sync(routingKey(user), present)
}

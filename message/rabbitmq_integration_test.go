//go:build integration

package message

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
	amqp "github.com/rabbitmq/amqp091-go"
)

func integrationClient(t *testing.T, specs ...QueueSpec) *Client {
	t.Helper()
	raw := os.Getenv("NFORGE_TEST_RABBITMQ_URL")
	if raw == "" {
		t.Skip("NFORGE_TEST_RABBITMQ_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reg, err := codec.New(codec.Config{Formats: []string{"json"}, Default: "json", MaxPayloadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Enabled: true, Driver: "rabbitmq", Namespace: fmt.Sprintf("nf_test_%d", time.Now().UnixNano()), PayloadFormat: "json", RabbitMQ: RabbitMQConfig{URL: raw}, Publisher: PublisherConfig{MaxPayloadBytes: codec.MB}}
	cfg.defaults()
	c, err := openClient(ctx, cfg, reg, nil, specs)
	if err != nil {
		t.Fatal(err)
	}
	c.started.Store(true)
	t.Cleanup(func() {
		_ = c.Shutdown(context.Background())
		co, e := amqp.Dial(raw)
		if e == nil {
			ch, e := co.Channel()
			if e == nil {
				for _, q := range specs {
					_, _ = ch.QueueDelete(q.name+"_dlq", false, false, false)
					_, _ = ch.QueueDelete(q.name+"_retry", false, false, false)
					_, _ = ch.QueueDelete(q.name, false, false, false)
				}
				_ = ch.ExchangeDelete(cfg.Namespace+".dlx", false, false)
				_ = ch.Close()
			}
			_ = co.Close()
		}
	})
	return c
}

func TestRabbitMQConfirmAndUnroutable(t *testing.T) {
	name := fmt.Sprintf("nf_publish_%d", time.Now().UnixNano())
	c := integrationClient(t, Queue(name))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.Publish(ctx, name, map[string]string{"ok": "yes"})
	if err != nil || result == nil || result.MessageID == "" {
		t.Fatalf("Publish = %#v, %v", result, err)
	}
	if _, err = c.Publish(ctx, name+"_missing", map[string]int{"x": 1}); !errors.Is(err, ErrQueueNotRegistered) {
		t.Fatalf("unknown queue = %v", err)
	}
	missing := name + "_unroutable"
	c.queues[missing] = Queue(missing)
	if _, err = c.Publish(ctx, missing, map[string]int{"x": 1}); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("mandatory publish = %v", err)
	}
}

func TestRabbitMQRetryExhaustion(t *testing.T) {
	name := fmt.Sprintf("nf_retry_%d", time.Now().UnixNano())
	c := integrationClient(t, Queue(name).DeadLetter().WithRetry(100*time.Millisecond, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c.started.Store(false)
	c.lifeCtx = ctx
	if err := c.AddConsumer(name, func(context.Context, *Delivery) error { return errors.New("expected handler failure") }); err != nil {
		t.Fatal(err)
	}
	if err := c.startConsumers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(ctx, name, []byte("retry")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		total, err := c.Total(ctx, name)
		if err == nil && total.Failed == 1 {
			_ = c.stopConsumers(context.Background())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = c.stopConsumers(context.Background())
	total, _ := c.Total(context.Background(), name)
	t.Fatalf("retry did not reach DLQ: %+v", total)
}

func TestRabbitMQConnectionRecoveryRestartsConsumer(t *testing.T) {
	name := fmt.Sprintf("nf_reconnect_%d", time.Now().UnixNano())
	c := integrationClient(t, Queue(name))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.started.Store(false)
	c.lifeCtx = ctx
	delivered := make(chan struct{}, 1)
	if err := c.AddConsumer(name, func(context.Context, *Delivery) error { delivered <- struct{}{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.startConsumers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	failures := make(chan error, 1)
	go c.supervise(ctx, func(err error) {
		select {
		case failures <- err:
		default:
		}
	}, nil)
	generation := c.generation.Load()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && c.generation.Load() == generation {
		select {
		case err := <-failures:
			t.Fatalf("supervisor reported failure: %v", err)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if c.generation.Load() == generation || !c.Status().Ready {
		t.Fatalf("connection did not recover: %+v", c.Status())
	}
	if _, err := c.Publish(ctx, name, []byte("again")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := c.stopConsumers(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRabbitMQDeadLetterAndReprocess(t *testing.T) {
	name := fmt.Sprintf("nf_dlq_%d", time.Now().UnixNano())
	c := integrationClient(t, Queue(name).DeadLetter())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Publish(ctx, name, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := c.conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	tag := name + "-test"
	deliveries, err := ch.Consume(name, tag, false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-deliveries:
		if err = d.Nack(false, false); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = ch.Cancel(tag, false)
	_ = ch.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		total, e := c.Total(ctx, name)
		if e == nil && total.Failed == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, err := c.Reprocess(ctx, name, 0)
	if err != nil || res.Reprocessed != 1 {
		t.Fatalf("Reprocess = %+v, %v", res, err)
	}
	ch, err = c.conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := ch.Get(name, true)
	if err != nil || !ok || string(got.Body) != "payload" {
		t.Fatalf("get = %v, %v, %v", got.Body, ok, err)
	}
	_ = ch.Close()
}

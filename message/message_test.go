package message

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/internal/apptest"
)

func TestQueueBuilderIsImmutable(t *testing.T) {
	base := Queue("events")
	priority := base.MaxPriority(8)
	dead := priority.DeadLetter()
	retry := dead.WithRetry(2*time.Second, 3)
	if base.prioritySet || base.deadLetter || base.retryDelay != 0 {
		t.Fatal("builder mutated the original queue")
	}
	if priority.deadLetter || priority.retryDelay != 0 {
		t.Fatal("builder mutation escaped to another value")
	}
	if err := retry.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedMessageIDIsUUIDv7(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || id[14] != '7' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("generated ID is not UUIDv7: %q", id)
	}
}

func TestDisabledModuleDoesNotValidateQueuesOrConnect(t *testing.T) {
	a, err := apptest.New(t, "message: {enabled: false, rabbitmq: {url: 'not a URL'}, queues: [{name: 'amq.bad', max_priority: 0}]}\n", codec.Module(), Module(WithQueues(Queue("amq.code-bug"))))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.Value(a, Capability); ok {
		t.Fatal("disabled message module published a client")
	}
	if err = a.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestProductionRejectsPlaintextRabbitMQ(t *testing.T) {
	base := "env: prd\nmessage: {enabled: true, rabbitmq: {url: 'amqp://user:pass@broker.example:5672/'}}\n"
	if _, err := apptest.New(t, base, codec.Module(), Module()); err == nil || !strings.Contains(err.Error(), "prd_exceptions.message_plaintext") {
		t.Fatalf("app.New error = %v", err)
	}
	a, err := apptest.New(t, base+"prd_exceptions: {message_plaintext: true}\n", codec.Module(), Module())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Warnings()) == 0 {
		t.Fatal("production exception should retain a warning")
	}
}

func TestQueueValidation(t *testing.T) {
	cases := []struct {
		name string
		q    QueueSpec
		want error
	}{
		{"classic", Queue("events"), nil},
		{"quorum", Queue("events").QuorumQueue(), nil},
		{"priority", Queue("events").MaxPriority(10), nil},
		{"priority zero", Queue("events").MaxPriority(0), ErrInvalidPriority},
		{"priority over max", Queue("events").MaxPriority(11), ErrInvalidPriority},
		{"quorum priority", Queue("events").MaxPriority(2).QuorumQueue(), ErrInvalidPriority},
		{"reserved prefix", Queue("amq.events"), ErrInvalidQueue},
		{"reserved suffix", Queue("events_dlq"), ErrInvalidQueue},
		{"retry without dlq", Queue("events").WithRetry(time.Second, 2), ErrInvalidQueue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.q.validate(); !errors.Is(err, tc.want) {
				t.Fatalf("validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConfigDefaultsAndRedaction(t *testing.T) {
	c := Config{Enabled: true, RabbitMQ: RabbitMQConfig{URL: "amqp://user:very-secret@rabbit.example/vhost"}}
	c.defaults()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if c.Consumer.Concurrency != 1 || c.Consumer.Timeout != 30*time.Second || c.Publisher.MaxPayloadBytes != 1024*1024 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(s, "very-secret") || strings.Contains(s, "user:") {
			t.Fatalf("secret leaked in %s", s)
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "very-secret") {
		t.Fatalf("secret leaked in JSON: %s", b)
	}
}

func TestValidateHeaders(t *testing.T) {
	if err := validateHeaders(map[string]string{"tenant.id": "abc"}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []map[string]string{{"x-internal": "x"}, {"bad key": "x"}} {
		if !errors.Is(validateHeaders(h), ErrInvalidHeader) {
			t.Errorf("accepted reserved/invalid header %v", h)
		}
	}
	tooMany := map[string]string{}
	for i := 0; i < 33; i++ {
		tooMany[fmt.Sprintf("h%d", i)] = "v"
	}
	if !errors.Is(validateHeaders(tooMany), ErrInvalidHeader) {
		t.Fatal("accepted too many headers")
	}
}

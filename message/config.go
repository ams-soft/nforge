package message

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ams-soft/nforge/codec"
)

// Config is the message section. Formatting and marshaling redact connection secrets.
type Config struct {
	Enabled       bool            `yaml:"enabled"`        // default: false
	Driver        string          `yaml:"driver"`         // default: rabbitmq
	Namespace     string          `yaml:"namespace"`      // default: nforge
	PayloadFormat string          `yaml:"payload_format"` // default: json
	RabbitMQ      RabbitMQConfig  `yaml:"rabbitmq"`
	Publisher     PublisherConfig `yaml:"publisher"`
	Consumer      ConsumerConfig  `yaml:"consumer"`
	Queues        []QueueConfig   `yaml:"queues"`
}

// RabbitMQConfig configures the broker URL, TLS and reconnect policy.
type RabbitMQConfig struct {
	URL            string          `yaml:"url"`             // required when enabled
	ConnectionName string          `yaml:"connection_name"` // default: app.name
	DialTimeout    time.Duration   `yaml:"dial_timeout"`    // default: 10s
	Heartbeat      time.Duration   `yaml:"heartbeat"`       // default: 30s
	TLS            RabbitTLSConfig `yaml:"tls"`
	Reconnect      ReconnectConfig `yaml:"reconnect"`
}

// RabbitTLSConfig configures custom trust roots for amqps.
type RabbitTLSConfig struct {
	CAFile string `yaml:"ca_file"` // default: system roots
}

// ReconnectConfig bounds the connection supervisor's exponential backoff.
type ReconnectConfig struct {
	InitialDelay time.Duration `yaml:"initial_delay"` // default: 1s
	MaxDelay     time.Duration `yaml:"max_delay"`     // default: 30s
	Multiplier   float64       `yaml:"multiplier"`    // default: 2
	Jitter       *bool         `yaml:"jitter"`        // default: true
}

// PublisherConfig sets publication confirmation and payload limits.
type PublisherConfig struct {
	ConfirmTimeout  time.Duration  `yaml:"confirm_timeout"`   // default: 10s
	MaxPayloadBytes codec.ByteSize `yaml:"max_payload_bytes"` // default: 1mb; at most codec.max_payload_bytes
}

// ConsumerConfig sets the defaults for registered consumers.
type ConsumerConfig struct {
	Concurrency int           `yaml:"concurrency"` // default: 1; at most 1024
	Prefetch    int           `yaml:"prefetch"`    // default: 0 (uses concurrency)
	Timeout     time.Duration `yaml:"timeout"`     // default: 30s
	AckMode     string        `yaml:"ack_mode"`    // default: automatic | manual
}

// QueueConfig declares a managed queue in YAML.
type QueueConfig struct {
	Name        string      `yaml:"name"`         // queue name
	Type        string      `yaml:"type"`         // classic (default) | quorum
	MaxPriority *uint8      `yaml:"max_priority"` // Classic only; 1..10
	DeadLetter  bool        `yaml:"dead_letter"`  // default: false
	Retry       RetryConfig `yaml:"retry"`        // requires dead_letter
	// OnFailure documents what happens to a message whose consumer fails:
	// "dead_letter" (needs dead_letter: true) or "drop" (the message is
	// discarded; the explicit form silences the startup warning).
	OnFailure string `yaml:"on_failure"` // default: unset
}

// RetryConfig enables a fixed-TTL retry bucket when dead_letter is enabled.
type RetryConfig struct {
	Delay       time.Duration `yaml:"delay"`        // minimum: 1ms
	MaxAttempts uint32        `yaml:"max_attempts"` // total handler invocations before the DLQ (1 = no retry); positive
}

func (q QueueConfig) spec() QueueSpec {
	s := Queue(q.Name)
	if q.Type == "quorum" {
		s = s.QuorumQueue()
	}
	if q.MaxPriority != nil {
		s = s.MaxPriority(*q.MaxPriority)
	}
	if q.DeadLetter {
		s = s.DeadLetter()
	}
	if q.OnFailure == "drop" {
		s = s.DropOnFailure()
	}
	if q.Retry.Delay != 0 || q.Retry.MaxAttempts != 0 {
		s = s.WithRetry(q.Retry.Delay, q.Retry.MaxAttempts)
	}
	return s
}
func (c *Config) defaults() {
	if c.Driver == "" {
		c.Driver = "rabbitmq"
	}
	if c.Namespace == "" {
		c.Namespace = "nforge"
	}
	if c.PayloadFormat == "" {
		c.PayloadFormat = "json"
	}
	if c.RabbitMQ.DialTimeout == 0 {
		c.RabbitMQ.DialTimeout = 10 * time.Second
	}
	if c.RabbitMQ.Heartbeat == 0 {
		c.RabbitMQ.Heartbeat = 30 * time.Second
	}
	if c.RabbitMQ.Reconnect.InitialDelay == 0 {
		c.RabbitMQ.Reconnect.InitialDelay = time.Second
	}
	if c.RabbitMQ.Reconnect.MaxDelay == 0 {
		c.RabbitMQ.Reconnect.MaxDelay = 30 * time.Second
	}
	if c.RabbitMQ.Reconnect.Multiplier == 0 {
		c.RabbitMQ.Reconnect.Multiplier = 2
	}
	if c.RabbitMQ.Reconnect.Jitter == nil {
		enabled := true
		c.RabbitMQ.Reconnect.Jitter = &enabled
	}
	if c.Publisher.ConfirmTimeout == 0 {
		c.Publisher.ConfirmTimeout = 10 * time.Second
	}
	if c.Publisher.MaxPayloadBytes == 0 {
		c.Publisher.MaxPayloadBytes = codec.MB
	}
	if c.Consumer.Concurrency == 0 {
		c.Consumer.Concurrency = 1
	}
	if c.Consumer.Timeout == 0 {
		c.Consumer.Timeout = 30 * time.Second
	}
	if c.Consumer.AckMode == "" {
		c.Consumer.AckMode = "automatic"
	}
}
func (c Config) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Driver != "rabbitmq" {
		return fmt.Errorf("message: driver must be rabbitmq")
	}
	if c.Namespace == "" {
		return fmt.Errorf("message: namespace is required")
	}
	if len(c.Namespace) > 240 || strings.HasPrefix(c.Namespace, "amq.") {
		return fmt.Errorf("message: namespace must be a valid managed exchange prefix")
	}
	u, e := url.Parse(c.RabbitMQ.URL)
	if e != nil || u == nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.Hostname() == "" || u.Fragment != "" {
		return fmt.Errorf("message: rabbitmq.url must be an amqp or amqps URL with a host")
	}
	if c.RabbitMQ.DialTimeout <= 0 || c.RabbitMQ.Heartbeat <= 0 || c.RabbitMQ.Reconnect.InitialDelay <= 0 || c.RabbitMQ.Reconnect.MaxDelay < c.RabbitMQ.Reconnect.InitialDelay || c.RabbitMQ.Reconnect.Multiplier < 1 || math.IsInf(c.RabbitMQ.Reconnect.Multiplier, 0) || math.IsNaN(c.RabbitMQ.Reconnect.Multiplier) || c.Publisher.ConfirmTimeout <= 0 || c.Publisher.MaxPayloadBytes <= 0 || c.Consumer.Concurrency < 1 || c.Consumer.Concurrency > 1024 || c.Consumer.Prefetch < 0 || c.Consumer.Prefetch > 65535 || c.Consumer.Timeout <= 0 || (c.Consumer.AckMode != "automatic" && c.Consumer.AckMode != "manual") {
		return fmt.Errorf("message: invalid duration, limit or consumer setting")
	}
	seen := map[string]bool{}
	for _, qc := range c.Queues {
		if qc.Type != "" && qc.Type != "classic" && qc.Type != "quorum" {
			return fmt.Errorf("%w: queue %s has invalid type", ErrInvalidQueue, qc.Name)
		}
		switch qc.OnFailure {
		case "", "drop":
		case "dead_letter":
			if !qc.DeadLetter {
				return fmt.Errorf("%w: queue %s: on_failure dead_letter requires dead_letter: true", ErrInvalidQueue, qc.Name)
			}
		default:
			return fmt.Errorf("%w: queue %s: on_failure must be drop or dead_letter", ErrInvalidQueue, qc.Name)
		}
		s := qc.spec()
		if err := s.validate(); err != nil {
			return err
		}
		if seen[s.name] {
			return fmt.Errorf("%w: duplicate queue %s", ErrInvalidQueue, s.name)
		}
		seen[s.name] = true
	}
	return nil
}
func (c Config) UsesTLS() bool { return strings.HasPrefix(c.RabbitMQ.URL, "amqps://") }
func (c Config) plaintextCredentials() bool {
	u, e := url.Parse(c.RabbitMQ.URL)
	if e != nil || u == nil || u.Scheme != "amqp" || u.User == nil {
		return false
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return !strings.EqualFold(h, "localhost") && (ip == nil || !ip.IsLoopback())
}
func (c Config) tlsConfig() (*tls.Config, error) {
	t := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.RabbitMQ.TLS.CAFile != "" {
		b, e := os.ReadFile(c.RabbitMQ.TLS.CAFile)
		if e != nil {
			return nil, fmt.Errorf("read RabbitMQ CA file")
		}
		roots, e := x509.SystemCertPool()
		if e != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("invalid RabbitMQ CA file")
		}
		t.RootCAs = roots
	}
	return t, nil
}
func (c Config) Redacted() Config {
	c.RabbitMQ.URL = "[REDACTED]"
	c.RabbitMQ.TLS.CAFile = "[REDACTED]"
	return c
}

type plainConfig Config

func (c Config) String() string               { return fmt.Sprintf("%+v", plainConfig(c.Redacted())) }
func (c Config) GoString() string             { return fmt.Sprintf("%#v", plainConfig(c.Redacted())) }
func (c Config) LogValue() slog.Value         { return slog.AnyValue(plainConfig(c.Redacted())) }
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(plainConfig(c.Redacted())) }

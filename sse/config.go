package sse

import (
	"fmt"
	"strings"
	"time"

	"github.com/ams-soft/nforge/codec"
)

// Routing modes accepted by sse.rabbitmq.routing.
const (
	RoutingFanout = "fanout"
	RoutingUser   = "user"
)

// Driver names accepted by sse.driver.
const (
	DriverMemory   = "memory"
	DriverRabbitMQ = "rabbitmq"
)

// Config is the sse section. The module is explicit: registering sse.Module is
// the opt-in, and an absent section means these defaults.
type Config struct {
	// Enabled is true when omitted; false keeps a registered module out of the run.
	Enabled *bool  `yaml:"enabled"`
	Driver  string `yaml:"driver"` // memory | rabbitmq; default: memory
	// Heartbeat is the interval of the ": heartbeat" comment frames that keep
	// proxies from closing an idle stream and reveal dead clients.
	Heartbeat time.Duration `yaml:"heartbeat"` // default: 15s
	// Retry is the reconnection delay advertised to browsers with "retry:".
	Retry time.Duration `yaml:"retry"` // default: 3s; 1s..1h
	// BufferSize is the bounded event queue of one connection; a client whose
	// queue fills is disconnected.
	BufferSize int `yaml:"buffer_size"` // default: 64
	// MaxConnections and MaxConnectionsPerUser bound the streams of this instance.
	MaxConnections        int `yaml:"max_connections"`          // default: 10000
	MaxConnectionsPerUser int `yaml:"max_connections_per_user"` // default: 5
	// MaxConnectsPerSecond limits the connection attempts of this instance
	// (a token bucket with a burst of the same size); it is no substitute for
	// per-client limits at the gateway.
	MaxConnectsPerSecond int `yaml:"max_connects_per_second"` // default: 100
	// WriteTimeout is the deadline of every frame write, so a client that
	// stops reading is dropped even though the stream has no overall timeout.
	WriteTimeout time.Duration `yaml:"write_timeout"` // default: 10s
	// MaxLifetime closes a stream after this long (with a 10% jitter) so a
	// token that expired or was revoked stops authorizing it; the browser
	// reconnects and authenticates again. Revocation is therefore only
	// effective at the next reconnection.
	MaxLifetime time.Duration `yaml:"max_lifetime"` // default: 15m
	// SendBufferBytes caps the kernel send buffer (SO_SNDBUF) of HTTP/1.1
	// stream connections. Without a cap the kernel auto-tunes it to megabytes
	// and absorbs everything sent to a client that never reads, so write_timeout
	// and the queue never trigger; with a small cap such a client is cut as soon
	// as the buffers fill. Zero keeps the operating system default. HTTP/2 and
	// HTTP/3 multiplex streams on one connection and are not affected.
	SendBufferBytes int          `yaml:"send_buffer_bytes"` // default: 0 (OS default); else 4096..16mb
	Limits          LimitsConfig `yaml:"limits"`
	// AllowedOrigins are the exact origins that may open a stream with an
	// Origin header. Empty accepts only the request's own origin. "*" is not accepted.
	AllowedOrigins []string       `yaml:"allowed_origins"`
	RabbitMQ       RabbitMQConfig `yaml:"rabbitmq"`
}

// LimitsConfig bounds what a publication may carry.
type LimitsConfig struct {
	MaxPayloadBytes  codec.ByteSize `yaml:"max_payload_bytes"`   // default: 64kb; at most 1mb
	MaxUserIDBytes   int            `yaml:"max_user_id_bytes"`   // default: 512 (the key holds issuer, tenant and subject)
	MaxEventNameSize int            `yaml:"max_event_name_size"` // default: 64
	MaxEventIDBytes  int            `yaml:"max_event_id_bytes"`  // default: 128
}

// RabbitMQConfig is the rabbitmq driver's part of the section.
type RabbitMQConfig struct {
	// Exchange is the fanout exchange; default <message.namespace>.sse.<env>.<app.name>
	// (sanitized), so applications sharing a broker never see each other's events.
	Exchange string `yaml:"exchange"`
	// Prefetch bounds the unacknowledged messages of this instance's queue.
	Prefetch int `yaml:"prefetch"` // default: 64
	// Routing selects how events reach the instances. "fanout" (default): every
	// instance receives every event and filters by user locally. "user": a direct
	// exchange where each instance binds only the users it has connected, so an
	// instance receives the events of its own users and nothing else (traffic
	// per instance no longer grows with the fleet). A user's events published
	// between its connection and the binding, or during an outage, are lost,
	// like everything in this module. The exchange is named <exchange>.user in
	// this mode, so switching modes never conflicts with the other type.
	Routing string `yaml:"routing"` // fanout | user; default: fanout
}

// IsEnabled reports whether the module runs.
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// ApplyDefaults fills what is unset.
func (c *Config) ApplyDefaults() {
	if c.Driver == "" {
		c.Driver = DriverMemory
	}
	dur := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	num := func(n *int, v int) {
		if *n == 0 {
			*n = v
		}
	}
	dur(&c.Heartbeat, 15*time.Second)
	dur(&c.Retry, 3*time.Second)
	dur(&c.WriteTimeout, 10*time.Second)
	dur(&c.MaxLifetime, 15*time.Minute)
	num(&c.BufferSize, 64)
	num(&c.MaxConnections, 10000)
	num(&c.MaxConnectionsPerUser, 5)
	num(&c.MaxConnectsPerSecond, 100)
	num(&c.Limits.MaxUserIDBytes, 512)
	num(&c.Limits.MaxEventNameSize, 64)
	num(&c.Limits.MaxEventIDBytes, 128)
	num(&c.RabbitMQ.Prefetch, 64)
	if c.RabbitMQ.Routing == "" {
		c.RabbitMQ.Routing = RoutingFanout
	}
	if c.Limits.MaxPayloadBytes == 0 {
		c.Limits.MaxPayloadBytes = 64 * codec.KB
	}
}

// Validate checks the section alone; cross-module rules live in the module's Check.
func (c Config) Validate() error {
	switch c.Driver {
	case DriverMemory, DriverRabbitMQ:
	default:
		return fmt.Errorf("sse.driver must be %q or %q, got %q", DriverMemory, DriverRabbitMQ, c.Driver)
	}
	if c.Heartbeat < time.Second || c.Heartbeat > time.Hour {
		return fmt.Errorf("sse.heartbeat must be between 1s and 1h, got %s", c.Heartbeat)
	}
	if c.Retry < time.Second || c.Retry > time.Hour {
		return fmt.Errorf("sse.retry must be between 1s and 1h, got %s", c.Retry)
	}
	if c.WriteTimeout < time.Second || c.WriteTimeout > time.Hour {
		return fmt.Errorf("sse.write_timeout must be between 1s and 1h, got %s", c.WriteTimeout)
	}
	if c.MaxLifetime < time.Minute {
		return fmt.Errorf("sse.max_lifetime must be at least 1m, got %s", c.MaxLifetime)
	}
	for _, f := range []struct {
		name  string
		value int
		max   int
	}{
		{"buffer_size", c.BufferSize, 65536},
		{"max_connections", c.MaxConnections, 10_000_000},
		{"max_connections_per_user", c.MaxConnectionsPerUser, 1000},
		{"max_connects_per_second", c.MaxConnectsPerSecond, 1_000_000},
		{"limits.max_user_id_bytes", c.Limits.MaxUserIDBytes, 1024},
		{"limits.max_event_name_size", c.Limits.MaxEventNameSize, 256},
		{"limits.max_event_id_bytes", c.Limits.MaxEventIDBytes, 1024},
		{"rabbitmq.prefetch", c.RabbitMQ.Prefetch, 65535},
	} {
		if f.value < 1 || f.value > f.max {
			return fmt.Errorf("sse.%s must be between 1 and %d, got %d", f.name, f.max, f.value)
		}
	}
	if c.MaxConnectionsPerUser > c.MaxConnections {
		return fmt.Errorf("sse.max_connections_per_user (%d) must not exceed sse.max_connections (%d)", c.MaxConnectionsPerUser, c.MaxConnections)
	}
	if c.Limits.MaxPayloadBytes < 1 || c.Limits.MaxPayloadBytes > codec.MB {
		return fmt.Errorf("sse.limits.max_payload_bytes must be between 1b and 1mb, got %d", int64(c.Limits.MaxPayloadBytes))
	}
	if c.RabbitMQ.Routing != RoutingFanout && c.RabbitMQ.Routing != RoutingUser {
		return fmt.Errorf("sse.rabbitmq.routing must be %q or %q, got %q", RoutingFanout, RoutingUser, c.RabbitMQ.Routing)
	}
	if c.SendBufferBytes != 0 && (c.SendBufferBytes < 4096 || c.SendBufferBytes > 16<<20) {
		return fmt.Errorf("sse.send_buffer_bytes must be 0 or between 4096 and 16777216, got %d", c.SendBufferBytes)
	}
	for _, o := range c.AllowedOrigins {
		httpOrigin := strings.HasPrefix(o, "https://") || strings.HasPrefix(o, "http://")
		if o == "*" || o == "" || o != strings.ToLower(o) || strings.HasSuffix(o, "/") || !httpOrigin {
			return fmt.Errorf("sse.allowed_origins entries must be exact lowercase origins such as https://app.example.com, got %q", o)
		}
	}
	return nil
}

// exchangeName is the default fanout exchange of an application.
func exchangeName(namespace, env, app string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
				b.WriteRune(r)
			default:
				b.WriteByte('-')
			}
		}
		return b.String()
	}
	name := clean(namespace) + ".sse." + clean(env) + "." + clean(app)
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

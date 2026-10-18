package database

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is the `database:` section of the Nano Forge configuration.
// It contains secrets: never log it with %v/%+v (LogValue redacts it for slog).
type Config struct {
	Enabled bool `yaml:"enabled"` // deps.NewDeps only opens the pool when true

	Driver string `yaml:"driver"` // postgres | postgresql; empty = postgres

	DSN string `yaml:"dsn"` // takes precedence over the individual fields

	Host     string `yaml:"host"`
	Port     uint16 `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`

	SSL SSLConfig `yaml:"ssl"`

	Connection ConnectionConfig `yaml:"connection"`

	Pool PoolConfig `yaml:"pool"`

	Tenancy TenancyConfig `yaml:"tenancy"`
}

type SSLConfig struct {
	Mode string `yaml:"mode"` // libpq sslmode; empty = pgx default (prefer)
}

type ConnectionConfig struct {
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
}

// PoolConfig overrides pgxpool settings; zero values keep pgx defaults (or
// the values embedded in the DSN).
type PoolConfig struct {
	MinConns int32 `yaml:"min_conns"`
	MaxConns int32 `yaml:"max_conns"`

	MaxConnLifetime   time.Duration `yaml:"max_conn_lifetime"`
	MaxConnIdleTime   time.Duration `yaml:"max_conn_idle_time"`
	HealthCheckPeriod time.Duration `yaml:"health_check_period"`
}

// ConnectionString returns the DSN to hand to pgxpool.ParseConfig.
// A non-empty DSN wins and is returned unchanged; otherwise a postgres://
// URL is built from the individual fields with proper escaping.
// The returned string may contain credentials and must never be logged.
func (c Config) ConnectionString() (string, error) {
	switch strings.ToLower(strings.TrimSpace(c.Driver)) {
	case "", "postgres", "postgresql":
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedDriver, c.Driver)
	}

	if strings.TrimSpace(c.DSN) != "" {
		return c.DSN, nil
	}

	var missing []string
	if strings.TrimSpace(c.Host) == "" {
		missing = append(missing, "host")
	}
	if c.Port == 0 {
		missing = append(missing, "port")
	}
	if strings.TrimSpace(c.Name) == "" {
		missing = append(missing, "name")
	}
	if strings.TrimSpace(c.User) == "" {
		missing = append(missing, "user")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("%w: %s", ErrMissingConfig, strings.Join(missing, ", "))
	}

	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))),
		Path:   c.Name,
	}

	query := dsn.Query()
	if c.SSL.Mode != "" {
		query.Set("sslmode", c.SSL.Mode)
	}
	dsn.RawQuery = query.Encode()

	return dsn.String(), nil
}

// LogValue makes accidental structured logging safe: the password is omitted
// and the DSN is redacted.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("enabled", c.Enabled),
		slog.String("driver", c.Driver),
		slog.String("dsn", redactDSN(c.DSN)),
		slog.String("host", c.Host),
		slog.Int("port", int(c.Port)),
		slog.String("name", c.Name),
		slog.String("user", c.User),
		slog.String("ssl_mode", c.SSL.Mode),
		slog.Bool("tenancy", c.Tenancy.Enabled),
		slog.String("tenancy_mode", c.Tenancy.Mode),
	)
}

func redactDSN(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return ""
	}
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		return u.Redacted()
	}
	return "[REDACTED]"
}

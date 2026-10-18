package database

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		Host:     "localhost",
		Port:     5432,
		Name:     "nano_forge",
		User:     "postgres",
		Password: "secret",
		SSL:      SSLConfig{Mode: "disable"},
	}
}

func TestConnectionStringDSNWins(t *testing.T) {
	cfg := validConfig()
	cfg.DSN = "postgres://other:pw@db.internal:6543/app"
	got, err := cfg.ConnectionString()
	if err != nil || got != cfg.DSN {
		t.Fatalf("got %q, %v; want DSN unchanged", got, err)
	}
}

func TestConnectionStringBlankDSNFallsBack(t *testing.T) {
	cfg := validConfig()
	cfg.DSN = "   "
	got, err := cfg.ConnectionString()
	if err != nil || !strings.HasPrefix(got, "postgres://postgres:secret@localhost:5432/") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestConnectionStringExample(t *testing.T) {
	got, err := validConfig().ConnectionString()
	want := "postgres://postgres:secret@localhost:5432/nano_forge?sslmode=disable"
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestConnectionStringEscapesPassword(t *testing.T) {
	for _, pw := range []string{"p@ss:w/rd#1", "a?b%c d[e]f"} {
		cfg := validConfig()
		cfg.Password = pw
		got, err := cfg.ConnectionString()
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse %q: %v", got, err)
		}
		if p, _ := u.User.Password(); p != pw {
			t.Errorf("password round-trip: got %q want %q", p, pw)
		}
		if u.Hostname() != "localhost" || u.Port() != "5432" || u.Path != "/nano_forge" {
			t.Errorf("host/port/path corrupted: %q", got)
		}
	}
	cfg := validConfig()
	cfg.Password = "p@ss:w/rd#1"
	got, _ := cfg.ConnectionString()
	want := "postgres://postgres:p%40ss%3Aw%2Frd%231@localhost:5432/nano_forge?sslmode=disable"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestConnectionStringIPv6(t *testing.T) {
	cfg := validConfig()
	cfg.Host = "::1"
	got, err := cfg.ConnectionString()
	if err != nil || !strings.Contains(got, "@[::1]:5432/") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestConnectionStringNoSSLMode(t *testing.T) {
	cfg := validConfig()
	cfg.SSL.Mode = ""
	got, err := cfg.ConnectionString()
	if err != nil || strings.Contains(got, "sslmode") || strings.Contains(got, "?") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestConnectionStringMissingFields(t *testing.T) {
	cases := map[string]func(*Config){
		"host": func(c *Config) { c.Host = " " },
		"port": func(c *Config) { c.Port = 0 },
		"name": func(c *Config) { c.Name = "" },
		"user": func(c *Config) { c.User = "" },
	}
	for field, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)
		_, err := cfg.ConnectionString()
		if !errors.Is(err, ErrMissingConfig) || !strings.HasSuffix(err.Error(), ": "+field) {
			t.Errorf("%s: got %v", field, err)
		}
	}

	_, err := Config{Password: "secret"}.ConnectionString()
	if !errors.Is(err, ErrMissingConfig) || !strings.HasSuffix(err.Error(), ": host, port, name, user") {
		t.Errorf("all missing: got %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks password: %v", err)
	}

	cfg := validConfig()
	cfg.Password = ""
	if _, err := cfg.ConnectionString(); err != nil {
		t.Errorf("empty password must be allowed: %v", err)
	}
}

func TestConnectionStringDriver(t *testing.T) {
	for _, d := range []string{"", "postgres", "PostgreSQL", " postgresql "} {
		cfg := validConfig()
		cfg.Driver = d
		if _, err := cfg.ConnectionString(); err != nil {
			t.Errorf("driver %q: %v", d, err)
		}
	}

	cfg := validConfig()
	cfg.Driver = "mysql"
	cfg.DSN = "postgres://u:secret@h/db"
	_, err := cfg.ConnectionString()
	if !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestLogValueRedactsSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.DSN = "postgres://u:topsecret@h/db"
	out := cfg.LogValue().String()
	if strings.Contains(out, "secret") {
		t.Errorf("LogValue leaks secrets: %s", out)
	}
}

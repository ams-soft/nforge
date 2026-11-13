package email

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func valid() Config {
	c := Config{From: FromConfig{Address: "no-reply@example.com", Name: "Example"}, SMTP: SMTPConfig{Host: "smtp.example.com", Username: "u", Password: "SECRET-pw"}}
	c.ApplyDefaults()
	return c
}

func TestDefaults(t *testing.T) {
	c := valid()
	if c.Driver != "smtp" || c.SMTP.Security != SecurityStartTLS || c.SMTP.Port != 587 || c.Timeout != defaultTimeout {
		t.Fatalf("%+v", c)
	}
	c = Config{SMTP: SMTPConfig{Security: SecurityTLS}}
	c.ApplyDefaults()
	if c.SMTP.Port != 465 {
		t.Fatalf("tls port %d", c.SMTP.Port)
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	mut := func(f func(*Config)) Config { c := valid(); f(&c); return c }
	for name, c := range map[string]Config{
		"driver":        mut(func(c *Config) { c.Driver = "ses" }),
		"no from":       mut(func(c *Config) { c.From.Address = "" }),
		"from invalid":  mut(func(c *Config) { c.From.Address = "not an address" }),
		"from name ctl": mut(func(c *Config) { c.From.Name = "a\r\nb" }),
		"no host":       mut(func(c *Config) { c.SMTP.Host = "" }),
		"host scheme":   mut(func(c *Config) { c.SMTP.Host = "smtp://x" }),
		"host port":     mut(func(c *Config) { c.SMTP.Host = "x:587" }),
		"port zero-neg": mut(func(c *Config) { c.SMTP.Port = -1 }),
		"port big":      mut(func(c *Config) { c.SMTP.Port = 70000 }),
		"security":      mut(func(c *Config) { c.SMTP.Security = "none" }),
		"user only":     mut(func(c *Config) { c.SMTP.Password = "" }),
		"pass only":     mut(func(c *Config) { c.SMTP.Username = "" }),
		"cred ctl":      mut(func(c *Config) { c.SMTP.Password = "a\nb" }),
		"timeout neg":   mut(func(c *Config) { c.Timeout = -1 }),
		"timeout big":   mut(func(c *Config) { c.Timeout = maxTimeout + 1 }),
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := mut(func(c *Config) { c.Driver = "ses" }).Validate(); !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("driver error = %v", err)
	}
	ok := mut(func(c *Config) { c.SMTP.Username, c.SMTP.Password = "", "" })
	if err := ok.Validate(); err != nil {
		t.Fatalf("no-auth relay: %v", err)
	}
}

func TestSecretsNeverPrinted(t *testing.T) {
	c := valid()
	c.SMTP.Username = "USER-name"
	var b strings.Builder
	h := slog.New(slog.NewJSONHandler(&b, nil))
	h.Info("cfg", "config", c, "smtp", c.SMTP)
	text := b.String()
	for _, v := range []string{"%v", "%+v", "%#v", "%s"} {
		text += fmt.Sprintf(v, c) + fmt.Sprintf(v, c.SMTP) + fmt.Sprintf(v, &c)
	}
	for _, secret := range []string{"SECRET-pw", "USER-name"} {
		if strings.Contains(text, secret) {
			t.Fatalf("%q leaked: %s", secret, text)
		}
	}
}

package email

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"time"
	"unicode/utf8"
)

// Drivers and SMTP security modes. Only SMTP is implemented.
const (
	DriverSMTP = "smtp"
	// DriverNoop discards every message after validating and encoding it. It is
	// for tests and local runs that must not reach a server; it needs no smtp section.
	DriverNoop = "noop"

	SecurityStartTLS = "starttls" // plain connect, then a mandatory STARTTLS upgrade
	SecurityTLS      = "tls"      // implicit TLS from the first byte (normally port 465)
)

const (
	defaultTimeout = 15 * time.Second
	maxTimeout     = 5 * time.Minute
	maxNameBytes   = 128
)

// ErrUnsupportedDriver is returned for an email.driver other than "smtp".
var ErrUnsupportedDriver = errors.New("email: driver is not supported")

// Config is the email section.
type Config struct {
	// Enabled is true when omitted; false keeps a registered module out of the run.
	Enabled *bool `yaml:"enabled"`
	// Driver selects the transport: "smtp" (default) or "noop" (tests: discards every message).
	Driver string `yaml:"driver"`
	// From is the sender of every message.
	From FromConfig `yaml:"from"`
	// Timeout bounds one whole Send (dial, TLS, AUTH, envelope, DATA, close); default 15s, at most 5m.
	Timeout time.Duration `yaml:"timeout"`
	// SMTP configures the smtp driver.
	SMTP SMTPConfig `yaml:"smtp"`
}

// FromConfig is the default sender.
type FromConfig struct {
	// Address is the bare ASCII mailbox of the envelope sender and the From header. Required.
	Address string `yaml:"address"`
	// Name is the optional display name; any Unicode text without control characters.
	Name string `yaml:"name"`
}

// SMTPConfig is the smtp driver section.
type SMTPConfig struct {
	// Host is the server name used to dial and to verify its certificate. Required.
	Host string `yaml:"host"`
	// Port defaults to 587 for starttls and 465 for tls.
	Port int `yaml:"port"`
	// Username and Password enable AUTH PLAIN, only ever sent over TLS. Both or neither.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Security is "starttls" (default) or "tls". There is no plaintext mode.
	Security string `yaml:"security"`
}

// IsEnabled reports whether the module takes part in the run.
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// ApplyDefaults fills driver, timeout, security and port.
func (c *Config) ApplyDefaults() {
	if c.Driver == "" {
		c.Driver = DriverSMTP
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.SMTP.Security == "" {
		c.SMTP.Security = SecurityStartTLS
	}
	if c.SMTP.Port == 0 {
		if c.SMTP.Security == SecurityTLS {
			c.SMTP.Port = 465
		} else {
			c.SMTP.Port = 587
		}
	}
}

// Validate checks the section without any I/O.
func (c Config) Validate() error {
	if c.Driver != DriverSMTP && c.Driver != DriverNoop {
		return fmt.Errorf("%w: email.driver %q (want %q or %q)", ErrUnsupportedDriver, c.Driver, DriverSMTP, DriverNoop)
	}
	if c.Timeout < 0 || c.Timeout > maxTimeout {
		return fmt.Errorf("email.timeout must be between 0 (default %s) and %s, got %s", defaultTimeout, maxTimeout, c.Timeout)
	}
	if c.From.Address == "" {
		return errors.New("email.from.address is required")
	}
	if err := checkAddress(c.From.Address); err != nil {
		return fmt.Errorf("email.from.address: %w", err)
	}
	if err := checkDisplayName(c.From.Name); err != nil {
		return fmt.Errorf("email.from.name: %w", err)
	}
	if c.Driver == DriverNoop {
		return nil
	}
	s := c.SMTP
	if err := checkHost(s.Host); err != nil {
		return fmt.Errorf("email.smtp.host: %w", err)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("email.smtp.port must be 1..65535, got %d", s.Port)
	}
	if s.Security != SecurityStartTLS && s.Security != SecurityTLS {
		return fmt.Errorf("email.smtp.security must be %q or %q, got %q", SecurityStartTLS, SecurityTLS, s.Security)
	}
	if (s.Username == "") != (s.Password == "") {
		return errors.New("email.smtp.username and email.smtp.password must be set together")
	}
	if hasControl(s.Username) || hasControl(s.Password) {
		return errors.New("email.smtp credentials must not contain control characters")
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func checkHost(h string) error {
	switch {
	case h == "":
		return errors.New("is required")
	case len(h) > 253:
		return errors.New("is too long")
	case net.ParseIP(h) != nil:
		return nil
	}
	for _, r := range h {
		ok := r == '-' || r == '.' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !ok {
			return errors.New("must be a host name or IP address without scheme, port or path")
		}
	}
	return nil
}

// checkAddress accepts one bare ASCII mailbox: no display name, no angle
// brackets, no whitespace or control characters. Internationalized mailboxes
// (SMTPUTF8) are not implemented, so non-ASCII is refused rather than corrupted.
func checkAddress(a string) error {
	if len(a) > 254 {
		return errors.New("address is too long")
	}
	for i := range len(a) {
		if a[i] <= 0x20 || a[i] >= 0x7f {
			return errors.New("address must be one bare ASCII mailbox (no spaces, control or non-ASCII characters; use punycode for the domain)")
		}
	}
	p, err := mail.ParseAddress(a)
	if err != nil || p.Name != "" || p.Address != a {
		return errors.New("address is not a valid bare mailbox")
	}
	return nil
}

func checkDisplayName(n string) error {
	if len(n) > maxNameBytes {
		return fmt.Errorf("must be at most %d bytes", maxNameBytes)
	}
	if !utf8.ValidString(n) || hasControl(n) {
		return errors.New("must be valid UTF-8 without control characters")
	}
	return nil
}

// redacted returns a copy that is safe to print.
func (s SMTPConfig) redacted() SMTPConfig {
	if s.Password != "" {
		s.Password = "[redacted]"
	}
	if s.Username != "" {
		s.Username = "[redacted]"
	}
	return s
}

// Format redacts the credentials for every verb, also when SMTPConfig is
// printed inside Config.
func (s SMTPConfig) Format(f fmt.State, verb rune) {
	type plain SMTPConfig
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), plain(s.redacted()))
}

// LogValue reports the shape of the configuration for slog: no credentials,
// no addresses, no host.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("enabled", c.IsEnabled()),
		slog.String("driver", c.Driver),
		slog.String("security", c.SMTP.Security),
		slog.Bool("auth", c.SMTP.Username != ""),
		slog.Duration("timeout", c.Timeout),
	)
}

// String is a one-line summary without secrets.
func (c Config) String() string {
	return fmt.Sprintf("email.Config{driver=%s security=%s}", c.Driver, c.SMTP.Security)
}

// GoString matches String.
func (c Config) GoString() string { return c.String() }

// LogValue logs the shape of the server section without credentials or host.
func (s SMTPConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("security", s.Security), slog.Int("port", s.Port), slog.Bool("auth", s.Username != ""))
}

// MarshalJSON redacts the credentials.
func (s SMTPConfig) MarshalJSON() ([]byte, error) {
	type plain SMTPConfig
	return json.Marshal(plain(s.redacted()))
}

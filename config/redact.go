package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/ams-soft/nforge/jose"
)

const redactedValue = "[REDACTED]"

// Redacted returns a copy of the configuration that is safe to print or log:
// every field that can hold a credential, a key or a key path is masked. It
// is what String, GoString, LogValue, MarshalJSON and MarshalYAML print, so a
// whole Config can be logged with any formatter or handler (including the
// slog JSON handler, which never consults the redactors of nested fields).
//
// The returned value shares no mutable data that Redacted changes with c.
func (c Config) Redacted() Config {
	c.Database = c.Database.RedactedCopy()
	c.Logging.Output.Otel.Endpoint = redactEndpoint(c.Logging.Output.Otel.Endpoint)
	c.Tracing.Otel.Endpoint = redactEndpoint(c.Tracing.Otel.Endpoint)
	if c.HTTP.Server.TLS.KeyFile != "" {
		c.HTTP.Server.TLS.KeyFile = redactedValue
	}
	if c.HTTP.Server.HTTP3.KeyFile != "" {
		c.HTTP.Server.HTTP3.KeyFile = redactedValue
	}
	if c.Cache.Redis.Password != "" {
		c.Cache.Redis.Password = redactedValue
	}
	if c.Cache.Redis.URL != "" {
		c.Cache.Redis.URL = redactedValue
	}
	if c.Cache.Redis.TLS.CAFile != "" {
		c.Cache.Redis.TLS.CAFile = redactedValue
	}
	if len(c.JOSE.Keys) > 0 {
		keys := make([]jose.KeyConfig, len(c.JOSE.Keys))
		copy(keys, c.JOSE.Keys)
		for i := range keys {
			if keys[i].Secret != "" {
				keys[i].Secret = redactedValue
			}
			if keys[i].Source.File != "" {
				keys[i].Source.File = redactedValue
			}
		}
		c.JOSE.Keys = keys
	}
	return c
}

// plainConfig has none of Config's methods, so printing it cannot recurse
// into the redactors below.
type plainConfig Config

// String implements fmt.Stringer with the redacted configuration.
func (c Config) String() string { return fmt.Sprintf("%+v", plainConfig(c.Redacted())) }

// GoString implements fmt.GoStringer with the redacted configuration.
func (c Config) GoString() string { return fmt.Sprintf("%#v", plainConfig(c.Redacted())) }

// LogValue implements slog.LogValuer. It hands the handler the redacted
// copy, so text and JSON handlers alike print masked values.
func (c Config) LogValue() slog.Value { return slog.AnyValue(plainConfig(c.Redacted())) }

// MarshalJSON marshals the redacted configuration.
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(plainConfig(c.Redacted())) }

// MarshalYAML marshals the redacted configuration.
func (c Config) MarshalYAML() (any, error) { return plainConfig(c.Redacted()), nil }

// redactEndpoint masks the userinfo of an endpoint URL
// ("https://user:token@host:4317") and drops its query; a bare "host:port"
// has neither and is returned unchanged.
func redactEndpoint(endpoint string) string {
	if !strings.Contains(endpoint, "://") {
		return endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return redactedValue
	}
	if u.User != nil {
		u.User = url.User(redactedValue)
	}
	u.RawQuery = ""
	return u.String()
}

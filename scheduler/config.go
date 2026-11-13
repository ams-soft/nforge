package scheduler

import (
	"errors"
	"fmt"
	"time"
)

// Execution modes. Only local is implemented.
const (
	ModeLocal       = "local"
	ModeDistributed = "distributed"
)

// ErrUnsupportedMode is returned for scheduler.mode: distributed, which is
// reserved for the future and never runs locally instead.
var ErrUnsupportedMode = errors.New("scheduler: mode is not supported")

// Config is the scheduler section.
type Config struct {
	// Enabled is true when omitted; false keeps a registered module out of the run.
	Enabled *bool `yaml:"enabled"`
	// Mode is "local" (default). "distributed" is reserved and fails the configuration.
	Mode string `yaml:"mode"`
	// Timezone is the IANA zone the expressions are evaluated in (default UTC).
	Timezone string `yaml:"timezone"`
	// MaxConcurrency is the default cap of simultaneous executions of one job;
	// 0 (default) is unlimited, a firing at the cap is skipped. Negative is an error.
	MaxConcurrency int `yaml:"max_concurrency"`
}

// IsEnabled reports whether the module takes part in the run.
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// ApplyDefaults fills mode and timezone.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = ModeLocal
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
}

// Validate checks the section and returns the loaded location. It reads the
// time zone database but opens no connection.
func (c Config) Validate() (*time.Location, error) {
	switch c.Mode {
	case ModeLocal:
	case ModeDistributed:
		return nil, fmt.Errorf("%w: scheduler.mode %q is reserved and not implemented; use %q", ErrUnsupportedMode, c.Mode, ModeLocal)
	default:
		return nil, fmt.Errorf("scheduler.mode must be %q, got %q", ModeLocal, c.Mode)
	}
	if c.MaxConcurrency < 0 {
		return nil, fmt.Errorf("scheduler.max_concurrency must not be negative, got %d", c.MaxConcurrency)
	}
	if c.Timezone == "Local" {
		return nil, errors.New(`scheduler.timezone "Local" depends on the host; use an IANA name such as "UTC" or "America/Sao_Paulo"`)
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("scheduler.timezone %q: %w", c.Timezone, err)
	}
	return loc, nil
}

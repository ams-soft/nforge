// Package config reads the configuration of an nForge application once: one
// logical YAML document (a base file, ${VAR} expansion from the process
// environment, an optional env file and the overlay of the environment) split
// into immutable raw sections. It knows no module schema: the module that owns
// a section decodes it, applies its defaults and validates it (package module,
// Source.Decode). Only the reserved application sections are typed here: env,
// app and prd_exceptions.
//
// Precedence, later wins, per section: base YAML, env file, overlay. An
// unknown key is an error in every section, in the YAML and in the env file,
// and so is a section that no registered module owns: a typo in a security
// setting must not silently fall back to its default. Top-level keys starting
// with "x-" are ignored, as room for application data in the same file.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultConfigFileName = "config.yaml"

// Reserved top-level sections, decoded by this package.
const (
	SectionEnv           = "env"
	SectionApp           = "app"
	SectionPrdExceptions = "prd_exceptions"
)

// AppConfig is the app section.
type AppConfig struct {
	Name    string `yaml:"name"`    // REQUIRED
	Version string `yaml:"version"` // REQUIRED
	// InternalUse declares that the service is not reachable from the internet
	// (default false). It only silences the prd warnings about unauthenticated
	// maintenance endpoints; every other check is unaffected.
	InternalUse bool `yaml:"internal_use"`
}

// PrdExceptions lists the insecure settings an operator accepts in production
// on purpose. Every flag defaults to false: with env prd the matching setting
// is then a load error instead of a startup warning. The keys are owned by the
// modules that report the settings; unknown keys are errors.
type PrdExceptions struct {
	TLSKeyPermissions bool `yaml:"tls_key_permissions"` // TLS private key readable by group or others
	RedisPlaintext    bool `yaml:"redis_plaintext"`     // Redis without TLS (credentials, or behind HTTPS/HTTP/3)
	DatabasePlaintext bool `yaml:"database_plaintext"`  // PostgreSQL sslmode that can fall back to clear text
}

// Allows reports whether the named exception is set.
func (e PrdExceptions) Allows(name string) bool {
	switch name {
	case "tls_key_permissions":
		return e.TLSKeyPermissions
	case "redis_plaintext":
		return e.RedisPlaintext
	case "database_plaintext":
		return e.DatabasePlaintext
	}
	return false
}

// Document is a loaded configuration. It is immutable and safe for concurrent
// use; Bind gives each application its own decoding session.
type Document struct {
	env         string
	app         AppConfig
	prd         PrdExceptions
	onlineSince time.Time
	sections    map[string]*section
	order       []string // section names, in file order
	envVars     map[string]string
	reserved    map[string]bool // env file keys consumed by the reserved sections
}

// section holds the raw value of one top-level key, per layer.
type section struct {
	base, overlay *yaml.Node
}

// Env returns the environment: local, dev, stg or prd.
func (d *Document) Env() string { return d.env }

// App returns the app section.
func (d *Document) App() AppConfig { return d.app }

// PrdExceptions returns the prd_exceptions section.
func (d *Document) PrdExceptions() PrdExceptions { return d.prd }

// OnlineSince is when the configuration was loaded.
func (d *Document) OnlineSince() time.Time { return d.onlineSince }

// Sections returns the names of the non-reserved sections present, in the
// order they first appear.
func (d *Document) Sections() []string { return slices.Clone(d.order) }

// Load parses the flags -config and -envfile (it calls flag.Parse: define your
// own flags before) and loads the configuration; see LoadFlags.
func Load() (*Document, error) {
	configPath := flag.String("config", "", "path to the base config.yaml")
	envFilePath := flag.String("envfile", "", "path to the env overrides file")
	flag.Parse()
	return LoadFlags(*configPath, *envFilePath)
}

// LoadFlags loads configPath (default ./config.yaml in the working directory,
// which may then be absent), the env file at envFilePath (optional) and the
// overlay of the environment, config.<env>.yaml in the working directory
// (dev, stg and prd; a missing overlay is skipped).
func LoadFlags(configPath, envFilePath string) (*Document, error) {
	basePath := configPath
	if basePath == "" {
		root, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
		basePath = filepath.Join(root, defaultConfigFileName)
	}
	// The default ./config.yaml may be absent (everything can come from the
	// env file and defaults); a path the caller named must exist, otherwise a
	// typo in -config silently starts the app with an empty configuration.
	base, err := readYAMLFile(basePath, configPath != "")
	if err != nil {
		return nil, fmt.Errorf("load base config %q: %w", basePath, err)
	}
	d := &Document{sections: map[string]*section{}, envVars: map[string]string{}, reserved: map[string]bool{}}
	if envFilePath != "" {
		if d.envVars, err = readEnvFile(envFilePath); err != nil {
			return nil, fmt.Errorf("apply env file %q: %w", envFilePath, err)
		}
	}

	env := "local"
	if err := d.decodeLayer(base, SectionEnv, &env); err != nil {
		return nil, fmt.Errorf("load base config %q: %w", basePath, err)
	}
	if v, ok := d.envVars["ENV"]; ok {
		env, d.reserved["ENV"] = v, true
	}
	if env, err = normalizeEnv(env); err != nil {
		return nil, err
	}

	var overlay *yaml.Node
	if path := overlayFileFor(env); path != "" {
		// Only a missing overlay is skipped: an unreadable one (permissions,
		// I/O error) must not silently drop the environment's settings.
		if _, err := os.Stat(path); err == nil {
			if overlay, err = readYAMLFile(path, true); err != nil {
				return nil, fmt.Errorf("load overlay %q: %w", path, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stat overlay %q: %w", path, err)
		}
	}
	if err := d.decodeLayer(overlay, SectionEnv, &env); err != nil { // an overlay may set env too
		return nil, fmt.Errorf("load overlay: %w", err)
	}
	if d.env, err = normalizeEnv(env); err != nil {
		return nil, err
	}

	for _, layer := range []struct {
		root    *yaml.Node
		overlay bool
	}{{base, false}, {overlay, true}} {
		for name, value := range topLevel(layer.root) {
			if name == SectionEnv {
				continue
			}
			s := d.sections[name]
			if s == nil {
				s = &section{}
				d.sections[name] = s
				if name != SectionApp && name != SectionPrdExceptions {
					d.order = append(d.order, name)
				}
			}
			if layer.overlay {
				s.overlay = value
			} else {
				s.base = value
			}
		}
	}
	for name, dst := range map[string]any{SectionApp: &d.app, SectionPrdExceptions: &d.prd} {
		used := map[string]bool{}
		if err := d.decode(name, dst, used); err != nil {
			return nil, err
		}
		for k := range used {
			d.reserved[k] = true
		}
		delete(d.sections, name)
	}
	if d.app.Name == "" {
		return nil, errors.New("validate config: app.name must not be empty")
	}
	if d.app.Version == "" {
		return nil, errors.New("validate config: app.version must not be empty")
	}
	d.onlineSince = time.Now().Truncate(time.Second)
	return d, nil
}

// environments are the accepted values of env, in the form they are stored.
var environments = []string{"local", "dev", "stg", "prd"}

// normalizeEnv lower-cases env and rejects anything but local, dev, stg or
// prd. Both the config overlay and the log level are chosen from it, so a
// misspelling ("production", "prod") would otherwise run production with
// debug logging and without its overlay.
func normalizeEnv(env string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(env))
	if !slices.Contains(environments, n) {
		return "", fmt.Errorf("env must be one of %s, got %q", strings.Join(environments, ", "), env)
	}
	return n, nil
}

func overlayFileFor(env string) string {
	switch env {
	case "prd", "stg", "dev":
		return "config." + env + ".yaml"
	default:
		return ""
	}
}

// decodeLayer decodes one top-level key of a single layer, strictly.
func (d *Document) decodeLayer(root *yaml.Node, name string, dst any) error {
	for key, value := range topLevel(root) {
		if key == name {
			return decodeNode(name, value, dst)
		}
	}
	return nil
}

// decode runs the pipeline of one section: base, env file, overlay.
func (d *Document) decode(name string, dst any, used map[string]bool) error {
	s := d.sections[name]
	if s != nil && s.base != nil {
		if err := decodeNode(name, s.base, dst); err != nil {
			return fmt.Errorf("load base config: %w", err)
		}
	}
	if err := applyEnvVars(dst, name, d.envVars, used); err != nil {
		return fmt.Errorf("apply env file: %w", err)
	}
	if s != nil && s.overlay != nil {
		if err := decodeNode(name, s.overlay, dst); err != nil {
			return fmt.Errorf("load overlay: %w", err)
		}
	}
	return nil
}

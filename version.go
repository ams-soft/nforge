// Package nforge holds framework-level metadata, read from the config.yaml at
// the module root (not the application's config.yaml).
package nforge

import (
	_ "embed"

	"gopkg.in/yaml.v3"
)

// The file is embedded at build time, so the version travels with the
// framework even when the application runs from another directory.
//
//go:embed config.yaml
var frameworkConfig []byte

// Version is the nForge version declared in the module root config.yaml.
var Version = mustReadVersion()

func mustReadVersion() string {
	var cfg struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(frameworkConfig, &cfg); err != nil {
		panic("nforge: invalid embedded config.yaml: " + err.Error())
	}
	if cfg.Version == "" {
		panic("nforge: embedded config.yaml has no version")
	}
	return cfg.Version
}

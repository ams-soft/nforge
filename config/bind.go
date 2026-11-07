package config

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Binding is one application's decoding session over a Document: it records
// which sections and env file keys the registered modules consumed, so Finish
// can reject the rest. A Document may be bound several times (several
// applications in one process); each Binding is independent.
type Binding struct {
	d     *Document
	mu    sync.Mutex
	used  map[string]bool // env file keys
	owned map[string]bool // sections
}

// Bind starts a decoding session.
func (d *Document) Bind() *Binding {
	return &Binding{d: d, used: map[string]bool{}, owned: map[string]bool{}}
}

// Document returns the bound document.
func (b *Binding) Document() *Document { return b.d }

// Own declares that a registered module owns section, so its presence is not
// an error even if the module never decodes it.
func (b *Binding) Own(section string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owned[section] = true
}

// Decode decodes section into dst: base YAML, env file keys of the section,
// overlay, each strict. An absent section leaves dst unchanged, so dst carries
// the defaults the module put in it.
func (b *Binding) Decode(section string, dst any) error {
	if isReserved(section) {
		return fmt.Errorf("config: section %q is reserved", section)
	}
	used := map[string]bool{}
	if err := b.d.decode(section, dst, used); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owned[section] = true
	for k := range used {
		b.used[k] = true
	}
	return nil
}

// Present reports whether section appears in the YAML (base or overlay).
func (b *Binding) Present(section string) bool {
	_, ok := b.d.sections[section]
	return ok
}

// Finish reports the sections that no registered module owns and the env file
// keys that nothing consumed. YAML cannot activate a module: a section of an
// unregistered module is an error, not a silent no-op.
func (b *Binding) Finish() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var errs []string
	for _, name := range b.d.order {
		if !b.owned[name] {
			errs = append(errs, fmt.Sprintf("section %q belongs to no registered module (register the module, or remove the section; application data goes under an x- key)", name))
		}
	}
	// A key that names no field is a typo (HTTP_CORS_ORIGNS) or a stale
	// leftover, and ignoring it would silently keep the default, which for a
	// security setting is the unsafe one.
	var unknown []string
	for key := range b.d.envVars {
		if !b.used[key] && !b.d.reserved[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		errs = append(errs, fmt.Sprintf("env file: unknown keys %s: a key is the YAML path in upper case joined by \"_\" (for example HTTP_SERVER_PORT) of a registered module's section", strings.Join(unknown, ", ")))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("config: %s", strings.Join(errs, "; "))
}

func isReserved(section string) bool {
	return section == SectionEnv || section == SectionApp || section == SectionPrdExceptions
}

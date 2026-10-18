package cache

import (
	"context"
	"strings"
)

// keyspace turns application keys into full backend keys:
//
//	default, single tenant: nforge:<env>:<app>:<key>
//	default, multitenant:   nforge:<env>:<tenant>:<app>:<key>
//	custom,  single tenant: <prefix>:<key>
//	custom,  multitenant:   <prefix>:<tenant>:<key>
//
// The dynamic components (env, app, tenant) are escaped with escapeComponent,
// so two different identities can never produce the same namespace. The
// custom prefix is a trusted literal and the key is appended verbatim.
type keyspace struct {
	head    string // before the tenant
	tail    string // after the tenant
	tenant  func(context.Context) (string, error)
	maxSize int
}

func newKeyspace(cfg Config, o options) (keyspace, error) {
	ks := keyspace{tenant: o.tenant, maxSize: cfg.Limits.MaxKeyBytes}
	if cfg.Prefix != "" {
		ks.head = cfg.Prefix
		if !strings.HasSuffix(ks.head, ":") {
			ks.head += ":"
		}
		return ks, nil
	}
	if !o.hasNamespace {
		return ks, invalid("cache.prefix is empty and WithNamespace was not given")
	}
	ks.head = "nforge:" + escapeComponent(o.env) + ":"
	ks.tail = escapeComponent(o.app) + ":"
	if len(ks.head)+len(ks.tail) >= ks.maxSize {
		return ks, invalid("the default namespace is longer than cache.limits.max_key_bytes")
	}
	return ks, nil
}

// resolve validates key and returns the full backend key. The tenant is
// resolved here, once per logical operation, before any I/O.
func (ks keyspace) resolve(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", newError(ErrInvalidKey, "empty")
	}
	var b strings.Builder
	if ks.tenant != nil {
		id, err := ks.tenant(ctx)
		if err != nil || id == "" {
			return "", newError(ErrTenantRequired, "")
		}
		id = escapeComponent(id)
		b.Grow(len(ks.head) + len(id) + 1 + len(ks.tail) + len(key))
		b.WriteString(ks.head)
		b.WriteString(id)
		b.WriteByte(':')
	} else {
		b.Grow(len(ks.head) + len(ks.tail) + len(key))
		b.WriteString(ks.head)
	}
	b.WriteString(ks.tail)
	b.WriteString(key)
	if b.Len() > ks.maxSize {
		return "", newError(ErrInvalidKey, "too_long")
	}
	return b.String(), nil
}

// escapeComponent percent-encodes '%', ':' and control bytes (an injective
// encoding: '%' itself is escaped). UUIDs and ordinary names are unchanged.
func escapeComponent(s string) string {
	n := 0
	for i := 0; i < len(s); i++ {
		if needsEscape(s[i]) {
			n++
		}
	}
	if n == 0 {
		return s
	}
	const hex = "0123456789ABCDEF"
	b := make([]byte, 0, len(s)+2*n)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if needsEscape(c) {
			b = append(b, '%', hex[c>>4], hex[c&15])
			continue
		}
		b = append(b, c)
	}
	return string(b)
}

func needsEscape(c byte) bool { return c == '%' || c == ':' || c < 0x20 || c == 0x7f }

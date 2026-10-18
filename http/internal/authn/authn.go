// Package authn holds the syntax rules of nhttp bearer authentication:
// credential transport, the WWW-Authenticate challenge and the validation of
// scopes, realms and header names. It never sees claims or keys.
package authn

import (
	"fmt"
	stdhttp "net/http"
	"slices"
	"strings"
)

// BearerToken extracts exactly one Bearer credential. It returns 401 when
// there is no bearer credential, 400 for malformed transport and 413 (mapped
// to 401 invalid_token by the caller) for oversized tokens.
func BearerToken(r *stdhttp.Request, maxBytes int64) (string, int) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", stdhttp.StatusUnauthorized
	}
	if len(values) > 1 {
		return "", stdhttp.StatusBadRequest
	}
	v := values[0]
	if len(v) < 6 || !strings.EqualFold(v[:6], "bearer") {
		if strings.TrimSpace(v) == "" {
			return "", stdhttp.StatusBadRequest
		}
		return "", stdhttp.StatusUnauthorized // another scheme: no bearer credentials
	}
	rest := v[6:]
	if rest == "" || rest[0] != ' ' {
		return "", stdhttp.StatusBadRequest
	}
	token := strings.TrimLeft(rest, " ")
	if token == "" || strings.ContainsAny(token, " \t,\r\n") {
		return "", stdhttp.StatusBadRequest
	}
	if int64(len(token)) > maxBytes {
		return "", stdhttp.StatusRequestEntityTooLarge
	}
	return token, 0
}

// Challenge builds the Bearer WWW-Authenticate value. realm and scopes must
// already be validated (ValidRealm, NormalizeScopes), so no escaping is
// needed.
func Challenge(realm, errCode string, scopes []string) string {
	challenge := `Bearer realm="` + realm + `"`
	if errCode != "" {
		challenge += `, error="` + errCode + `"`
	}
	if len(scopes) > 0 {
		challenge += `, scope="` + strings.Join(scopes, " ") + `"`
	}
	return challenge
}

// NormalizeScopes validates RFC 6749 scope tokens and removes duplicates,
// keeping the order.
func NormalizeScopes(scopes []string) ([]string, error) {
	var out []string
	for _, s := range scopes {
		if !validScope(s) {
			return nil, fmt.Errorf("invalid scope %q", s)
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// validScope follows RFC 6749: 1*( %x21 / %x23-5B / %x5D-7E ).
func validScope(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// ValidRealm accepts printable ASCII without quotes or backslashes, up to
// 128 bytes.
func ValidRealm(realm string) bool {
	if len(realm) > 128 {
		return false
	}
	for i := 0; i < len(realm); i++ {
		if c := realm[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// HasControl reports whether s contains a C0, DEL or C1 control character.
func HasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return true
		}
	}
	return false
}

// ValidHeaderName follows the RFC 9110 token syntax.
func ValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

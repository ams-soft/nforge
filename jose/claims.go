package jose

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ams-soft/nforge/jose/internal/strictjson"
)

// RegisteredClaims are the RFC 7519 registered claims managed by the
// framework. Custom claims travel separately (the private argument of
// IssueJWT and the target of ValidateJWT) and may never reuse these names.
type RegisteredClaims struct {
	Issuer    string       `json:"iss,omitempty"`
	Subject   string       `json:"sub,omitempty"`
	Audience  Audience     `json:"aud,omitempty"`
	ExpiresAt *NumericDate `json:"exp,omitempty"`
	NotBefore *NumericDate `json:"nbf,omitempty"`
	IssuedAt  *NumericDate `json:"iat,omitempty"`
	ID        string       `json:"jti,omitempty"`
}

var registeredNames = map[string]bool{"iss": true, "sub": true, "aud": true, "exp": true, "nbf": true, "iat": true, "jti": true}

func (c RegisteredClaims) clone() RegisteredClaims {
	out := c
	out.Audience = append(Audience(nil), c.Audience...)
	if c.ExpiresAt != nil {
		v := *c.ExpiresAt
		out.ExpiresAt = &v
	}
	if c.NotBefore != nil {
		v := *c.NotBefore
		out.NotBefore = &v
	}
	if c.IssuedAt != nil {
		v := *c.IssuedAt
		out.IssuedAt = &v
	}
	return out
}

// Audience is the "aud" claim. It decodes from a single nonempty string or a
// nonempty array of unique nonempty strings and always encodes as an array.
// Comparisons are exact: no case folding, trimming or URL normalization.
type Audience []string

// MarshalJSON always encodes an array.
func (a Audience) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(a))
}

// UnmarshalJSON accepts a string or an array of strings, rejecting null,
// empty values and duplicates.
func (a *Audience) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return errAudience
		}
		*a = Audience{s}
		return a.check()
	}
	if len(data) == 0 || data[0] != '[' {
		return errAudience
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return errAudience
	}
	*a = list
	return a.check()
}

var errAudience = errors.New("aud must be a nonempty string or array of unique nonempty strings")

func (a Audience) check() error {
	if len(a) == 0 || hasDuplicates(a) {
		return errAudience
	}
	for _, v := range a {
		if v == "" || hasControl(v) {
			return errAudience
		}
	}
	return nil
}

// NumericDate is an integral number of seconds since the Unix epoch. This
// is a deliberately narrower profile of RFC 7519: fractions, strings and
// dates outside the years 0001-9999 are rejected.
type NumericDate int64

// Bounds of a NumericDate: 0001-01-01T00:00:00Z and 9999-12-31T23:59:59Z.
const (
	minNumericDate NumericDate = -62135596800
	maxNumericDate NumericDate = 253402300799
)

// NewNumericDate truncates t to whole seconds. Out-of-range dates are
// rejected when the claims are issued or validated.
func NewNumericDate(t time.Time) *NumericDate {
	n := NumericDate(t.Unix())
	return &n
}

// Time returns the date in UTC.
func (n NumericDate) Time() time.Time { return time.Unix(int64(n), 0).UTC() }

func (n NumericDate) valid() bool { return n >= minNumericDate && n <= maxNumericDate }

// MarshalJSON encodes the integer.
func (n NumericDate) MarshalJSON() ([]byte, error) {
	if !n.valid() {
		return nil, errNumericDate
	}
	return strconv.AppendInt(nil, int64(n), 10), nil
}

var errNumericDate = errors.New("numeric date must be an integer in the years 0001-9999")

// UnmarshalJSON accepts only an integer literal (no fraction, exponent,
// string or null) within range. The value never goes through float64.
func (n *NumericDate) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return errNumericDate
	}
	start := 0
	if data[0] == '-' {
		start = 1
	}
	if start == len(data) {
		return errNumericDate
	}
	for _, c := range data[start:] {
		if c < '0' || c > '9' {
			return errNumericDate
		}
	}
	if len(data)-start > 1 && data[start] == '0' {
		return errNumericDate
	}
	v, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || !NumericDate(v).valid() {
		return errNumericDate
	}
	*n = NumericDate(v)
	return nil
}

// decodeRegistered reads the registered claims from the validated payload
// members, with strict types: null is never accepted for a present claim.
func decodeRegistered(members map[string]json.RawMessage) (RegisteredClaims, error) {
	var c RegisteredClaims
	str := func(name string, dst *string) error {
		raw, ok := members[name]
		if !ok {
			return nil
		}
		if strictjson.IsNull(raw) || json.Unmarshal(raw, dst) != nil {
			return claimErr(name, "must be a string")
		}
		return nil
	}
	date := func(name string, dst **NumericDate) error {
		raw, ok := members[name]
		if !ok {
			return nil
		}
		var d NumericDate
		if err := d.UnmarshalJSON(raw); err != nil {
			return claimErr(name, "must be an integer numeric date")
		}
		*dst = &d
		return nil
	}
	for _, err := range []error{
		str("iss", &c.Issuer), str("sub", &c.Subject), str("jti", &c.ID),
		date("exp", &c.ExpiresAt), date("nbf", &c.NotBefore), date("iat", &c.IssuedAt),
	} {
		if err != nil {
			return c, err
		}
	}
	if raw, ok := members["aud"]; ok {
		if err := c.Audience.UnmarshalJSON(raw); err != nil {
			return c, claimErr("aud", "must be a nonempty string or array of unique nonempty strings")
		}
	}
	return c, nil
}

func claimErr(field, reason string) error {
	return newError(ErrInvalidClaims, "", "", field, reason)
}

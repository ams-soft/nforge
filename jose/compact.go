package jose

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/ams-soft/nforge/jose/internal/strictjson"
)

// Header is the protected header of a verified JWS or decrypted JWE. All
// headers are protected; unprotected headers are never accepted.
type Header struct {
	Algorithm   string
	Encryption  string // Empty for JWS.
	KeyID       string
	Type        string
	ContentType string
}

const (
	jwsSegments = 3
	jweSegments = 5
)

// compact is a structurally checked compact serialization whose protected
// header was parsed under the strict rules. Nothing in it is trusted yet.
type compact struct {
	raw      string
	segments []string
	header   Header
}

// headerRules describes the header shape a profile accepts.
type headerRules struct {
	segments int
	allowCty bool
	needEnc  bool
}

// parseCompact bounds and splits the input and parses the protected header.
// It does not decode the other segments nor run any cryptography.
func parseCompact(input string, rules headerRules, limits LimitsConfig) (*compact, error) {
	if int64(len(input)) > limits.MaxTokenBytes {
		return nil, newError(ErrPayloadTooLarge, "", "", "", "token exceeds limits.max_token_bytes")
	}
	if input == "" {
		return nil, malformed("", "empty token")
	}
	for i := 0; i < len(input); i++ {
		if !isCompactChar(input[i]) {
			return nil, malformed("", "token has characters outside the base64url alphabet")
		}
	}
	if n := strings.Count(input, ".") + 1; n != rules.segments {
		return nil, malformed("", "wrong number of segments")
	}
	segments := strings.Split(input, ".")
	if segments[0] == "" {
		return nil, malformed("header", "empty protected header")
	}
	if int64(len(segments[0])) > int64(base64.RawURLEncoding.EncodedLen(int(limits.MaxHeaderBytes))) {
		return nil, newError(ErrPayloadTooLarge, "", "", "header", "protected header exceeds limits.max_header_bytes")
	}
	rawHeader, err := decodeB64([]byte(segments[0]))
	if err != nil {
		return nil, malformed("header", "invalid base64url")
	}
	members, err := strictjson.Object(rawHeader, limits.MaxJSONDepth)
	if err != nil {
		return nil, malformed("header", "protected header is not a strict json object")
	}

	var h Header
	targets := map[string]*string{"alg": &h.Algorithm, "kid": &h.KeyID, "typ": &h.Type}
	if rules.segments == jweSegments {
		targets["enc"] = &h.Encryption
	}
	if rules.allowCty {
		targets["cty"] = &h.ContentType
	}
	for name, raw := range members {
		dst, ok := targets[name]
		if !ok {
			return nil, malformed("header."+boundedName(name), "header parameter not allowed")
		}
		if strictjson.IsNull(raw) || json.Unmarshal(raw, dst) != nil || *dst == "" {
			return nil, malformed("header."+name, "must be a nonempty string")
		}
	}
	if h.Algorithm == "" {
		return nil, malformed("header.alg", "required")
	}
	if h.KeyID == "" {
		return nil, malformed("header.kid", "required")
	}
	if !identRe.MatchString(h.KeyID) {
		return nil, malformed("header.kid", "invalid key id")
	}
	if h.Type == "" {
		return nil, newError(ErrInvalidType, "", "", "header.typ", "required")
	}
	if rules.needEnc && h.Encryption == "" {
		return nil, malformed("header.enc", "required")
	}
	return &compact{raw: input, segments: segments, header: h}, nil
}

// segment decodes segment i as canonical base64url.
func (c *compact) segment(i int) ([]byte, error) {
	b, err := decodeB64([]byte(c.segments[i]))
	if err != nil {
		return nil, malformed("", "invalid base64url segment")
	}
	return b, nil
}

func isCompactChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// boundedName keeps attacker-chosen header names out of error messages
// unless they are short identifiers.
func boundedName(name string) string {
	if len(name) <= 16 && identRe.MatchString(name) {
		return name
	}
	return "?"
}

func malformed(field, reason string) error {
	return newError(ErrMalformedToken, "", "", field, reason)
}

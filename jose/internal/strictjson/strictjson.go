// Package strictjson parses JOSE protocol JSON (headers, claims, JWKs)
// independently of the application codec, with guarantees encoding/json does
// not give by itself: valid UTF-8, bounded nesting depth, no duplicate member
// names at any level and no trailing data. encoding/json would silently keep
// the last duplicate and replace invalid UTF-8, which enables
// parser-differential attacks.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var (
	errSyntax    = errors.New("invalid json")
	errUTF8      = errors.New("invalid utf-8")
	errDepth     = errors.New("json too deep")
	errDuplicate = errors.New("duplicate member name")
	// ErrNotObject is returned by Object for valid JSON that is not an object.
	ErrNotObject = errors.New("not a json object")
)

// Check validates data as one JSON value under the rules above.
func Check(data []byte, maxDepth int) error {
	if !utf8.Valid(data) {
		return errUTF8
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	type frame struct {
		object    bool
		expectKey bool
		keys      map[string]struct{}
	}
	var stack []frame
	values := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errSyntax
		}
		if len(stack) == 0 {
			if values > 0 {
				return errSyntax // trailing data after the top-level value
			}
			values++
		}
		var top *frame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		if top != nil && top.object && top.expectKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				continue
			}
			key, ok := tok.(string)
			if !ok {
				return errSyntax
			}
			if _, dup := top.keys[key]; dup {
				return errDuplicate
			}
			top.keys[key] = struct{}{}
			top.expectKey = false
			continue
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				if len(stack) >= maxDepth {
					return errDepth
				}
				if top != nil && top.object {
					top.expectKey = true
				}
				stack = append(stack, frame{object: d == '{', expectKey: d == '{', keys: map[string]struct{}{}})
			case ']':
				stack = stack[:len(stack)-1]
			default:
				return errSyntax
			}
			continue
		}
		// scalar value
		if top != nil && top.object {
			top.expectKey = true
		}
	}
	if len(stack) != 0 || values == 0 {
		return errSyntax
	}
	return nil
}

// Object validates data and splits the top-level object into its raw
// members.
func Object(data []byte, maxDepth int) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		if err := Check(data, maxDepth); err != nil {
			return nil, err
		}
		return nil, ErrNotObject
	}
	if err := Check(data, maxDepth); err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, errSyntax
	}
	return members, nil
}

// DecodeNumbers decodes already validated JSON keeping numbers as json.Number.
func DecodeNumbers(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// IsNull reports whether raw is the JSON null literal.
func IsNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

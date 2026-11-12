package codec

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
)

// DefaultMaxElements is the number of values (JSON tokens, XML elements and
// attributes) one document may hold when none is configured. The payload limit
// bounds bytes on the wire, not what decoding them costs: a 4.7 MB array of
// numbers decodes into ~300 MB of interface values. This bounds the work.
const DefaultMaxElements = 250_000

// MaxElementsCeiling is the largest accepted codec.max_elements.
const MaxElementsCeiling = 100_000_000

var (
	// ErrDuplicateField is returned for an object that names the same member
	// twice (or twice differing only in case, which encoding/json treats as
	// one). Parsers disagree about which duplicate wins, so a gateway that
	// reads the first one and nForge that reads the last would see different
	// documents: it is refused outright.
	ErrDuplicateField = errors.New("codec: duplicate field")
	// ErrTooComplex is returned for a document with more elements than the
	// configured maximum (codec.max_elements).
	ErrTooComplex = errors.New("codec: document has too many elements")
)

// DuplicateFieldError reports an object with a repeated member.
type DuplicateFieldError struct{ Format string }

func (e *DuplicateFieldError) Error() string {
	return "codec: " + e.Format + ": duplicate field in an object"
}
func (e *DuplicateFieldError) Is(target error) bool { return target == ErrDuplicateField }

// ComplexityError reports a document over the element limit.
type ComplexityError struct {
	Format string
	Limit  int
}

func (e *ComplexityError) Error() string {
	return fmt.Sprintf("codec: %s: document has more than %d elements", e.Format, e.Limit)
}
func (e *ComplexityError) Is(target error) bool { return target == ErrTooComplex }

// frame is one open JSON object or array of the scan.
type frame struct {
	obj   bool
	child int                 // direct children read so far: names and values alternate in an object
	small []string            // folded member names, linear for small objects
	big   map[string]struct{} // and a set once an object grows
}

// seen records a folded member name and reports whether it was already present.
func (f *frame) seen(name string) bool {
	if f.big != nil {
		if _, dup := f.big[name]; dup {
			return true
		}
		f.big[name] = struct{}{}
		return false
	}
	for _, prev := range f.small {
		if prev == name {
			return true
		}
	}
	f.small = append(f.small, name)
	if len(f.small) > 16 {
		f.big = make(map[string]struct{}, len(f.small)*2)
		for _, n := range f.small {
			f.big[n] = struct{}{}
		}
		f.small = nil
	}
	return false
}

// scanJSON walks the token stream once, before decoding, and rejects what
// must never reach a decoder: duplicate member names (exact, or equal after
// case folding, which encoding/json treats as one field) and documents over
// maxElements tokens. Syntax errors are not its business: it returns nil for
// them and the decoder produces the error with its usual form and offset.
func scanJSON(data []byte, maxElements int) error {
	dec := jsontext.NewDecoder(bytes.NewReader(data), jsontext.AllowInvalidUTF8(true))
	var stack []frame
	n := 0
	for {
		tok, err := dec.ReadToken()
		if err != nil {
			if errors.Is(err, jsontext.ErrDuplicateName) {
				return &DuplicateFieldError{Format: "json"}
			}
			return nil // io.EOF, or a syntax error the decoder will report
		}
		n++
		if maxElements > 0 && n > maxElements {
			return &ComplexityError{Format: "json", Limit: maxElements}
		}
		kind := tok.Kind()
		if kind == '}' || kind == ']' { // end tokens are not children
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.obj {
				idx := top.child
				top.child++
				if idx%2 == 0 && top.seen(strings.ToLower(tok.String())) {
					return &DuplicateFieldError{Format: "json"}
				}
			}
		}
		switch kind {
		case '{':
			stack = append(stack, frame{obj: true})
		case '[':
			stack = append(stack, frame{})
		}
	}
}

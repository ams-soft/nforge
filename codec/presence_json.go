package codec

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"reflect"
	"strconv"
	"sync"
)

// JSON presence check without the generic tree: the payload is walked token
// by token alongside the target type, descending only into values whose
// type can produce a presence error and skipping the rest. It reports
// exactly the error checkPresence would report on the decoded tree: the
// tree walk visits object keys in sorted order and stops at the first error,
// so the walker keeps, per object, the candidate of the smallest key.
//
// Anything outside that model falls back to the tree (errFallback): invalid
// syntax (so syntax errors keep their usual form), duplicate names (the tree
// keeps only the last value), one field matched by two spellings, and names
// with invalid UTF-8 (the tree replaces them with U+FFFD).
//
// The walk allocates nothing per value: paths are a stack of elements
// reused across calls and only become strings when an error is reported.

var errFallback = errors.New("codec: json presence: use the tree")

// walkerOptions accept what encoding/json accepts; duplicate names stay
// rejected so they reach the fallback. Built once: options allocate.
var walkerOptions = []jsontext.Options{jsontext.AllowInvalidUTF8(true)}

var walkerPool = sync.Pool{New: func() any {
	w := &jsonWalker{}
	w.dec = jsontext.NewDecoder(&w.rd, walkerOptions...) // jsontext rejects a nil reader
	return w
}}

// maxPooledPayload bounds the payloads whose walker returns to the pool: the
// decoder's buffer grows with the payload and would stay retained.
const maxPooledPayload = 64 << 10

type jsonWalker struct {
	dec    *jsontext.Decoder
	rd     bytes.Reader
	strict bool
	name   []byte     // last object name read, unquoted
	path   []pathElem // path to the current value
	keys   []byte     // map keys referenced by path
	err    error      // first read error: the walk is abandoned
}

// pathElem is a struct field name, a map key (keys[start:end]) or an array
// index.
type pathElem struct {
	field      string
	start, end int
	index      int
	kind       uint8
}

const (
	elemField uint8 = iota
	elemKey
	elemIndex
)

// checkPresenceJSON is checkPresence(fmtJSON, t, tree, "", strict) for the
// tree that data decodes into, without building the tree.
func checkPresenceJSON(data []byte, t reflect.Type, strict bool) error {
	w, _ := walkerPool.Get().(*jsonWalker)
	defer func() {
		w.rd.Reset(nil) // drop the reference to data
		w.path, w.keys, w.err = w.path[:0], w.keys[:0], nil
		if len(data) <= maxPooledPayload {
			walkerPool.Put(w)
		}
	}()
	w.rd.Reset(data)
	w.dec.Reset(&w.rd, walkerOptions...)
	w.strict = strict

	perr := w.value(t)
	if w.err == nil {
		if _, err := w.dec.ReadToken(); !errors.Is(err, io.EOF) {
			w.err = errFallback // trailing data or a syntax error
		}
	}
	if w.err != nil {
		return checkPresenceTree(data, t, strict)
	}
	return perr
}

// checkPresenceTree is the reference implementation: decode into a generic
// tree, then walk it. It reports syntax errors exactly as Decode does.
func checkPresenceTree(data []byte, t reflect.Type, strict bool) error {
	var tree any
	if err := jsonDecodeAll(bytes.NewReader(data), &tree); err != nil {
		return err
	}
	return checkPresence(fmtJSON, t, tree, "", strict)
}

// pathTo renders the current path plus leaf with the tree walk's rules
// (joinPath for names, "[i]" for array elements).
func (w *jsonWalker) pathTo(leaf string) string {
	p := ""
	for _, e := range w.path {
		switch e.kind {
		case elemField:
			p = joinPath(p, e.field)
		case elemKey:
			p = joinPath(p, string(w.keys[e.start:e.end]))
		case elemIndex:
			p += "[" + strconv.Itoa(e.index) + "]"
		}
	}
	return joinPath(p, leaf)
}

func (w *jsonWalker) pop() { w.path = w.path[:len(w.path)-1] }

// value consumes one value of type t and returns its presence error, if any.
// Once w.err is set the walk is abandoned and every result is ignored:
// checkPresenceJSON falls back to the tree.
func (w *jsonWalker) value(t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isOpaque(t) || !needsWalk(t, w.strict) {
		w.skip()
		return nil
	}
	kind := w.dec.PeekKind()
	switch t.Kind() {
	case reflect.Struct:
		if kind == '{' {
			return w.object(t)
		}
	case reflect.Slice, reflect.Array:
		if kind == '[' && t.Elem().Kind() != reflect.Uint8 {
			return w.array(t.Elem())
		}
	case reflect.Map:
		if kind == '{' {
			return w.mapObject(t.Elem())
		}
	}
	// null, or a type the decoder will reject with more context
	w.skip()
	return nil
}

// candidate tracks the error of the smallest key in one object.
type candidate struct {
	key string
	err error
}

func (c *candidate) offer(key []byte, err error) {
	if c.err == nil || string(key) < c.key {
		c.key, c.err = string(key), err
	}
}

func (w *jsonWalker) object(t reflect.Type) error {
	si := getStruct(t)
	fields := si.fields[fmtJSON]
	present := newBitset(len(fields))
	seen := newBitset(len(fields))
	var best candidate
	var keyBuf [64]byte

	w.read() // '{'
	for w.err == nil && w.dec.PeekKind() != '}' {
		if !w.readName() {
			break
		}
		key := append(keyBuf[:0], w.name...) // w.name is reused by nested values
		i := lookupFieldIndex(si, fields, key)
		if i < 0 {
			if w.strict {
				best.offer(key, &UnknownFieldError{Format: fmtJSON.String(), Field: w.pathTo(string(key))})
			}
			w.skip()
			continue
		}
		if seen.has(i) {
			w.err = errFallback // the tree would merge both spellings
			break
		}
		seen.set(i)
		if w.dec.PeekKind() != 'n' {
			present.set(i)
		}
		w.path = append(w.path, pathElem{kind: elemField, field: fields[i].name})
		err := w.value(fields[i].typ)
		w.pop()
		if err != nil {
			best.offer(key, err)
		}
	}
	w.read() // '}'
	if best.err != nil {
		return best.err
	}
	for i, fl := range fields {
		if fl.required && !present.has(i) {
			return &MissingFieldError{Format: fmtJSON.String(), Field: w.pathTo(fl.name)}
		}
	}
	return nil
}

func (w *jsonWalker) mapObject(elem reflect.Type) error {
	var best candidate
	w.read() // '{'
	for w.err == nil && w.dec.PeekKind() != '}' {
		if !w.readName() {
			break
		}
		start := len(w.keys)
		w.keys = append(w.keys, w.name...)
		end := len(w.keys)
		w.path = append(w.path, pathElem{kind: elemKey, start: start, end: end})
		err := w.value(elem)
		w.pop()
		if err != nil {
			best.offer(w.keys[start:end], err)
		}
		w.keys = w.keys[:start]
	}
	w.read() // '}'
	return best.err
}

func (w *jsonWalker) array(elem reflect.Type) error {
	var first error
	w.read() // '['
	for i := 0; w.err == nil && w.dec.PeekKind() != ']'; i++ {
		if first != nil {
			w.skip() // the tree walk stops at the first element error
			continue
		}
		w.path = append(w.path, pathElem{kind: elemIndex, index: i})
		first = w.value(elem)
		w.pop()
	}
	w.read() // ']'
	return first
}

func (w *jsonWalker) read() {
	if w.err != nil {
		return
	}
	if _, err := w.dec.ReadToken(); err != nil {
		w.err = err
	}
}

func (w *jsonWalker) skip() {
	if w.err != nil {
		return
	}
	if err := w.dec.SkipValue(); err != nil {
		w.err = err
	}
}

// readName reads an object name into w.name, unquoted.
func (w *jsonWalker) readName() bool {
	v, err := w.dec.ReadValue()
	if err != nil {
		w.err = err
		return false
	}
	w.name, err = jsontext.AppendUnquote(w.name[:0], v)
	if err != nil {
		w.err = errFallback // invalid UTF-8: the tree replaces it
		return false
	}
	return true
}

// lookupFieldIndex is lookupField returning the field's index: exact match
// first, then case-insensitive, like encoding/json.
func lookupFieldIndex(si *structInfo, fields []*field, key []byte) int {
	if fl, ok := si.byName[fmtJSON][string(key)]; ok {
		for i, f := range fields {
			if f == fl {
				return i
			}
		}
	}
	for i, fl := range fields {
		if bytes.EqualFold([]byte(fl.name), key) {
			return i
		}
	}
	return -1
}

// needsWalk reports whether a value of type t can hold a presence error:
// a required field (hasRequired) or, in strict mode, any struct.
func needsWalk(t reflect.Type, strict bool) bool {
	if strict {
		return hasStruct(t)
	}
	return hasRequired(t)
}

var hasStructCache sync.Map // reflect.Type -> bool

// hasStruct reports whether t is or reaches a non-opaque struct.
func hasStruct(t reflect.Type) bool {
	if v, ok := hasStructCache.Load(t); ok {
		b, _ := v.(bool)
		return b
	}
	r := hasStructRec(t, map[reflect.Type]bool{})
	hasStructCache.Store(t, r)
	return r
}

func hasStructRec(t reflect.Type, seen map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] || isOpaque(t) {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Struct:
		return true
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() != reflect.Uint8 && hasStructRec(t.Elem(), seen)
	case reflect.Map:
		return hasStructRec(t.Elem(), seen)
	}
	return false
}

// bitset is a per-object set of field indexes, on the stack up to 64 fields.
type bitset struct {
	small uint64
	large []bool
}

func newBitset(n int) bitset {
	if n > 64 {
		return bitset{large: make([]bool, n)}
	}
	return bitset{}
}

func (b *bitset) set(i int) {
	if b.large != nil {
		b.large[i] = true
		return
	}
	b.small |= 1 << uint(i)
}

func (b *bitset) has(i int) bool {
	if b.large != nil {
		return b.large[i]
	}
	return b.small&(1<<uint(i)) != 0
}

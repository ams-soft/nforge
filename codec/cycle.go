package codec

import (
	"reflect"
	"sync"
)

// maxValueDepth bounds how deep the value walks below follow a value. It only
// matters for pathological acyclic data: cycles are caught by identity long
// before.
const maxValueDepth = 10_000

// errCycle reports a value that contains itself. encoding/json detects this on
// its own; the CBOR library and the XML encoder recurse until the Go stack
// overflows, which is fatal for the whole process (not a recoverable panic).
func errCycle(format string) error {
	return &EncodeError{Format: format, Msg: "value contains a reference cycle or is nested too deeply"}
}

var mayCycleCache sync.Map // reflect.Type -> bool

// mayCycle reports whether a value of type t can contain a cycle: its type
// graph loops back through a reference (a recursive type) or reaches an
// interface, whose dynamic value is unknown. DTO-style trees cannot, so they
// skip the walk entirely.
func mayCycle(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if v, ok := mayCycleCache.Load(t); ok {
		b, _ := v.(bool)
		return b
	}
	r := reachesInterface(t) || typeLoops(t, map[reflect.Type]bool{}, map[reflect.Type]bool{})
	mayCycleCache.Store(t, r)
	return r
}

// typeLoops reports whether t reaches itself through pointers, slices, arrays,
// maps or struct fields (a depth-first search with an "on the current path" set).
func typeLoops(t reflect.Type, onPath, done map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if done[t] || isOpaque(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
	default:
		return false
	}
	if onPath[t] {
		return true
	}
	onPath[t] = true
	defer delete(onPath, t)
	loops := false
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		loops = typeLoops(t.Elem(), onPath, done)
	case reflect.Struct:
		for i := range t.NumField() {
			if sf := t.Field(i); sf.IsExported() || sf.Anonymous {
				if typeLoops(sf.Type, onPath, done) {
					loops = true
					break
				}
			}
		}
	}
	if !loops {
		done[t] = true
	}
	return loops
}

// cycleKey identifies a pointer, map or slice by address (and type, since a
// struct and its first field share an address).
type cycleKey struct {
	ptr uintptr
	typ reflect.Type
	n   int
}

// checkAcyclic returns an EncodeError when v contains a reference cycle. It
// follows the fields the format would encode (a back-reference tagged
// `json:"-"` is not a cycle for JSON) and only ancestors count: the same
// pointer reached twice by different routes is fine.
func checkAcyclic(f format, v reflect.Value) error {
	if !v.IsValid() || !mayCycle(v.Type()) {
		return nil
	}
	w := cycleWalker{f: f, active: map[cycleKey]bool{}}
	if !w.walk(v, 0) {
		return errCycle(f.String())
	}
	return nil
}

type cycleWalker struct {
	f      format
	active map[cycleKey]bool
}

// walk reports false when a cycle (or excessive depth) is found.
func (w *cycleWalker) walk(v reflect.Value, depth int) bool {
	if depth > maxValueDepth {
		return false
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return true
		}
		if v.Kind() == reflect.Pointer {
			key := cycleKey{ptr: v.Pointer(), typ: v.Type()}
			if w.active[key] {
				return false
			}
			w.active[key] = true
			defer delete(w.active, key)
		}
		return w.walk(v.Elem(), depth+1)
	case reflect.Map:
		if v.IsNil() || v.Len() == 0 {
			return true
		}
		key := cycleKey{ptr: v.Pointer(), typ: v.Type()}
		if w.active[key] {
			return false
		}
		w.active[key] = true
		defer delete(w.active, key)
		iter := v.MapRange()
		for iter.Next() {
			if !w.walk(iter.Value(), depth+1) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 || v.Len() == 0 {
			return true
		}
		if v.Kind() == reflect.Slice {
			key := cycleKey{ptr: v.Pointer(), typ: v.Type(), n: v.Len()}
			if w.active[key] {
				return false
			}
			w.active[key] = true
			defer delete(w.active, key)
		}
		for i := range v.Len() {
			if !w.walk(v.Index(i), depth+1) {
				return false
			}
		}
	case reflect.Struct:
		if isOpaque(v.Type()) {
			return true
		}
		for _, fl := range getStruct(v.Type()).fields[w.f] {
			fv, ok := fieldByIndex(v, fl.index)
			if ok && !w.walk(fv, depth+1) {
				return false
			}
		}
	}
	return true
}

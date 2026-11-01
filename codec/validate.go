package codec

import (
	"fmt"
	"reflect"
	"sync"
)

var validatorType = reflect.TypeFor[Validator]()

var hasValidatorCache sync.Map // reflect.Type -> bool

// hasValidator reports whether a value of type t can contain something that
// implements Validator, so decoding into a type with no validator anywhere
// skips the walk. Interfaces are not followed: a value decoded into an
// interface is a generic map, slice or scalar.
func hasValidator(t reflect.Type) bool {
	if v, ok := hasValidatorCache.Load(t); ok {
		b, _ := v.(bool)
		return b
	}
	r := hasValidatorRec(t, map[reflect.Type]bool{})
	hasValidatorCache.Store(t, r)
	return r
}

func hasValidatorRec(t reflect.Type, seen map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	if implements(t, validatorType) {
		return true
	}
	if isOpaque(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return hasValidatorRec(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if sf := t.Field(i); (sf.IsExported() || sf.Anonymous) && hasValidatorRec(sf.Type, seen) {
				return true
			}
		}
	}
	return false
}

// validateTree calls Validate on every value reachable from root that
// implements Validator, children before their parent, and returns the first
// failure as a ValidationError whose message names where it happened
// ("address: invalid zip"). Only the fields the format decodes are visited.
// root may be a pointer (the decode target) or a plain value.
func validateTree(f format, root any) error {
	rv := reflect.ValueOf(root)
	if !rv.IsValid() || !hasValidator(rv.Type()) {
		return nil
	}
	if rv.Kind() != reflect.Pointer {
		cp := reflect.New(rv.Type()).Elem()
		cp.Set(rv)
		rv = cp
	}
	w := validateWalker{f: f}
	return w.walk(rv, nil, 0)
}

type validateWalker struct{ f format }

func (w validateWalker) walk(v reflect.Value, path *xpath, depth int) error {
	if depth > maxValueDepth {
		return &ValidationError{Err: fmt.Errorf("%s: value nested too deeply", path)}
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Interface || !hasValidator(v.Type()) {
		return nil
	}
	if !isOpaque(v.Type()) { // custom (un)marshalers own their content
		if err := w.children(v, path, depth); err != nil {
			return err
		}
	}
	return w.self(v, path)
}

func (w validateWalker) children(v reflect.Value, path *xpath, depth int) error {
	switch v.Kind() {
	case reflect.Struct:
		for _, fl := range getStruct(v.Type()).fields[w.f] {
			if fv, ok := fieldByIndex(v, fl.index); ok {
				if err := w.walk(fv, path.field(fl.name), depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := range v.Len() {
			if err := w.walk(v.Index(i), path.at(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			// map values are not addressable: validate a copy
			cp := reflect.New(v.Type().Elem()).Elem()
			cp.Set(iter.Value())
			key, _ := keyText(iter.Key())
			if err := w.walk(cp, path.field(key), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// self calls Validate on v itself (pointer receiver if v is addressable).
func (w validateWalker) self(v reflect.Value, path *xpath) error {
	var val Validator
	switch {
	case v.CanAddr() && v.Addr().Type().Implements(validatorType):
		val, _ = v.Addr().Interface().(Validator)
	case v.Type().Implements(validatorType):
		val, _ = v.Interface().(Validator)
	}
	if val == nil {
		return nil
	}
	if err := val.Validate(); err != nil {
		if path == nil {
			return &ValidationError{Err: err}
		}
		return &ValidationError{Err: fmt.Errorf("%s: %w", path, err)}
	}
	return nil
}

// formatOf maps a codec to the field set used to walk a decoded value: CBOR
// and XML have their own; JSON and custom codecs use the JSON one.
func formatOf(c Codec) format {
	switch normalize(c.ContentType()) {
	case CBOR:
		return fmtCBOR
	case XML:
		return fmtXML
	}
	return fmtJSON
}

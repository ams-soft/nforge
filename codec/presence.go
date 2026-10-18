package codec

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Presence check for JSON and CBOR: the standard libs do not report which
// keys were in the payload, so, when there is strict mode or required
// fields, the payload is also decoded into a generic tree and walked
// alongside the target type. This yields errors with the full field path
// (e.g. "user.address.zip").

var requiredCache sync.Map // reflect.Type -> bool

// hasRequired reports whether t (or any reachable type) has a `codec:"required"` field.
func hasRequired(t reflect.Type) bool {
	if v, ok := requiredCache.Load(t); ok {
		return v.(bool)
	}
	r := hasRequiredRec(t, map[reflect.Type]bool{})
	requiredCache.Store(t, r)
	return r
}

func hasRequiredRec(t reflect.Type, seen map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] || isOpaque(t) {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return hasRequiredRec(t.Elem(), seen)
	case reflect.Struct:
		si := getStruct(t)
		for f := range numFormats {
			for _, fl := range si.fields[f] {
				if fl.required || hasRequiredRec(fl.typ, seen) {
					return true
				}
			}
		}
	}
	return false
}

func checkPresence(f format, t reflect.Type, tree any, path string, strict bool) error {
	if tree == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isOpaque(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := asMap(tree)
		if !ok {
			return nil // incompatible type: the lib reports it with more context
		}
		si := getStruct(t)
		present := make(map[*field]bool, len(m))
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fl := lookupField(si, f, k)
			if fl == nil {
				if strict {
					return &UnknownFieldError{Format: f.String(), Field: joinPath(path, k)}
				}
				continue
			}
			if m[k] != nil {
				present[fl] = true
			}
			if err := checkPresence(f, fl.typ, m[k], joinPath(path, fl.name), strict); err != nil {
				return err
			}
		}
		for _, fl := range si.fields[f] {
			if fl.required && !present[fl] {
				return &MissingFieldError{Format: f.String(), Field: joinPath(path, fl.name)}
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		arr, ok := tree.([]any)
		if !ok {
			return nil
		}
		for i, item := range arr {
			if err := checkPresence(f, t.Elem(), item, path+"["+strconv.Itoa(i)+"]", strict); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := asMap(tree)
		if !ok {
			return nil
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if err := checkPresence(f, t.Elem(), m[k], joinPath(path, k), strict); err != nil {
				return err
			}
		}
	}
	return nil
}

// lookupField matches the key like encoding/json and fxamacker/cbor do: exact
// match first, then case-insensitive.
func lookupField(si *structInfo, f format, key string) *field {
	if fl, ok := si.byName[f][key]; ok {
		return fl
	}
	for _, fl := range si.fields[f] {
		if strings.EqualFold(fl.name, key) {
			return fl
		}
	}
	return nil
}

func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any: // cbor
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

package codec

import (
	"reflect"
)

// FieldInfo describes a struct field as the codec sees it in a format:
// effective name (tag or Go name), required and omission flags. It is the
// basis for code that needs to walk types outside the codec (e.g. OpenAPI generation).
type FieldInfo struct {
	Name      string // name in the payload
	GoName    string
	Type      reflect.Type
	Index     []int // path for reflect.Value.FieldByIndex
	Required  bool  // `codec:"required"`
	OmitEmpty bool  // omitempty or omitzero
	Attr      bool  // xml only: ",attr"
}

// Fields returns the fields of t in the given format ("json", "cbor", "xml"
// or the content type), with the same naming, embedded struct promotion and
// conflict rules used in encode/decode. t may be a pointer; non-struct types
// return nil.
func Fields(format string, t reflect.Type) []FieldInfo {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || isOpaque(t) {
		return nil
	}
	f := fmtJSON
	switch normalize(format) {
	case CBOR:
		f = fmtCBOR
	case XML:
		f = fmtXML
	}
	fields := getStruct(t).fields[f]
	out := make([]FieldInfo, 0, len(fields))
	for _, fl := range fields {
		out = append(out, FieldInfo{
			Name:      fl.name,
			GoName:    fl.goName,
			Type:      fl.typ,
			Index:     fl.index,
			Required:  fl.required,
			OmitEmpty: fl.omitEmpty || fl.omitZero,
			Attr:      fl.attr,
		})
	}
	return out
}

// IsOpaque reports whether t has its own (de)serialization (TextMarshaler,
// json.Marshaler...), i.e. the codec does not look at its fields.
func IsOpaque(t reflect.Type) bool { return isOpaque(t) }

// CheckRequired checks, on an already built value (e.g. a handler's
// response), whether every `codec:"required"` field would be in the payload:
// it cannot be nil (pointer, slice, map, interface) nor empty when it has
// omitempty/omitzero. It is the encode-side mirror of the presence check done
// on decode.
//
// It walks the value only when that can find something: the type reaches a
// `codec:"required"` field (hasRequired) or an interface, whose dynamic value
// is unknown. A value that contains a reference cycle is an EncodeError
// instead of an endless recursion.
func CheckRequired(v any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	if t := rv.Type(); !hasRequired(t) && !reachesInterface(t) {
		return nil
	}
	if err := checkAcyclic(fmtJSON, rv); err != nil {
		return err
	}
	return checkRequired(rv, nil)
}

func checkRequired(v reflect.Value, path *xpath) error {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() || isOpaque(v.Type()) {
		return nil
	}
	switch v.Kind() {
	case reflect.Struct:
		for _, fl := range getStruct(v.Type()).fields[fmtJSON] {
			fv, ok := fieldByIndex(v, fl.index)
			if fl.required && (!ok || absent(fl, fv)) {
				return &MissingFieldError{Format: "response", Field: path.leaf(fl.name)}
			}
			if ok {
				if err := checkRequired(fv, path.field(fl.name)); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := range v.Len() {
			if err := checkRequired(v.Index(i), path.at(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			k, _ := keyText(iter.Key())
			if err := checkRequired(iter.Value(), path.field(k)); err != nil {
				return err
			}
		}
	}
	return nil
}

// absent reports whether the field would be left out of the payload (or sent as null).
func absent(fl *field, v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map:
		if v.IsNil() {
			return true
		}
	}
	return omitField(fl, v)
}

// Validate runs the Registry's post-decode validation (Validator and
// Config.Validate) on v, which must be a pointer when Validate has a
// pointer receiver. Useful for values that did not come from a decode.
func (r *Registry) Validate(v any) error {
	return r.runValidation(v)
}

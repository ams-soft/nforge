// Package query decodes typed query strings (nhttp TGet). Each exported
// struct field becomes a parameter:
//
//	type ListUsers struct {
//		Name   string    `query:"name"`                  // optional
//		Page   int       `query:"page" default:"1"`      // default value
//		Limit  int       `query:"limit" codec:"required"` // required
//		Active *bool     `query:"active"`                // nil if absent
//		Tags   []string  `query:"tag"`                   // ?tag=a&tag=b or ?tag=a,b
//		Since  time.Time `query:"since"`                 // RFC 3339
//	}
//
// Name: the `query` tag, else the `json` tag, else the field name; "-" skips
// the field. Anonymous embedded structs have their fields promoted. An empty
// parameter (?name=) counts as absent.
package query

import (
	"encoding"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ams-soft/nforge/codec"
)

// Field is one query parameter of a struct type.
type Field struct {
	Name       string
	GoName     string
	Index      []int
	Type       reflect.Type
	Required   bool
	Default    string
	HasDefault bool
}

var (
	cache sync.Map // reflect.Type -> []Field

	// DurationType is decoded from text such as "1m30s".
	DurationType        = reflect.TypeFor[time.Duration]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// Fields returns the parameters of the struct type t, cached per type.
func Fields(t reflect.Type) []Field {
	if v, ok := cache.Load(t); ok {
		return v.([]Field)
	}
	var fields []Field
	collect(t, nil, &fields)
	v, _ := cache.LoadOrStore(t, fields)
	return v.([]Field)
}

func collect(t reflect.Type, index []int, out *[]Field) {
	for i := range t.NumField() {
		sf := t.Field(i)
		idx := append(append([]int(nil), index...), i)
		name := tagName(sf.Tag.Get("query"))
		if name == "" {
			name = tagName(sf.Tag.Get("json"))
		}
		if name == "-" {
			continue
		}
		if sf.Anonymous && name == "" && sf.Type.Kind() == reflect.Struct {
			collect(sf.Type, idx, out)
			continue
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		def, hasDef := sf.Tag.Lookup("default")
		*out = append(*out, Field{
			Name:       name,
			GoName:     sf.Name,
			Index:      idx,
			Type:       sf.Type,
			Required:   hasTagOpt(sf.Tag.Get("codec"), "required"),
			Default:    def,
			HasDefault: hasDef,
		})
	}
}

func tagName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return name
}

func hasTagOpt(tag, opt string) bool {
	for o := range strings.SplitSeq(tag, ",") {
		if o == opt {
			return true
		}
	}
	return false
}

// Check validates, at route registration, that t is a struct holding
// only types the query string can represent, and that the defaults are valid.
func Check(t reflect.Type) error {
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("nhttp: query type must be a struct, got %s", t)
	}
	for _, f := range Fields(t) {
		if !queryTypeSupported(f.Type) {
			return fmt.Errorf("nhttp: %s.%s: type %s is not supported in the query string", t, f.GoName, f.Type)
		}
		if f.HasDefault {
			if err := Set(reflect.New(f.Type).Elem(), []string{f.Default}, f.Name); err != nil {
				return fmt.Errorf("nhttp: %s.%s: invalid default: %w", t, f.GoName, err)
			}
		}
	}
	return nil
}

func queryTypeSupported(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isTextUnmarshaler(t) {
		return true
	}
	if t.Kind() == reflect.Slice {
		t = t.Elem()
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		return isTextUnmarshaler(t) || scalarKind(t.Kind())
	}
	return scalarKind(t.Kind())
}

func scalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func isTextUnmarshaler(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(textUnmarshalerType)
}

// Decode fills a new *t with the parameters from values. In strict
// mode, parameters that do not exist in the struct are rejected.
func Decode(values url.Values, t reflect.Type, strict bool) (reflect.Value, error) {
	fields := Fields(t)
	if strict {
		for key := range values {
			known := false
			for _, f := range fields {
				if f.Name == key {
					known = true
					break
				}
			}
			if !known {
				return reflect.Value{}, &codec.UnknownFieldError{Format: "query", Field: key}
			}
		}
	}
	ptr := reflect.New(t)
	for _, f := range fields {
		vals := nonEmpty(values[f.Name])
		if len(vals) == 0 {
			switch {
			case f.HasDefault:
				vals = []string{f.Default}
			case f.Required:
				return reflect.Value{}, &codec.MissingFieldError{Format: "query", Field: f.Name}
			default:
				continue
			}
		}
		if err := Set(ptr.Elem().FieldByIndex(f.Index), vals, f.Name); err != nil {
			return reflect.Value{}, err
		}
	}
	return ptr, nil
}

func nonEmpty(vals []string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Set parses vals into v, which may be a pointer, a slice (repeated or
// comma-separated values) or a scalar; errors are codec.TypeError for name.
func Set(v reflect.Value, vals []string, name string) error {
	if v.Kind() == reflect.Pointer {
		elem := reflect.New(v.Type().Elem())
		if err := Set(elem.Elem(), vals, name); err != nil {
			return err
		}
		v.Set(elem)
		return nil
	}
	if v.Kind() == reflect.Slice && !isTextUnmarshaler(v.Type()) {
		var items []string
		for _, s := range vals {
			for item := range strings.SplitSeq(s, ",") {
				if item = strings.TrimSpace(item); item != "" {
					items = append(items, item)
				}
			}
		}
		slice := reflect.MakeSlice(v.Type(), len(items), len(items))
		for i, item := range items {
			if err := Set(slice.Index(i), []string{item}, name); err != nil {
				return err
			}
		}
		v.Set(slice)
		return nil
	}
	return setScalar(v, vals[0], name)
}

func setScalar(v reflect.Value, s, name string) error {
	typeErr := func() error {
		return &codec.TypeError{Format: "query", Field: name, Expected: v.Type().String(), Got: strconv.Quote(s)}
	}
	if u, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
		if err := u.UnmarshalText([]byte(s)); err != nil {
			return typeErr()
		}
		return nil
	}
	if v.Type() == DurationType {
		d, err := time.ParseDuration(s)
		if err != nil {
			return typeErr()
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return typeErr()
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		if err != nil {
			return typeErr()
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		if err != nil {
			return typeErr()
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return typeErr()
		}
		v.SetFloat(n)
	default:
		return typeErr()
	}
	return nil
}

// DefaultValue converts the field's default tag to the schema type, for the
// documentation; it falls back to the raw text.
func (f Field) DefaultValue() any {
	v := reflect.New(f.Type).Elem()
	if err := Set(v, []string{f.Default}, f.Name); err != nil {
		return f.Default
	}
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Type() == DurationType {
		return f.Default
	}
	switch v.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return v.Interface()
	}
	return f.Default
}

package codec

import (
	"encoding"
	"reflect"
	"strings"
	"time"
)

var (
	wireTimeType     = reflect.TypeFor[time.Time]()
	wireDurationType = reflect.TypeFor[time.Duration]()
	wireTextType     = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// WireType describes, in the vocabulary of the payload (object, array,
// string, integer, number, boolean, ...), what a value of type t looks like on
// the wire. Error messages that reach clients use it instead of the Go type
// name, which would disclose package and type names ("nhttp.Address").
func WireType(t reflect.Type) string {
	if t == nil {
		return "value"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == wireTimeType:
		return "string (RFC 3339 date-time)"
	case t == wireDurationType:
		return "duration"
	case t.Implements(wireTextType) || reflect.PointerTo(t).Implements(wireTextType):
		return "string"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string (base64)"
		}
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Interface:
		return "any value"
	}
	return "value"
}

// wireFromGoName is WireType for a Go type that is only known by name (the
// CBOR library reports it as a string). A named type that is not a predeclared
// one is assumed to be a struct, the common case.
func wireFromGoName(name string) string {
	name = strings.TrimLeft(name, "*")
	switch {
	case name == "":
		return "value"
	case name == "[]uint8" || name == "[]byte":
		return "string (base64)"
	case strings.HasPrefix(name, "[]") || strings.HasPrefix(name, "["):
		return "array"
	case strings.HasPrefix(name, "map[") || strings.HasPrefix(name, "struct"):
		return "object"
	case name == "time.Time":
		return "string (RFC 3339 date-time)"
	case name == "time.Duration":
		return "duration"
	case name == "bool":
		return "boolean"
	case name == "string":
		return "string"
	case strings.HasPrefix(name, "int") || strings.HasPrefix(name, "uint"):
		return "integer"
	case strings.HasPrefix(name, "float"):
		return "number"
	case strings.HasPrefix(name, "interface"):
		return "any value"
	case strings.HasPrefix(name, "<"): // an XML element name, already wire-level
		return name
	case strings.Contains(name, "."):
		return "object"
	}
	return "value"
}

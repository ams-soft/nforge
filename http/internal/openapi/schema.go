package openapi

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/http/internal/query"
)

type schemaGen struct {
	schemas   map[string]schema
	names     map[reflect.Type]string
	used      map[string]reflect.Type
	envelopes map[reflect.Type]string // response type -> success envelope component
}

var (
	timeType    = reflect.TypeFor[time.Time]()
	rawJSONType = reflect.TypeFor[json.RawMessage]()
)

var componentNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// componentName gives t a unique name in components.schemas.
func (g *schemaGen) componentName(t reflect.Type) string {
	if name, ok := g.names[t]; ok {
		return name
	}
	base := strings.Trim(componentNameRe.ReplaceAllString(t.Name(), "_"), "_")
	name := base
	if other, ok := g.used[name]; ok && other != t {
		pkg := t.PkgPath()
		if i := strings.LastIndexByte(pkg, '/'); i >= 0 {
			pkg = pkg[i+1:]
		}
		name = componentNameRe.ReplaceAllString(pkg, "_") + "." + base
		for i := 2; g.used[name] != nil; i++ {
			name = base + strconv.Itoa(i)
		}
	}
	g.names[t] = name
	g.used[name] = t
	return name
}

// successEnvelope registers, once per response type, a named component for
// the success envelope whose data is resType, and returns a $ref to it. Names
// follow the data: User -> UserEnvelope, []User -> UserListEnvelope, no
// response -> SuccessEnvelope; other types are named after the operation.
func (g *schemaGen) successEnvelope(resType reflect.Type, opID string) schema {
	if name, ok := g.envelopes[resType]; ok {
		return schema{"$ref": "#/components/schemas/" + name}
	}

	data := schema{}
	base := "Success"
	if resType != nil {
		data = g.schemaOf(resType, false)
		elem, suffix := resType, ""
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if k := elem.Kind(); k == reflect.Slice || k == reflect.Array {
			elem, suffix = elem.Elem(), "List"
			for elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
		}
		if name, ok := g.names[elem]; ok {
			base = name + suffix
		} else {
			base = strings.ToUpper(opID[:1]) + opID[1:]
		}
	}

	name := base + "Envelope"
	for i := 2; g.schemas[name] != nil || g.used[name] != nil; i++ {
		name = base + "Envelope" + strconv.Itoa(i)
	}
	g.envelopes[resType] = name
	g.schemas[name] = schema{
		"type": "object",
		"properties": schema{
			"success": schema{"type": "boolean", "const": true},
			"status":  schema{"type": "integer", "examples": []any{200}},
			"data":    data,
		},
		"required": []string{"success", "status"},
		"xml":      schema{"name": "response"},
	}
	return schema{"$ref": "#/components/schemas/" + name}
}

// schemaOf describes t as JSON Schema. inQuery=true uses the query string
// representation (e.g. time.Duration as text "1m30s").
func (g *schemaGen) schemaOf(t reflect.Type, inQuery bool) schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return schema{"type": "string", "format": "date-time"}
	case t == query.DurationType:
		if inQuery {
			return schema{"type": "string", "examples": []any{"1m30s"}}
		}
		return schema{"type": "integer", "format": "int64", "description": "Duration in nanoseconds."}
	case t == rawJSONType:
		return schema{}
	case codec.IsOpaque(t):
		return schema{"type": "string"}
	}
	switch t.Kind() {
	case reflect.String:
		return schema{"type": "string"}
	case reflect.Bool:
		return schema{"type": "boolean"}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return schema{"type": "integer", "format": "int32"}
	case reflect.Int, reflect.Int64:
		return schema{"type": "integer", "format": "int64"}
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return schema{"type": "integer", "format": "int32", "minimum": 0}
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return schema{"type": "integer", "format": "int64", "minimum": 0}
	case reflect.Float32:
		return schema{"type": "number", "format": "float"}
	case reflect.Float64:
		return schema{"type": "number", "format": "double"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return schema{"type": "string", "contentEncoding": "base64"}
		}
		return schema{"type": "array", "items": g.schemaOf(t.Elem(), inQuery)}
	case reflect.Map:
		return schema{"type": "object", "additionalProperties": g.schemaOf(t.Elem(), inQuery)}
	case reflect.Struct:
		if t.Name() == "" {
			return g.structSchema(t)
		}
		name := g.componentName(t)
		if _, ok := g.schemas[name]; !ok {
			g.schemas[name] = schema{} // placeholder: recursive types
			g.schemas[name] = g.structSchema(t)
		}
		return schema{"$ref": "#/components/schemas/" + name}
	}
	return schema{} // interface: any value
}

func (g *schemaGen) structSchema(t reflect.Type) schema {
	xmlByGo := map[string]codec.FieldInfo{}
	for _, f := range codec.Fields("xml", t) {
		xmlByGo[f.GoName] = f
	}
	props := schema{}
	var required []string
	for _, f := range codec.Fields("json", t) {
		s := g.schemaOf(f.Type, false)
		if xf, ok := xmlByGo[f.GoName]; ok && (xf.Name != f.Name || xf.Attr) {
			x := schema{}
			if xf.Name != f.Name {
				x["name"] = xf.Name
			}
			if xf.Attr {
				x["attribute"] = true
			}
			if _, isRef := s["$ref"]; isRef {
				s = schema{"allOf": []any{s}} // in 3.1 $ref accepts siblings, but not every UI honors them
			}
			s["xml"] = x
		}
		props[f.Name] = s
		if f.Required {
			required = append(required, f.Name)
		}
	}
	out := schema{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	if sf, ok := t.FieldByName("XMLName"); ok {
		if name, _, _ := strings.Cut(sf.Tag.Get("xml"), ","); name != "" && name != "-" {
			if i := strings.LastIndexByte(name, ' '); i >= 0 {
				name = name[i+1:]
			}
			out["xml"] = schema{"name": name}
		}
	}
	return out
}

func (g *schemaGen) queryParams(t reflect.Type) []parameter {
	var params []parameter
	for _, f := range query.Fields(t) {
		s := g.schemaOf(f.Type, true)
		if f.HasDefault {
			s["default"] = f.DefaultValue()
		}
		p := parameter{Name: f.Name, In: "query", Required: f.Required && !f.HasDefault, Schema: s}
		if s["type"] == "array" {
			explode := true
			p.Style, p.Explode = "form", &explode
		}
		params = append(params, p)
	}
	return params
}

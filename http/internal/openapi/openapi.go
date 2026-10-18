// Package openapi builds the OpenAPI 3.1 spec and the documentation page of
// the nhttp typed routes. Schemas follow the JSON names (the same as CBOR by
// default); where XML differs (name, attribute, root) the difference goes in
// the schema's `xml` object.
package openapi

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/http/internal/envelope"
	"gopkg.in/yaml.v3"
)

// Route is the documented shape of one route: nhttp converts its
// TypedRoutes into Routes. Schema and Response hold a value of the request
// and response types, or nil when the route has none.
type Route struct {
	Method         string
	Path           string
	Description    string
	Tag            string
	Headers        []string
	IsPrivate      bool
	AuthProfile    string
	RequiredScopes []string
	Tenant         bool
	TenantHeader   string
	Codec          string
	Schema         any
	Response       any
}

// Doc is a generated OpenAPI document.
type Doc struct {
	OpenAPI    string                           `json:"openapi"`
	Info       openAPIInfo                      `json:"info"`
	Servers    []openAPIServer                  `json:"servers,omitempty"`
	Tags       []openAPITag                     `json:"tags,omitempty"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components openAPIComponents                `json:"components"`
}

type openAPIInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

type openAPIServer struct {
	URL string `json:"url"`
}

type openAPITag struct {
	Name string `json:"name"`
}

type openAPIComponents struct {
	Schemas         map[string]schema `json:"schemas"`
	SecuritySchemes map[string]schema `json:"securitySchemes,omitempty"`
}

type operation struct {
	Tags        []string               `json:"tags,omitempty"`
	Summary     string                 `json:"summary,omitempty"`
	OperationID string                 `json:"operationId"`
	Parameters  []parameter            `json:"parameters,omitempty"`
	RequestBody *requestBody           `json:"requestBody,omitempty"`
	Responses   map[string]response    `json:"responses"`
	Security    *[]map[string][]string `json:"security,omitempty"` // nil: no auth; empty: public
	// AuthProfile and RequiredScopes describe enforced bearer authentication;
	// HTTP bearer security schemes cannot carry scopes themselves.
	AuthProfile    string   `json:"x-auth-profile,omitempty"`
	RequiredScopes []string `json:"x-required-scopes,omitempty"`
}

type parameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required,omitempty"`
	Schema   schema `json:"schema"`
	Style    string `json:"style,omitempty"`
	Explode  *bool  `json:"explode,omitempty"`
}

type requestBody struct {
	Required bool                 `json:"required"`
	Content  map[string]mediaType `json:"content"`
}

type response struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

type mediaType struct {
	Schema  schema `json:"schema"`
	Example any    `json:"example,omitempty"`
}

type schema = map[string]any

const bearerScheme = "bearerAuth"

// Build builds the spec of routes.
func Build(cfg config.DocsConfig, routes []Route) *Doc {
	g := &schemaGen{
		schemas:   map[string]schema{},
		names:     map[reflect.Type]string{},
		used:      map[string]reflect.Type{},
		envelopes: map[reflect.Type]string{},
	}
	doc := &Doc{
		OpenAPI: "3.1.0",
		Info:    openAPIInfo{Title: cfg.Title, Version: cfg.Version},
		Paths:   map[string]map[string]*operation{},
	}
	for _, h := range cfg.Hosts {
		doc.Servers = append(doc.Servers, openAPIServer{URL: h})
	}
	auth := cfg.Auth != "none"
	tags := map[string]bool{}

	g.schemas["ErrorResponse"] = schema{
		"type": "object",
		"properties": schema{
			"title":    schema{"type": "string", "examples": []any{envelope.Title(400)}},
			"detail":   schema{"description": "Error detail: text or object."},
			"instance": schema{"type": "string", "description": "Request path."},
		},
		"required": []string{"title", "detail", "instance"},
	}
	g.schemas["ErrorEnvelope"] = schema{
		"type": "object",
		"properties": schema{
			"success": schema{"type": "boolean", "const": false},
			"status":  schema{"type": "integer"},
			"error":   schema{"$ref": "#/components/schemas/ErrorResponse"},
		},
		"required": []string{"success", "status", "error"},
		"xml":      schema{"name": "response"},
	}

	for _, rt := range routes {
		path, pathParams := openAPIPath(rt.Path)
		summary := rt.Description
		if summary == "" {
			summary = rt.Method + " " + path
		}
		op := &operation{
			Summary:     summary,
			OperationID: operationID(rt.Method, path),
			Responses:   map[string]response{},
		}
		if rt.Tag != "" {
			op.Tags = []string{rt.Tag}
			tags[rt.Tag] = true
		}
		for _, p := range pathParams {
			op.Parameters = append(op.Parameters, parameter{Name: p.name, In: "path", Required: true, Schema: pathParamSchema(p.typ)})
		}
		for _, h := range rt.Headers {
			op.Parameters = append(op.Parameters, parameter{Name: h, In: "header", Required: true, Schema: schema{"type": "string"}})
		}
		if rt.TenantHeader != "" {
			op.Parameters = append(op.Parameters, parameter{Name: rt.TenantHeader, In: "header", Required: true, Schema: schema{"type": "string", "format": "uuid"}})
		}

		content := func(s schema) map[string]mediaType { return map[string]mediaType{rt.Codec: {Schema: s}} }
		errorResp := func(status int) {
			example := envelope.Response{Status: status, Error: &envelope.Error{
				Title: envelope.Title(status), Detail: "error detail", Instance: path,
			}}
			op.Responses[strconv.Itoa(status)] = response{
				Description: envelope.Title(status),
				Content: map[string]mediaType{rt.Codec: {
					Schema:  schema{"$ref": "#/components/schemas/ErrorEnvelope"},
					Example: example,
				}},
			}
		}

		if reqType := reflect.TypeOf(rt.Schema); reqType != nil {
			if rt.Method == "GET" {
				op.Parameters = append(op.Parameters, g.queryParams(reqType)...)
			} else {
				op.RequestBody = &requestBody{Required: true, Content: content(g.schemaOf(reqType, false))}
				errorResp(413)
				errorResp(415)
			}
			errorResp(400)
			errorResp(422)
		}

		op.Responses["200"] = response{
			Description: "Success",
			Content:     content(g.successEnvelope(reflect.TypeOf(rt.Response), op.OperationID)),
		}
		if auth {
			security := []map[string][]string{} // public: explicitly without authentication
			if rt.IsPrivate {
				security = append(security, map[string][]string{bearerScheme: {}})
				errorResp(400) // malformed Authorization header
				errorResp(401)
				if len(rt.RequiredScopes) > 0 {
					errorResp(403)
					op.RequiredScopes = rt.RequiredScopes
					op.Summary += " (scopes: " + strings.Join(rt.RequiredScopes, ", ") + ")"
				}
				op.AuthProfile = rt.AuthProfile
			}
			op.Security = &security
		}
		if rt.Tenant {
			errorResp(400) // invalid tenant
			errorResp(403) // missing tenant
		}
		errorResp(500)

		if doc.Paths[path] == nil {
			doc.Paths[path] = map[string]*operation{}
		}
		doc.Paths[path][strings.ToLower(rt.Method)] = op
	}

	for _, name := range slices.Sorted(maps.Keys(tags)) {
		doc.Tags = append(doc.Tags, openAPITag{Name: name})
	}
	doc.Components.Schemas = g.schemas
	if auth {
		doc.Components.SecuritySchemes = map[string]schema{
			bearerScheme: {"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
		}
	}
	return doc
}

// JSON renders the document as indented JSON.
func (d *Doc) JSON() ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

// YAML converts the JSON (which is valid YAML) into block YAML, keeping the
// key order.
func (d *Doc) YAML() ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	blockStyle(&node)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

func blockStyle(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style = 0
	}
	if n.Kind == yaml.ScalarNode && n.Style == yaml.DoubleQuotedStyle && n.Tag == "!!str" {
		n.Style = 0 // the encoder adds quotes back when needed
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}

var pathParamRe = regexp.MustCompile(`\{([^}:]+)(?::([^}]*))?\}`)

// pathParam is a route parameter with its router type ("" untyped, "int",
// "uuid"); the router itself rejects any other type at registration.
type pathParam struct {
	name, typ string
}

// openAPIPath replaces typed params ({id:int}) with {id} and returns the
// parameters in path order.
func openAPIPath(path string) (string, []pathParam) {
	var params []pathParam
	out := pathParamRe.ReplaceAllStringFunc(path, func(m string) string {
		sub := pathParamRe.FindStringSubmatch(m)
		params = append(params, pathParam{name: sub[1], typ: sub[2]})
		return "{" + sub[1] + "}"
	})
	return out, params
}

// pathParamSchema documents what the router's typed params accept: {x:int}
// matches [0-9]+ and {x:uuid} a canonical UUID; untyped params are strings.
func pathParamSchema(typ string) schema {
	switch typ {
	case "int":
		return schema{"type": "integer", "minimum": 0}
	case "uuid":
		return schema{"type": "string", "format": "uuid"}
	}
	return schema{"type": "string"}
}

var nonIdentRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

func operationID(method, path string) string {
	id := strings.ToLower(method)
	for part := range strings.SplitSeq(path, "/") {
		part = nonIdentRe.ReplaceAllString(part, " ")
		for w := range strings.FieldsSeq(part) {
			id += strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return id
}

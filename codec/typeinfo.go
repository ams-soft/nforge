package codec

import (
	"encoding"
	"encoding/json"
	"encoding/xml"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/fxamacker/cbor/v2"
)

// Field mapping per type, computed once and cached. Each format has its own
// list because the effective names (and the promotion of embedded structs)
// depend on the tag used:
//
//   - json: `json` tag, else the field name;
//   - cbor: `cbor` tag, else the `json` tag, else the field name (same rule as
//     fxamacker/cbor, so the presence check matches the lib);
//   - xml:  `xml` tag, else the `json` tag, else the field name.
//
// The `codec:"required"` tag marks the field as required in all formats.

type format int

const (
	fmtJSON format = iota
	fmtCBOR
	fmtXML
	numFormats
)

func (f format) String() string {
	switch f {
	case fmtJSON:
		return "json"
	case fmtCBOR:
		return "cbor"
	default:
		return "xml"
	}
}

type field struct {
	goName    string
	name      string
	space     string // XML namespace declared in the tag ("ns name"), xml only
	index     []int
	typ       reflect.Type
	tagged    bool
	omitEmpty bool
	omitZero  bool
	required  bool
	attr      bool   // xml: ",attr"
	chardata  bool   // xml: ",chardata"
	xmlOpt    string // xml option not supported by the codec (innerxml, any, comment, a>b)
}

type structInfo struct {
	fields [numFormats][]*field
	byName [numFormats]map[string]*field // key: name (xml: "@name" for attributes)

	xmlRoot      xml.Name // root element name taken from the XMLName field's tag
	xmlNameIndex []int    // index of the XMLName field, if any
}

var (
	structCache   sync.Map // reflect.Type -> *structInfo
	analysisCache sync.Map // reflect.Type -> error (nil = ok)

	xmlNameType = reflect.TypeFor[xml.Name]()
)

// analyze recursively validates the tags of t and of every type reachable
// from it. The result (including the error) is cached per type, so the
// reflection cost is paid only the first time the type goes through the codec.
func analyze(t reflect.Type) error {
	if t == nil {
		return nil
	}
	if err, ok := analysisCache.Load(t); ok {
		if err == nil {
			return nil
		}
		return err.(error)
	}
	err := analyzeRec(t, map[reflect.Type]bool{})
	if err != nil {
		analysisCache.Store(t, err)
	} else {
		analysisCache.Store(t, nil)
	}
	return err
}

func analyzeRec(t reflect.Type, seen map[reflect.Type]bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	if isOpaque(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return analyzeRec(t.Elem(), seen)
	case reflect.Map:
		return analyzeRec(t.Elem(), seen)
	case reflect.Struct:
		if err := checkTagConsistency(t); err != nil {
			return err
		}
		si := getStruct(t)
		for f := range numFormats {
			for _, fl := range si.fields[f] {
				if err := analyzeRec(fl.typ, seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkTagConsistency requires omitempty/omitzero to be the same in all tags
// present on a field: a field optional in one format is optional in all
// three, otherwise an XML payload would include as empty what JSON omits.
func checkTagConsistency(t reflect.Type) error {
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() && !sf.Anonymous {
			continue
		}
		var (
			first    string
			optional bool
		)
		for _, key := range []string{"json", "cbor", "xml"} {
			tag, ok := sf.Tag.Lookup(key)
			if !ok || tag == "" || tag == "-" {
				continue
			}
			_, opts := splitTag(tag)
			opt := hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero")
			if first == "" {
				first, optional = key, opt
				continue
			}
			if opt != optional {
				return &TagError{Type: t, Field: sf.Name,
					Msg: "inconsistent omitempty/omitzero between tags `" + first + "` and `" + key + "`"}
			}
		}
	}
	return nil
}

func getStruct(t reflect.Type) *structInfo {
	if si, ok := structCache.Load(t); ok {
		return si.(*structInfo)
	}
	si := &structInfo{}
	for f := range numFormats {
		si.fields[f] = collectFields(t, f)
		si.byName[f] = make(map[string]*field, len(si.fields[f]))
		for _, fl := range si.fields[f] {
			si.byName[f][fieldKey(fl)] = fl
		}
	}
	if sf, ok := t.FieldByName("XMLName"); ok && sf.Type == xmlNameType {
		si.xmlNameIndex = sf.Index
		if tag := sf.Tag.Get("xml"); tag != "" && tag != "-" {
			name, _ := splitTag(tag)
			si.xmlRoot = splitXMLName(name)
		}
	}
	actual, _ := structCache.LoadOrStore(t, si)
	return actual.(*structInfo)
}

func fieldKey(fl *field) string {
	if fl.attr {
		return "@" + fl.name
	}
	return fl.name
}

// tagFor returns the field's effective tag for the format and whether it came
// from an explicit tag.
func tagFor(sf reflect.StructField, f format) (tag string, tagged bool) {
	switch f {
	case fmtJSON:
		tag = sf.Tag.Get("json")
	case fmtCBOR:
		tag = sf.Tag.Get("cbor")
		if tag == "" {
			tag = sf.Tag.Get("json")
		}
	case fmtXML:
		tag = sf.Tag.Get("xml")
		if tag == "" {
			tag = sf.Tag.Get("json")
		} else if name, opts := splitTag(tag); name == "" && tag != "-" {
			// xml:",attr" without a name: inherits the name from the json tag.
			if jname, _ := splitTag(sf.Tag.Get("json")); jname != "" && jname != "-" {
				tag = jname + "," + opts
			}
		}
	}
	return tag, tag != ""
}

type pendingStruct struct {
	typ   reflect.Type
	index []int
	depth int
}

// collectFields follows the visibility rules of encoding/json (and
// fxamacker/cbor): embedded structs without a name in the tag have their
// fields promoted; on a name conflict the least nested wins and, on a tie, the
// one with an explicit tag; remaining ties drop all of them.
func collectFields(t reflect.Type, f format) []*field {
	var (
		all     []*field
		depthOf = map[*field]int{}
		current = []pendingStruct{{typ: t}}
		visited = map[reflect.Type]bool{}
	)
	for len(current) > 0 {
		var next []pendingStruct
		count := map[reflect.Type]int{}
		for _, p := range current {
			count[p.typ]++
		}
		for _, p := range current {
			if count[p.typ] > 1 || visited[p.typ] {
				continue
			}
			visited[p.typ] = true
			for i := range p.typ.NumField() {
				sf := p.typ.Field(i)
				ft := sf.Type
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if sf.Anonymous {
					if !sf.IsExported() && ft.Kind() != reflect.Struct {
						continue
					}
				} else if !sf.IsExported() {
					continue
				}
				if f == fmtXML && sf.Name == "XMLName" && sf.Type == xmlNameType {
					continue
				}
				tag, tagged := tagFor(sf, f)
				if tag == "-" {
					continue
				}
				name, opts := splitTag(tag)
				index := append(append([]int(nil), p.index...), i)

				if sf.Anonymous && ft.Kind() == reflect.Struct && name == "" {
					next = append(next, pendingStruct{typ: ft, index: index, depth: p.depth + 1})
					continue
				}

				fl := &field{
					goName:    sf.Name,
					index:     index,
					typ:       sf.Type,
					tagged:    tagged,
					omitEmpty: hasOpt(opts, "omitempty"),
					omitZero:  hasOpt(opts, "omitzero"),
					required:  hasOpt(sf.Tag.Get("codec"), "required"),
				}
				if f == fmtXML {
					xn := splitXMLName(name)
					fl.space, name = xn.Space, xn.Local
					fl.attr = hasOpt(opts, "attr")
					fl.chardata = hasOpt(opts, "chardata")
					for _, o := range []string{"innerxml", "any", "comment", "cdata"} {
						if hasOpt(opts, o) {
							fl.xmlOpt = o
						}
					}
					if strings.Contains(name, ">") {
						fl.xmlOpt = "a>b"
					}
				}
				if name == "" {
					name = sf.Name
				}
				fl.name = name
				all = append(all, fl)
				depthOf[fl] = p.depth
			}
		}
		current = next
	}

	byKey := map[string][]*field{}
	for _, fl := range all {
		k := fieldKey(fl)
		byKey[k] = append(byKey[k], fl)
	}
	var out []*field
	for _, group := range byKey {
		if len(group) == 1 {
			out = append(out, group[0])
			continue
		}
		minDepth := depthOf[group[0]]
		for _, fl := range group {
			minDepth = min(minDepth, depthOf[fl])
		}
		var top, tagged []*field
		for _, fl := range group {
			if depthOf[fl] == minDepth {
				top = append(top, fl)
				if fl.tagged {
					tagged = append(tagged, fl)
				}
			}
		}
		switch {
		case len(top) == 1:
			out = append(out, top[0])
		case len(tagged) == 1:
			out = append(out, tagged[0])
		}
	}
	sort.Slice(out, func(i, j int) bool { return lessIndex(out[i].index, out[j].index) })
	return out
}

func lessIndex(a, b []int) bool {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func splitTag(tag string) (name, opts string) {
	name, opts, _ = strings.Cut(tag, ",")
	return name, opts
}

func hasOpt(opts, opt string) bool {
	for o := range strings.SplitSeq(opts, ",") {
		if o == opt {
			return true
		}
	}
	return false
}

// splitXMLName parses "ns name" (encoding/xml form) or just "name".
func splitXMLName(s string) xml.Name {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return xml.Name{Space: s[:i], Local: s[i+1:]}
	}
	return xml.Name{Local: s}
}

var (
	textMarshalerType   = reflect.TypeFor[encoding.TextMarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonMarshalerType   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	xmlMarshalerType    = reflect.TypeFor[xml.Marshaler]()
	xmlUnmarshalerType  = reflect.TypeFor[xml.Unmarshaler]()
	cborMarshalerType   = reflect.TypeFor[cbor.Marshaler]()
	cborUnmarshalerType = reflect.TypeFor[cbor.Unmarshaler]()
)

// isOpaque reports types with custom (de)serialization: the codec does not
// look inside them (e.g. time.Time).
func isOpaque(t reflect.Type) bool {
	for _, it := range []reflect.Type{
		textMarshalerType, textUnmarshalerType,
		jsonMarshalerType, jsonUnmarshalerType,
		xmlMarshalerType, xmlUnmarshalerType,
		cborMarshalerType, cborUnmarshalerType,
	} {
		if implements(t, it) {
			return true
		}
	}
	return false
}

func implements(t, iface reflect.Type) bool {
	return t.Implements(iface) || (t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(iface))
}

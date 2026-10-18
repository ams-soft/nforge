package codec

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The XML codec does not use xml.Marshal/Unmarshal directly because
// encoding/xml only reads the `xml` tag; here the field mapping is the same as
// in the other formats (fallback to the `json` tag), and the decoder applies
// strict mode, required fields, namespace checks and DTD blocking.
// Token reading/writing is still done by encoding/xml.
//
// Representation:
//   - struct: element with one child per field (or attribute, with ",attr");
//   - slice: repeated element with the same name;
//   - map: <field><entry key="k">value</entry>...</field>;
//   - []byte: base64 text (same as JSON);
//   - types with MarshalXML/UnmarshalXML or MarshalText/UnmarshalText use
//     those methods.
//
// Namespace: a field without a namespace in its tag inherits the parent
// element's; an element in a namespace other than the expected one is an
// error (ErrNamespace).

type xmlCodec struct {
	strict   bool
	allowDTD bool
}

// NewXML creates the XML codec. Output always starts with xml.Header
// (<?xml version="1.0" encoding="UTF-8"?>). By default any <!DOCTYPE> is
// rejected (ErrDTDNotAllowed); external entities are never resolved.
func NewXML(opts Options) StreamCodec {
	return &xmlCodec{strict: opts.StrictUnknownFields, allowDTD: opts.AllowDTD}
}

func (c *xmlCodec) ContentType() string { return XML }

func (c *xmlCodec) Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := c.EncodeTo(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *xmlCodec) EncodeTo(w io.Writer, v any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	if err := analyze(rv.Type()); err != nil {
		return err
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	e := &xmlEncoder{enc: xml.NewEncoder(w)}
	if err := e.root(rv); err != nil {
		return err
	}
	if err := e.enc.Close(); err != nil {
		return xmlEncodeErr(err)
	}
	return nil
}

func (c *xmlCodec) Decode(data []byte, v any) error {
	return c.DecodeFrom(bytes.NewReader(data), v)
}

func (c *xmlCodec) DecodeFrom(r io.Reader, v any) error {
	if err := checkTarget(v); err != nil {
		return err
	}
	rv := reflect.ValueOf(v).Elem()
	if err := analyze(rv.Type()); err != nil {
		return err
	}
	raw := xml.NewDecoder(r)
	d := &xmlDecoder{
		raw:    raw,
		d:      xml.NewTokenDecoder(&xmlGuard{d: raw, allowDTD: c.allowDTD}),
		strict: c.strict,
	}
	return d.decodeDocument(rv)
}

// ---------------------------------------------------------------- encode

type xmlEncoder struct {
	enc *xml.Encoder
}

func (e *xmlEncoder) root(v reflect.Value) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	t := v.Type()
	if (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && t.Elem().Kind() != reflect.Uint8 {
		for i := range v.Len() {
			if err := e.root(v.Index(i)); err != nil {
				return err
			}
		}
		return nil
	}
	name := xml.Name{Local: t.Name()}
	if t.Kind() == reflect.Struct {
		if si := getStruct(t); si.xmlRoot.Local != "" {
			name = si.xmlRoot
		}
	}
	if name.Local == "" {
		return &UnsupportedTypeError{Format: "xml", Type: t}
	}
	return e.value(xml.StartElement{Name: name}, v)
}

func (e *xmlEncoder) value(start xml.StartElement, v reflect.Value) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	t := v.Type()

	if m, ok := asInterface[xml.Marshaler](v); ok {
		if err := e.enc.EncodeElement(m, start); err != nil {
			return xmlEncodeErr(err)
		}
		return nil
	}
	if m, ok := asInterface[encoding.TextMarshaler](v); ok {
		text, err := m.MarshalText()
		if err != nil {
			return fmt.Errorf("codec: xml: %w", err)
		}
		return e.textElement(start, string(text))
	}

	switch t.Kind() {
	case reflect.Struct:
		return e.structElement(start, v)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return e.textElement(start, base64.StdEncoding.EncodeToString(bytesOf(v)))
		}
		for i := range v.Len() {
			if err := e.value(start, v.Index(i)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		return e.mapElement(start, v)
	}
	text, ok := scalarText(v)
	if !ok {
		return &UnsupportedTypeError{Format: "xml", Type: t}
	}
	return e.textElement(start, text)
}

func (e *xmlEncoder) structElement(start xml.StartElement, v reflect.Value) error {
	t := v.Type()
	si := getStruct(t)
	fields := si.fields[fmtXML]

	for _, fl := range fields {
		if fl.xmlOpt != "" {
			return xmlOptErr(t, fl)
		}
		if !fl.attr {
			continue
		}
		fv, ok := fieldByIndex(v, fl.index)
		if !ok || omitField(fl, fv) {
			continue
		}
		text, present, err := attrText(fv)
		if err != nil {
			return err
		}
		if present {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Space: fl.space, Local: fl.name}, Value: text})
		}
	}
	if err := e.enc.EncodeToken(start); err != nil {
		return xmlEncodeErr(err)
	}
	for _, fl := range fields {
		if fl.attr {
			continue
		}
		fv, ok := fieldByIndex(v, fl.index)
		if !ok || omitField(fl, fv) {
			continue
		}
		if fl.chardata {
			text, present, err := attrText(fv)
			if err != nil {
				return err
			}
			if present {
				if err := e.enc.EncodeToken(xml.CharData(text)); err != nil {
					return xmlEncodeErr(err)
				}
			}
			continue
		}
		child := xml.StartElement{Name: xml.Name{Space: fl.space, Local: fl.name}}
		if err := e.value(child, fv); err != nil {
			return err
		}
	}
	if err := e.enc.EncodeToken(start.End()); err != nil {
		return xmlEncodeErr(err)
	}
	return nil
}

func (e *xmlEncoder) mapElement(start xml.StartElement, v reflect.Value) error {
	type entry struct {
		key string
		val reflect.Value
	}
	entries := make([]entry, 0, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		k, ok := keyText(iter.Key())
		if !ok {
			return &UnsupportedTypeError{Format: "xml", Type: v.Type()}
		}
		entries = append(entries, entry{k, iter.Value()})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.key, b.key) })

	if err := e.enc.EncodeToken(start); err != nil {
		return xmlEncodeErr(err)
	}
	for _, en := range entries {
		child := xml.StartElement{
			Name: xml.Name{Space: start.Name.Space, Local: "entry"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "key"}, Value: en.key}},
		}
		if err := e.value(child, en.val); err != nil {
			return err
		}
	}
	if err := e.enc.EncodeToken(start.End()); err != nil {
		return xmlEncodeErr(err)
	}
	return nil
}

func (e *xmlEncoder) textElement(start xml.StartElement, text string) error {
	for _, tok := range []xml.Token{start, xml.CharData(text), start.End()} {
		if err := e.enc.EncodeToken(tok); err != nil {
			return xmlEncodeErr(err)
		}
	}
	return nil
}

// attrText converts a simple value to text (attribute or chardata).
// present=false for a nil pointer/interface.
func attrText(v reflect.Value) (text string, present bool, err error) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", false, nil
		}
		v = v.Elem()
	}
	if m, ok := asInterface[encoding.TextMarshaler](v); ok {
		b, err := m.MarshalText()
		if err != nil {
			return "", false, fmt.Errorf("codec: xml: %w", err)
		}
		return string(b), true, nil
	}
	if (v.Kind() == reflect.Slice || v.Kind() == reflect.Array) && v.Type().Elem().Kind() == reflect.Uint8 {
		return base64.StdEncoding.EncodeToString(bytesOf(v)), true, nil
	}
	if s, ok := scalarText(v); ok {
		return s, true, nil
	}
	return "", false, &UnsupportedTypeError{Format: "xml", Type: v.Type()}
}

func scalarText(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.String:
		return v.String(), true
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(v.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits()), true
	}
	return "", false
}

func keyText(k reflect.Value) (string, bool) {
	if m, ok := asInterface[encoding.TextMarshaler](k); ok {
		b, err := m.MarshalText()
		return string(b), err == nil
	}
	return scalarText(k)
}

func bytesOf(v reflect.Value) []byte {
	if v.Kind() == reflect.Slice {
		return v.Bytes()
	}
	b := make([]byte, v.Len())
	reflect.Copy(reflect.ValueOf(b), v)
	return b
}

func omitField(fl *field, v reflect.Value) bool {
	if fl.omitEmpty && isEmptyValue(v) {
		return true
	}
	if fl.omitZero {
		if z, ok := asInterface[interface{ IsZero() bool }](v); ok {
			return z.IsZero()
		}
		return v.IsZero()
	}
	return false
}

// isEmptyValue follows encoding/json's definition of "empty" for omitempty.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

// fieldByIndex walks index without allocating; ok=false if it goes through a
// nil embedded pointer.
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 {
			for v.Kind() == reflect.Pointer {
				if v.IsNil() {
					return reflect.Value{}, false
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v, true
}

// fieldByIndexAlloc is the decode version: it allocates nil embedded pointers.
func fieldByIndexAlloc(v reflect.Value, index []int) reflect.Value {
	for i, x := range index {
		if i > 0 {
			for v.Kind() == reflect.Pointer {
				if v.IsNil() {
					v.Set(reflect.New(v.Type().Elem()))
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v
}

// asInterface returns v as T, also trying the pointer to v (methods with a
// pointer receiver).
func asInterface[T any](v reflect.Value) (T, bool) {
	if v.CanInterface() {
		if t, ok := v.Interface().(T); ok {
			return t, true
		}
	}
	if v.CanAddr() && v.Addr().CanInterface() {
		if t, ok := v.Addr().Interface().(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}

func xmlOptErr(t reflect.Type, fl *field) error {
	return &TagError{Type: t, Field: fl.goName, Msg: "xml option not supported by the codec:" + fl.xmlOpt}
}

func xmlEncodeErr(err error) error {
	var ut *xml.UnsupportedTypeError
	if errors.As(err, &ut) {
		return &UnsupportedTypeError{Format: "xml", Type: ut.Type}
	}
	return &EncodeError{Format: "xml", Msg: err.Error()}
}

// ---------------------------------------------------------------- decode

// xmlGuard sits between the raw tokenizer and the namespace-resolving
// decoder, blocking any directive (<!DOCTYPE>, <!ENTITY>) when DTD is not
// allowed.
type xmlGuard struct {
	d        *xml.Decoder
	allowDTD bool
}

func (g *xmlGuard) Token() (xml.Token, error) {
	tok, err := g.d.RawToken()
	if err != nil {
		return nil, err
	}
	if _, ok := tok.(xml.Directive); ok && !g.allowDTD {
		return nil, ErrDTDNotAllowed
	}
	return tok, nil
}

type xmlDecoder struct {
	raw    *xml.Decoder
	d      *xml.Decoder
	strict bool
}

func (d *xmlDecoder) token() (xml.Token, error) {
	tok, err := d.d.Token()
	if err != nil {
		return nil, d.mapErr(err)
	}
	return tok, nil
}

func (d *xmlDecoder) mapErr(err error) error {
	var syn *xml.SyntaxError
	switch {
	case errors.Is(err, ErrPayloadTooLarge), errors.Is(err, ErrDTDNotAllowed):
		return err
	case errors.As(err, &syn):
		return &SyntaxError{Format: "xml", Offset: d.raw.InputOffset(), Msg: syn.Error()}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &SyntaxError{Format: "xml", Offset: d.raw.InputOffset(), Msg: "unexpected end of payload"}
	}
	return err
}

func (d *xmlDecoder) decodeDocument(v reflect.Value) error {
	var start xml.StartElement
	for found := false; !found; {
		tok, err := d.d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return &SyntaxError{Format: "xml", Offset: 0, Msg: "empty payload"}
			}
			return d.mapErr(err)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			start, found = tk, true
		case xml.CharData:
			if len(bytes.TrimSpace(tk)) > 0 {
				return &SyntaxError{Format: "xml", Offset: d.raw.InputOffset(), Msg: "text outside the root element"}
			}
		}
	}

	t := v.Type()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var expected xml.Name
	if t.Kind() == reflect.Struct {
		expected = getStruct(t).xmlRoot
	}
	if expected.Local != "" && start.Name.Local != expected.Local {
		return &TypeError{Format: "xml", Field: "", Expected: "<" + expected.Local + ">", Got: "<" + start.Name.Local + ">"}
	}
	if err := d.value(start, v, "", expected.Space); err != nil {
		return err
	}

	for {
		tok, err := d.d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return d.mapErr(err)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			return &SyntaxError{Format: "xml", Offset: d.raw.InputOffset(), Msg: "more than one root element"}
		case xml.CharData:
			if len(bytes.TrimSpace(tk)) > 0 {
				return &SyntaxError{Format: "xml", Offset: d.raw.InputOffset(), Msg: "text after the root element"}
			}
		}
	}
}

// value decodes the start element (already consumed) into v.
func (d *xmlDecoder) value(start xml.StartElement, v reflect.Value, path, ns string) error {
	if start.Name.Space != ns {
		return &NamespaceError{Field: pathOr(path, start.Name.Local), Expected: ns, Got: start.Name.Space}
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	t := v.Type()

	if u, ok := asInterface[xml.Unmarshaler](v); ok {
		if err := d.d.DecodeElement(u, &start); err != nil {
			return d.mapErr(err)
		}
		return nil
	}
	if u, ok := asInterface[encoding.TextUnmarshaler](v); ok {
		text, err := d.leafText(start, t, path)
		if err != nil {
			return err
		}
		if err := u.UnmarshalText([]byte(text)); err != nil {
			return &TypeError{Format: "xml", Field: path, Expected: t.String(), Got: strconv.Quote(text)}
		}
		return nil
	}

	switch t.Kind() {
	case reflect.Struct:
		return d.structValue(start, v, path, ns)
	case reflect.Map:
		return d.mapValue(start, v, path, ns)
	case reflect.Slice:
		if t.Elem().Kind() != reflect.Uint8 {
			elem := reflect.New(t.Elem()).Elem()
			if err := d.value(start, elem, path, ns); err != nil {
				return err
			}
			v.Set(reflect.Append(v, elem))
			return nil
		}
	case reflect.Interface:
		if t.NumMethod() != 0 {
			return &UnsupportedTypeError{Format: "xml", Type: t}
		}
	}
	text, err := d.leafText(start, t, path)
	if err != nil {
		return err
	}
	return setText(v, text, path)
}

func (d *xmlDecoder) structValue(start xml.StartElement, v reflect.Value, path, ns string) error {
	t := v.Type()
	si := getStruct(t)
	if si.xmlNameIndex != nil {
		fieldByIndexAlloc(v, si.xmlNameIndex).Set(reflect.ValueOf(start.Name))
	}

	seen := map[*field]bool{}
	for _, a := range start.Attr {
		if isNamespaceDecl(a) {
			continue
		}
		fl := si.byName[fmtXML]["@"+a.Name.Local]
		if fl == nil {
			if d.strict {
				return &UnknownFieldError{Format: "xml", Field: path + "@" + a.Name.Local}
			}
			continue
		}
		if fl.xmlOpt != "" {
			return xmlOptErr(t, fl)
		}
		if a.Name.Space != fl.space {
			return &NamespaceError{Field: path + "@" + a.Name.Local, Expected: fl.space, Got: a.Name.Space}
		}
		if err := setText(fieldByIndexAlloc(v, fl.index), a.Value, path+"@"+fl.name); err != nil {
			return err
		}
		seen[fl] = true
	}

	var chardata *field
	for _, fl := range si.fields[fmtXML] {
		if fl.chardata {
			chardata = fl
		}
	}
	var text []byte
	arrayPos := map[*field]int{}

loop:
	for {
		tok, err := d.token()
		if err != nil {
			return err
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			fl := si.byName[fmtXML][tk.Name.Local]
			if fl == nil || fl.chardata {
				if d.strict {
					return &UnknownFieldError{Format: "xml", Field: joinPath(path, tk.Name.Local)}
				}
				if err := d.d.Skip(); err != nil {
					return d.mapErr(err)
				}
				continue
			}
			if fl.xmlOpt != "" {
				return xmlOptErr(t, fl)
			}
			childNS := fl.space
			if childNS == "" {
				childNS = ns
			}
			fv := fieldByIndexAlloc(v, fl.index)
			if fv.Kind() == reflect.Array && fv.Type().Elem().Kind() != reflect.Uint8 {
				i := arrayPos[fl]
				arrayPos[fl] = i + 1
				if i >= fv.Len() {
					if err := d.d.Skip(); err != nil {
						return d.mapErr(err)
					}
					continue
				}
				fv = fv.Index(i)
			}
			if err := d.value(tk, fv, joinPath(path, fl.name), childNS); err != nil {
				return err
			}
			seen[fl] = true
		case xml.CharData:
			if chardata != nil {
				text = append(text, tk...)
			}
		case xml.EndElement:
			break loop
		}
	}

	if chardata != nil && len(text) > 0 {
		if err := setText(fieldByIndexAlloc(v, chardata.index), string(text), path); err != nil {
			return err
		}
		seen[chardata] = true
	}
	for _, fl := range si.fields[fmtXML] {
		if fl.required && !seen[fl] {
			name := joinPath(path, fl.name)
			if fl.attr {
				name = path + "@" + fl.name
			}
			return &MissingFieldError{Format: "xml", Field: name}
		}
	}
	return nil
}

func (d *xmlDecoder) mapValue(start xml.StartElement, v reflect.Value, path, ns string) error {
	if err := d.rejectAttrs(start, path); err != nil {
		return err
	}
	t := v.Type()
	if v.IsNil() {
		v.Set(reflect.MakeMap(t))
	}
	for {
		tok, err := d.token()
		if err != nil {
			return err
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			key, hasKey := "", false
			attrs := tk.Attr[:0:0]
			for _, a := range tk.Attr {
				if a.Name.Local == "key" && a.Name.Space == "" {
					key, hasKey = a.Value, true
				} else {
					attrs = append(attrs, a)
				}
			}
			if tk.Name.Local != "entry" || !hasKey {
				if d.strict {
					return &UnknownFieldError{Format: "xml", Field: joinPath(path, tk.Name.Local)}
				}
				if err := d.d.Skip(); err != nil {
					return d.mapErr(err)
				}
				continue
			}
			tk.Attr = attrs
			kv := reflect.New(t.Key()).Elem()
			if err := setText(kv, key, joinPath(path, key)); err != nil {
				return err
			}
			ev := reflect.New(t.Elem()).Elem()
			if err := d.value(tk, ev, joinPath(path, key), ns); err != nil {
				return err
			}
			v.SetMapIndex(kv, ev)
		case xml.EndElement:
			return nil
		}
	}
}

// leafText reads the text content up to the end of the element; child
// elements are a type error, since the target is a simple value.
func (d *xmlDecoder) leafText(start xml.StartElement, t reflect.Type, path string) (string, error) {
	if err := d.rejectAttrs(start, path); err != nil {
		return "", err
	}
	var text []byte
	for {
		tok, err := d.token()
		if err != nil {
			return "", err
		}
		switch tk := tok.(type) {
		case xml.CharData:
			text = append(text, tk...)
		case xml.StartElement:
			return "", &TypeError{Format: "xml", Field: pathOr(path, start.Name.Local), Expected: t.String(), Got: "elemento <" + tk.Name.Local + ">"}
		case xml.EndElement:
			return string(text), nil
		}
	}
}

func (d *xmlDecoder) rejectAttrs(start xml.StartElement, path string) error {
	if !d.strict {
		return nil
	}
	for _, a := range start.Attr {
		if !isNamespaceDecl(a) {
			return &UnknownFieldError{Format: "xml", Field: path + "@" + a.Name.Local}
		}
	}
	return nil
}

// setText converts text (content or attribute) to the type of v.
func setText(v reflect.Value, text, path string) error {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	t := v.Type()
	mismatch := func() error {
		return &TypeError{Format: "xml", Field: path, Expected: t.String(), Got: strconv.Quote(text)}
	}
	if u, ok := asInterface[encoding.TextUnmarshaler](v); ok {
		if err := u.UnmarshalText([]byte(text)); err != nil {
			return mismatch()
		}
		return nil
	}
	trimmed := strings.TrimSpace(text)
	switch t.Kind() {
	case reflect.String:
		v.SetString(text)
	case reflect.Bool:
		b, err := strconv.ParseBool(trimmed)
		if err != nil {
			return mismatch()
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(trimmed, 10, t.Bits())
		if err != nil {
			return mismatch()
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, err := strconv.ParseUint(trimmed, 10, t.Bits())
		if err != nil {
			return mismatch()
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(trimmed, t.Bits())
		if err != nil {
			return mismatch()
		}
		v.SetFloat(f)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() != reflect.Uint8 {
			return &UnsupportedTypeError{Format: "xml", Type: t}
		}
		b, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return mismatch()
		}
		if t.Kind() == reflect.Slice {
			v.SetBytes(b)
		} else {
			reflect.Copy(v, reflect.ValueOf(b))
		}
	case reflect.Interface:
		if t.NumMethod() != 0 {
			return &UnsupportedTypeError{Format: "xml", Type: t}
		}
		v.Set(reflect.ValueOf(text))
	default:
		return &UnsupportedTypeError{Format: "xml", Type: t}
	}
	return nil
}

func isNamespaceDecl(a xml.Attr) bool {
	return a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns")
}

func pathOr(path, name string) string {
	if path == "" {
		return name
	}
	return path
}

package qrcode

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// Limits on logo input. They are deliberately conservative: a logo is a
// small mark, and every limit is checked before the allocation it protects.
const (
	maxLogoBytes     = 1 << 20 // encoded logo, PNG/JPEG/SVG
	maxLogoDim       = 4096    // width or height of a decoded raster logo
	maxLogoPixels    = 4 << 20 // width*height of a decoded raster logo
	embedLogoMaxSide = 512     // raster logos are downscaled to this side for storage and SVG embedding
	maxSVGElements   = 2000    // SVG logo element budget
	maxSVGDepth      = 16      // SVG logo nesting
	maxSVGSegments   = 50000   // path segments of an SVG logo in total
	maxSVGCoord      = 1e6     // |coordinate| ceiling of an SVG logo
	svgNS            = "http://www.w3.org/2000/svg"
)

// logoImage is a validated logo, either a raster (PNG/JPEG, decoded once,
// re-encoded as PNG for SVG output) or a vector built from the strict SVG
// subset of parseSVGLogo. Raw input never reaches the output.
type logoImage struct {
	aspect float64 // width / height
	raster *image.NRGBA
	pngB64 string // base64 of the re-encoded PNG (raster only)
	vec    *svgLogo
}

func unsupportedLogo(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedLogo, fmt.Sprintf(format, args...))
}

// loadLogo detects the format from the content (never from a name or MIME
// type) and validates it.
func loadLogo(data []byte) (*logoImage, error) {
	if len(data) == 0 {
		return nil, unsupportedLogo("empty logo")
	}
	if len(data) > maxLogoBytes {
		return nil, unsupportedLogo("logo larger than %d bytes", maxLogoBytes)
	}
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return loadRaster(data, png.DecodeConfig, png.Decode)
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return loadRaster(data, jpeg.DecodeConfig, jpeg.Decode)
	}
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '<' {
		v, err := parseSVGLogo(data)
		if err != nil {
			return nil, err
		}
		return &logoImage{aspect: v.w / v.h, vec: v}, nil
	}
	return nil, unsupportedLogo("unrecognized logo format (PNG, JPEG and SVG are supported)")
}

func loadRaster(data []byte, cfgFn func(io.Reader) (image.Config, error), decFn func(io.Reader) (image.Image, error)) (*logoImage, error) {
	cfg, err := cfgFn(bytes.NewReader(data))
	if err != nil {
		return nil, unsupportedLogo("invalid image header")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxLogoDim || cfg.Height > maxLogoDim ||
		int64(cfg.Width)*int64(cfg.Height) > maxLogoPixels {
		return nil, unsupportedLogo("image dimensions %dx%d exceed the limit", cfg.Width, cfg.Height)
	}
	img, err := decFn(bytes.NewReader(data))
	if err != nil {
		return nil, unsupportedLogo("invalid image data")
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if m := max(w, h); m > embedLogoMaxSide {
		w, h = max(1, w*embedLogoMaxSide/m), max(1, h*embedLogoMaxSide/m)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, unsupportedLogo("cannot re-encode image")
	}
	return &logoImage{
		aspect: float64(b.Dx()) / float64(b.Dy()),
		raster: dst,
		pngB64: base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// --- strict SVG subset -------------------------------------------------

type point struct{ x, y float64 }

// pathCmd is one normalized path command: M and L use a, C uses a, b, c
// (control, control, end), Z uses none. Arcs, quadratics and relative or
// smooth forms are converted while parsing.
type pathCmd struct {
	op byte
	a  point
	b  point
	c  point
}

type svgShape struct {
	cmds []pathCmd
	fill color.NRGBA
}

// svgLogo is the validated model of an SVG logo: viewBox plus filled paths.
type svgLogo struct {
	vx, vy, w, h float64
	shapes       []svgShape
}

type svgStyle struct {
	fill    color.NRGBA
	hasFill bool
	opacity float64
}

var svgColors = map[string]color.NRGBA{
	"black": {0, 0, 0, 255}, "white": {255, 255, 255, 255}, "red": {255, 0, 0, 255},
	"green": {0, 128, 0, 255}, "blue": {0, 0, 255, 255}, "yellow": {255, 255, 0, 255},
	"gray": {128, 128, 128, 255}, "grey": {128, 128, 128, 255}, "orange": {255, 165, 0, 255},
	"purple": {128, 0, 128, 255}, "transparent": {0, 0, 0, 0},
}

func parseSVGColor(s string) (c color.NRGBA, none bool, err error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "none" {
		return c, true, nil
	}
	if v, ok := svgColors[s]; ok {
		return v, false, nil
	}
	if strings.HasPrefix(s, "#") && (len(s) == 4 || len(s) == 7) {
		h := s[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		n, perr := strconv.ParseUint(h, 16, 32)
		if perr == nil {
			return color.NRGBA{uint8(n >> 16), uint8(n >> 8), uint8(n), 255}, false, nil //nolint:gosec // G115: n is a 24-bit color, each shifted byte fits uint8
		}
	}
	return c, false, unsupportedLogo("unsupported SVG color")
}

func parseUnit(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "px"))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > maxSVGCoord {
		return 0, unsupportedLogo("invalid SVG number")
	}
	return v, nil
}

func parseSVGLogo(data []byte) (*svgLogo, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil // no custom entities; the five predefined ones only
	logo := &svgLogo{}
	var (
		stack    []svgStyle
		elements int
		segments int
		sawRoot  bool
		closed   bool
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, unsupportedLogo("malformed SVG")
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, unsupportedLogo("SVG DOCTYPE and entity declarations are not allowed")
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, unsupportedLogo("SVG processing instructions are not allowed")
			}
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, unsupportedLogo("SVG text content is not allowed")
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				closed = true
			}
		case xml.StartElement:
			if closed {
				return nil, unsupportedLogo("multiple SVG roots")
			}
			elements++
			if elements > maxSVGElements || len(stack) >= maxSVGDepth {
				return nil, unsupportedLogo("SVG too complex")
			}
			if t.Name.Space != "" && t.Name.Space != svgNS {
				return nil, unsupportedLogo("SVG element in a foreign namespace")
			}
			name := t.Name.Local
			if !sawRoot {
				if name != "svg" {
					return nil, unsupportedLogo("root element is not svg")
				}
			} else if name == "svg" {
				return nil, unsupportedLogo("nested svg elements are not supported")
			}
			attrs, err := collectSVGAttrs(name, t.Attr, !sawRoot)
			if err != nil {
				return nil, err
			}
			style := svgStyle{fill: color.NRGBA{0, 0, 0, 255}, hasFill: true, opacity: 1}
			if len(stack) > 0 {
				style = stack[len(stack)-1]
			}
			if v, ok := attrs["fill"]; ok {
				c, none, err := parseSVGColor(v)
				if err != nil {
					return nil, err
				}
				style.fill, style.hasFill = c, !none
			}
			for _, k := range []string{"opacity", "fill-opacity"} {
				if v, ok := attrs[k]; ok {
					f, err := parseUnit(v)
					if err != nil || f < 0 || f > 1 {
						return nil, unsupportedLogo("SVG opacity must be between 0 and 1")
					}
					style.opacity *= f
				}
			}
			if v, ok := attrs["fill-rule"]; ok && strings.TrimSpace(v) != "nonzero" {
				return nil, unsupportedLogo("only fill-rule nonzero is supported")
			}
			if !sawRoot {
				sawRoot = true
				if err := svgViewport(logo, attrs); err != nil {
					return nil, err
				}
			}
			stack = append(stack, style)
			if name == "svg" || name == "g" {
				continue
			}
			cmds, err := shapeCommands(name, attrs)
			if err != nil {
				return nil, err
			}
			segments += len(cmds)
			if segments > maxSVGSegments {
				return nil, unsupportedLogo("SVG too complex")
			}
			if style.hasFill && len(cmds) > 0 {
				c := style.fill
				c.A = uint8(math.Round(float64(c.A) * style.opacity))
				logo.shapes = append(logo.shapes, svgShape{cmds: cmds, fill: c})
			}
		}
	}
	if !sawRoot || !closed {
		return nil, unsupportedLogo("malformed SVG")
	}
	if len(logo.shapes) == 0 {
		return nil, unsupportedLogo("SVG has nothing to draw")
	}
	return logo, nil
}

var svgAllowedAttrs = map[string][]string{
	"svg":     {"xmlns", "version", "width", "height", "viewBox"},
	"g":       {},
	"path":    {"d"},
	"rect":    {"x", "y", "width", "height", "rx", "ry"},
	"circle":  {"cx", "cy", "r"},
	"ellipse": {"cx", "cy", "rx", "ry"},
	"polygon": {"points"},
}

func collectSVGAttrs(elem string, in []xml.Attr, root bool) (map[string]string, error) {
	allowed, ok := svgAllowedAttrs[elem]
	if !ok {
		return nil, unsupportedLogo("unsupported SVG element <%s>", safeName(elem))
	}
	out := make(map[string]string, len(in))
	for _, a := range in {
		n := a.Name.Local
		if a.Name.Space != "" {
			if a.Name.Space == "xmlns" || n == "xmlns" {
				return nil, unsupportedLogo("SVG namespace prefixes are not supported")
			}
			return nil, unsupportedLogo("unsupported SVG attribute")
		}
		if n == "xmlns" {
			if !root || a.Value != svgNS {
				return nil, unsupportedLogo("unexpected xmlns")
			}
			continue
		}
		common := n == "id" || n == "fill" || n == "fill-opacity" || n == "opacity" || n == "fill-rule"
		if n == "id" {
			continue // ignored, never emitted
		}
		if !common && !contains(allowed, n) {
			return nil, unsupportedLogo("unsupported SVG attribute %q on <%s>", safeName(n), elem)
		}
		if _, dup := out[n]; dup {
			return nil, unsupportedLogo("duplicate SVG attribute")
		}
		out[n] = a.Value
	}
	return out, nil
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// safeName keeps error text short and printable: names come from untrusted input.
func safeName(s string) string {
	if len(s) > 24 {
		s = s[:24]
	}
	q := strconv.QuoteToASCII(s)
	return q[1 : len(q)-1]
}

func svgViewport(l *svgLogo, a map[string]string) error {
	if vb, ok := a["viewBox"]; ok {
		f := strings.FieldsFunc(vb, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r' })
		if len(f) != 4 {
			return unsupportedLogo("invalid SVG viewBox")
		}
		var v [4]float64
		for i := range v {
			x, err := parseUnit(f[i])
			if err != nil {
				return err
			}
			v[i] = x
		}
		l.vx, l.vy, l.w, l.h = v[0], v[1], v[2], v[3]
	} else {
		w, werr := parseUnit(a["width"])
		h, herr := parseUnit(a["height"])
		if werr != nil || herr != nil {
			return unsupportedLogo("SVG needs a viewBox or numeric width and height")
		}
		l.w, l.h = w, h
	}
	if l.w <= 0 || l.h <= 0 {
		return unsupportedLogo("SVG viewport must be positive")
	}
	if r := l.w / l.h; r > 20 || r < 1.0/20 {
		return unsupportedLogo("SVG aspect ratio out of range")
	}
	return nil
}

const kappa = 0.5522847498307936

func ellipsePath(cx, cy, rx, ry float64) []pathCmd {
	kx, ky := rx*kappa, ry*kappa
	return []pathCmd{
		{op: 'M', a: point{cx + rx, cy}},
		{op: 'C', a: point{cx + rx, cy + ky}, b: point{cx + kx, cy + ry}, c: point{cx, cy + ry}},
		{op: 'C', a: point{cx - kx, cy + ry}, b: point{cx - rx, cy + ky}, c: point{cx - rx, cy}},
		{op: 'C', a: point{cx - rx, cy - ky}, b: point{cx - kx, cy - ry}, c: point{cx, cy - ry}},
		{op: 'C', a: point{cx + kx, cy - ry}, b: point{cx + rx, cy - ky}, c: point{cx + rx, cy}},
		{op: 'Z'},
	}
}

func numAttr(a map[string]string, key string, def float64) (float64, error) {
	v, ok := a[key]
	if !ok {
		return def, nil
	}
	return parseUnit(v)
}

func shapeCommands(name string, a map[string]string) ([]pathCmd, error) {
	switch name {
	case "path":
		return parsePathData(a["d"])
	case "rect":
		x, e1 := numAttr(a, "x", 0)
		y, e2 := numAttr(a, "y", 0)
		w, e3 := numAttr(a, "width", 0)
		h, e4 := numAttr(a, "height", 0)
		rx, e5 := numAttr(a, "rx", -1)
		ry, e6 := numAttr(a, "ry", -1)
		if err := errors.Join(e1, e2, e3, e4, e5, e6); err != nil {
			return nil, unsupportedLogo("invalid SVG rect")
		}
		if w <= 0 || h <= 0 {
			return nil, nil
		}
		if rx < 0 {
			rx = max(ry, 0)
		}
		if ry < 0 {
			ry = rx
		}
		rx, ry = min(rx, w/2), min(ry, h/2)
		if rx == 0 || ry == 0 {
			return []pathCmd{{op: 'M', a: point{x, y}}, {op: 'L', a: point{x + w, y}}, {op: 'L', a: point{x + w, y + h}}, {op: 'L', a: point{x, y + h}}, {op: 'Z'}}, nil
		}
		kx, ky := rx*kappa, ry*kappa
		return []pathCmd{
			{op: 'M', a: point{x + rx, y}},
			{op: 'L', a: point{x + w - rx, y}},
			{op: 'C', a: point{x + w - rx + kx, y}, b: point{x + w, y + ry - ky}, c: point{x + w, y + ry}},
			{op: 'L', a: point{x + w, y + h - ry}},
			{op: 'C', a: point{x + w, y + h - ry + ky}, b: point{x + w - rx + kx, y + h}, c: point{x + w - rx, y + h}},
			{op: 'L', a: point{x + rx, y + h}},
			{op: 'C', a: point{x + rx - kx, y + h}, b: point{x, y + h - ry + ky}, c: point{x, y + h - ry}},
			{op: 'L', a: point{x, y + ry}},
			{op: 'C', a: point{x, y + ry - ky}, b: point{x + rx - kx, y}, c: point{x + rx, y}},
			{op: 'Z'},
		}, nil
	case "circle":
		cx, e1 := numAttr(a, "cx", 0)
		cy, e2 := numAttr(a, "cy", 0)
		r, e3 := numAttr(a, "r", 0)
		if err := errors.Join(e1, e2, e3); err != nil {
			return nil, unsupportedLogo("invalid SVG circle")
		}
		if r <= 0 {
			return nil, nil
		}
		return ellipsePath(cx, cy, r, r), nil
	case "ellipse":
		cx, e1 := numAttr(a, "cx", 0)
		cy, e2 := numAttr(a, "cy", 0)
		rx, e3 := numAttr(a, "rx", 0)
		ry, e4 := numAttr(a, "ry", 0)
		if err := errors.Join(e1, e2, e3, e4); err != nil {
			return nil, unsupportedLogo("invalid SVG ellipse")
		}
		if rx <= 0 || ry <= 0 {
			return nil, nil
		}
		return ellipsePath(cx, cy, rx, ry), nil
	case "polygon":
		nums, err := scanNumbers(a["points"])
		if err != nil || len(nums) < 6 || len(nums)%2 != 0 {
			return nil, unsupportedLogo("invalid SVG polygon")
		}
		var cmds []pathCmd
		for i := 0; i < len(nums); i += 2 {
			op := byte('L')
			if i == 0 {
				op = 'M'
			}
			cmds = append(cmds, pathCmd{op: op, a: point{nums[i], nums[i+1]}})
		}
		return append(cmds, pathCmd{op: 'Z'}), nil
	}
	return nil, nil
}

// scanNumbers reads a whitespace/comma separated list of SVG numbers.
func scanNumbers(s string) ([]float64, error) {
	p := pathScanner{s: s}
	var out []float64
	for {
		p.skipSep()
		if p.i >= len(p.s) {
			return out, nil
		}
		v, err := p.number()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) > maxSVGSegments*6 {
			return nil, unsupportedLogo("SVG too complex")
		}
	}
}

type pathScanner struct {
	s string
	i int
}

func (p *pathScanner) skipSep() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == ',' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *pathScanner) number() (float64, error) {
	start := p.i
	if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
		p.i++
	}
	digits := 0
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
		digits++
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			digits++
		}
	}
	if digits == 0 {
		return 0, unsupportedLogo("invalid SVG number")
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		j := p.i + 1
		if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
			j++
		}
		if j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
			for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
				j++
			}
			p.i = j
		}
	}
	return parseUnit(p.s[start:p.i])
}

func parsePathData(d string) ([]pathCmd, error) {
	p := pathScanner{s: d}
	var (
		cmds        []pathCmd
		cur, start  point
		lastCtl     point // last cubic/quadratic control point
		lastOp      byte
		cmd         byte
		haveSubpath bool
	)
	args := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2, 'Z': 0}
	for {
		p.skipSep()
		if p.i >= len(p.s) {
			break
		}
		c := p.s[p.i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			p.i++
			cmd = c
		} else if cmd == 0 || cmd == 'Z' || cmd == 'z' {
			return nil, unsupportedLogo("invalid SVG path data")
		} else if cmd == 'M' { // implicit repeats of M are L
			cmd = 'L'
		} else if cmd == 'm' {
			cmd = 'l'
		}
		up := cmd &^ 0x20
		if up == 'A' {
			return nil, unsupportedLogo("SVG arc commands are not supported")
		}
		n, ok := args[up]
		if !ok {
			return nil, unsupportedLogo("unsupported SVG path command")
		}
		rel := cmd >= 'a'
		var v [6]float64
		for i := range n {
			p.skipSep()
			x, err := p.number()
			if err != nil {
				return nil, err
			}
			v[i] = x
		}
		off := point{}
		if rel {
			off = cur
		}
		abs := func(i int) point { return point{v[i] + off.x, v[i+1] + off.y} }
		if len(cmds) > maxSVGSegments {
			return nil, unsupportedLogo("SVG too complex")
		}
		if up != 'M' && up != 'Z' && !haveSubpath {
			return nil, unsupportedLogo("SVG path must start with a moveto")
		}
		switch up {
		case 'M':
			if haveSubpath {
				cmds = append(cmds, pathCmd{op: 'Z'})
			}
			cur = abs(0)
			start = cur
			cmds = append(cmds, pathCmd{op: 'M', a: cur})
			haveSubpath = true
		case 'L':
			cur = abs(0)
			cmds = append(cmds, pathCmd{op: 'L', a: cur})
		case 'H':
			cur = point{v[0] + off.x, cur.y}
			cmds = append(cmds, pathCmd{op: 'L', a: cur})
		case 'V':
			cur = point{cur.x, v[0] + off.y}
			cmds = append(cmds, pathCmd{op: 'L', a: cur})
		case 'C':
			c1, c2, e := abs(0), abs(2), abs(4)
			cmds = append(cmds, pathCmd{op: 'C', a: c1, b: c2, c: e})
			lastCtl, cur = c2, e
		case 'S':
			c1 := cur
			if lastOp == 'C' || lastOp == 'S' {
				c1 = point{2*cur.x - lastCtl.x, 2*cur.y - lastCtl.y}
			}
			c2, e := abs(0), abs(2)
			cmds = append(cmds, pathCmd{op: 'C', a: c1, b: c2, c: e})
			lastCtl, cur = c2, e
		case 'Q', 'T':
			var q, e point
			if up == 'Q' {
				q, e = abs(0), abs(2)
			} else {
				q = cur
				if lastOp == 'Q' || lastOp == 'T' {
					q = point{2*cur.x - lastCtl.x, 2*cur.y - lastCtl.y}
				}
				e = abs(0)
			}
			cmds = append(cmds, pathCmd{op: 'C',
				a: point{cur.x + 2.0/3*(q.x-cur.x), cur.y + 2.0/3*(q.y-cur.y)},
				b: point{e.x + 2.0/3*(q.x-e.x), e.y + 2.0/3*(q.y-e.y)},
				c: e})
			lastCtl, cur = q, e
		case 'Z':
			cmds = append(cmds, pathCmd{op: 'Z'})
			cur = start
			haveSubpath = false
		}
		lastOp = up
	}
	if haveSubpath {
		cmds = append(cmds, pathCmd{op: 'Z'})
	}
	return cmds, nil
}

// rasterize draws the SVG logo into a w×h tile (viewBox stretched to the
// tile; the caller sizes the tile with the logo's aspect ratio).
func (l *svgLogo) rasterize(w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	sx, sy := float64(w)/l.w, float64(h)/l.h
	tx := func(p point) (float32, float32) { return float32((p.x - l.vx) * sx), float32((p.y - l.vy) * sy) }
	z := vector.NewRasterizer(w, h)
	for _, sh := range l.shapes {
		z.Reset(w, h)
		var start point
		open := false
		for _, c := range sh.cmds {
			switch c.op {
			case 'M':
				x, y := tx(c.a)
				z.MoveTo(x, y)
				start, open = c.a, true
			case 'L':
				x, y := tx(c.a)
				z.LineTo(x, y)
			case 'C':
				x1, y1 := tx(c.a)
				x2, y2 := tx(c.b)
				x3, y3 := tx(c.c)
				z.CubeTo(x1, y1, x2, y2, x3, y3)
			case 'Z':
				if open {
					x, y := tx(start)
					z.LineTo(x, y)
					z.ClosePath()
					open = false
				}
			}
		}
		z.DrawOp = xdraw.Over
		z.Draw(dst, dst.Bounds(), image.NewUniform(sh.fill), image.Point{})
	}
	return dst
}

// pathData serializes the normalized commands as SVG path data.
func (sh svgShape) pathData(vx, vy float64) string {
	var b strings.Builder
	for i, c := range sh.cmds {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte(c.op)
		switch c.op {
		case 'M', 'L':
			b.WriteString(num(c.a.x-vx) + " " + num(c.a.y-vy))
		case 'C':
			b.WriteString(num(c.a.x-vx) + " " + num(c.a.y-vy) + " " + num(c.b.x-vx) + " " + num(c.b.y-vy) + " " + num(c.c.x-vx) + " " + num(c.c.y-vy))
		}
	}
	return b.String()
}

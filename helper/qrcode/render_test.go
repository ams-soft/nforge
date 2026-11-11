package qrcode

import (
	"bytes"
	"encoding/xml"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func mustPNG(t *testing.T, q *QRCode) (image.Image, []byte) {
	t.Helper()
	b, err := q.PNG()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img, b
}

func TestPNGHeaderAndColors(t *testing.T) {
	q, _ := New("pixels", WithSize(300))
	img, raw := mustPNG(t, q)
	if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("not a PNG")
	}
	if b := img.Bounds(); b.Dx() != q.Size() || b.Dy() != q.Size() {
		t.Fatalf("bounds %v, Size() %d", b, q.Size())
	}
	// Square modules at an integer scale: no interpolation, only two colors.
	seen := map[color.Color]bool{}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			seen[img.At(x, y)] = true
		}
	}
	if len(seen) != 2 {
		t.Errorf("%d distinct colors, want 2", len(seen))
	}
}

// TestPNGMatchesMatrix samples the center of every module and compares it
// with the matrix, for every style, so the renderer cannot drift from it.
func TestPNGMatchesMatrix(t *testing.T) {
	styles := map[string][]Option{
		"square":        nil,
		"rounded":       {WithRoundedModules(0.25)},
		"circle":        {WithRoundedModules(0.5)},
		"finders":       {WithRoundedFinders(0.2)},
		"finders-max":   {WithRoundedFinders(MaxFinderRadius)},
		"both":          {WithRoundedModules(0.3), WithRoundedFinders(MaxFinderRadius)},
		"colors":        {WithColors(color.NRGBA{10, 40, 90, 255}, color.NRGBA{240, 240, 200, 255})},
		"margin-2":      {WithMargin(2)},
		"margin-8-both": {WithMargin(8), WithRoundedModules(0.4), WithRoundedFinders(MaxFinderRadius)},
	}
	for name, opts := range styles {
		q, err := New("https://example.com/matrix", append([]Option{WithSize(600)}, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		img, _ := mustPNG(t, q)
		m := q.Modules()
		s, off := q.scale, q.margin*q.scale
		fg, bg := q.opt.fg, q.opt.bg
		for y := range m {
			for x := range m {
				c := color.NRGBAModel.Convert(img.At(off+x*s+s/2, off+y*s+s/2)).(color.NRGBA)
				want := bg
				if m[y][x] {
					want = fg
				}
				// finder rounding and circles may leave the corner of a module
				// light, never its center.
				if c != want {
					t.Fatalf("%s: module (%d,%d) center %v, want %v", name, x, y, c, want)
				}
			}
		}
		// The quiet zone stays background.
		if c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); c != bg {
			t.Errorf("%s: corner is %v", name, c)
		}
	}
}

func TestStylesDecode(t *testing.T) {
	logo := testLogoPNG(t, 64, 64)
	combos := map[string][]Option{
		"traditional":   nil,
		"rounded":       {WithRoundedModules(0.25)},
		"rounded-max":   {WithRoundedModules(0.5)},
		"finders":       {WithRoundedFinders(0.15)},
		"finders-max":   {WithRoundedFinders(MaxFinderRadius)},
		"both":          {WithRoundedModules(0.25), WithRoundedFinders(0.15)},
		"logo":          {WithLogo(logo)},
		"logo-rounded":  {WithRoundedModules(0.25), WithRoundedFinders(0.15), WithLogo(logo, LogoBorderRadius(0.2), LogoBackgroundRadius(0.2))},
		"logo-noplate":  {WithLogo(logo, LogoBackground(false))},
		"logo-jpeg":     {WithLogo(testLogoJPEG(t))},
		"logo-svg":      {WithLogo([]byte(testLogoSVG), LogoSize(0.15))},
		"logo-L-fixed":  {WithErrorCorrection(Quartile), WithLogo(logo)},
		"logo-pad0":     {WithLogo(logo, LogoPadding(0))},
		"logo-big":      {WithLogo(logo, LogoSize(0.2), LogoPadding(0.5))},
		"colors-logo":   {WithColors(color.NRGBA{20, 20, 80, 255}, color.White), WithLogo(logo)},
		"quartile-both": {WithErrorCorrection(Quartile), WithRoundedModules(0.2), WithRoundedFinders(0.2)},
	}
	for _, size := range []int{256, 512, 1024} {
		for name, opts := range combos {
			payload := "https://example.com/pay?id=teste123"
			q, err := New(payload, append([]Option{WithSize(size)}, opts...)...)
			if err != nil {
				t.Fatalf("%s/%d: %v", name, size, err)
			}
			_, raw := mustPNG(t, q)
			if got := decodePNG(t, raw); got != payload {
				t.Errorf("%s/%d (v%d): decoded %q", name, size, q.Version(), got)
			}
		}
	}
}

func TestLogoOnVersionsWithCenterAlignment(t *testing.T) {
	logo := testLogoPNG(t, 48, 48)
	bumped, decoded := 0, 0
	for n := 60; n <= 700; n += 23 {
		payload := strings.Repeat("a1B", n/3+1)[:n] + "/q?"
		plain, err := New(payload, WithErrorCorrection(High))
		if err != nil {
			continue
		}
		q, err := New(payload, WithErrorCorrection(High), WithLogo(logo), WithSize(1600))
		if err != nil {
			if errors.Is(err, ErrLogoOverlap) || errors.Is(err, ErrTooLarge) {
				continue
			}
			t.Fatalf("n=%d: %v", n, err)
		}
		if q.Version() > plain.Version() {
			bumped++
		}
		// Invariant: nothing the logo covers is a function module.
		fn := functionMask(q.Version())
		if !q.logoClear(q.logo, fn, q.sym.size) {
			t.Fatalf("n=%d: logo touches a function pattern", n)
		}
		_, raw := mustPNG(t, q)
		if got, err := func() (string, error) {
			img, _ := png.Decode(bytes.NewReader(raw))
			return decodeImage(img)
		}(); err == nil {
			decoded++
			if got != payload {
				t.Fatalf("n=%d: decoded %q", n, got)
			}
		}
	}
	if bumped == 0 {
		t.Error("no payload exercised the version bump for a centered alignment pattern")
	}
	if decoded < 5 {
		t.Errorf("only %d symbols decoded", decoded)
	}
}

func TestLogoDefaultsToHigh(t *testing.T) {
	q, err := New("x", WithLogo(testLogoPNG(t, 20, 20)))
	if err != nil {
		t.Fatal(err)
	}
	if q.ErrorCorrection() != High {
		t.Errorf("level %v", q.ErrorCorrection())
	}
	q, _ = New("x", WithLogo(testLogoPNG(t, 20, 20)), WithErrorCorrection(Low))
	if q.ErrorCorrection() != Low {
		t.Errorf("explicit level overridden: %v", q.ErrorCorrection())
	}
	// A payload that fits at M but not at H is an error, never a silent downgrade.
	big := strings.Repeat("z", 1400)
	if _, err := New(big, WithLogo(testLogoPNG(t, 20, 20))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestLogoDataIsCopied(t *testing.T) {
	data := testLogoPNG(t, 30, 30)
	q, err := New("copy", WithLogo(data))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := q.SVG()
	for i := range data {
		data[i] = 0
	}
	after, _ := q.SVG()
	if !bytes.Equal(before, after) {
		t.Fatal("mutating the caller's logo bytes changed the output")
	}
}

func TestLogoShapes(t *testing.T) {
	for name, dims := range map[string][2]int{"wide": {200, 20}, "tall": {20, 200}, "tiny": {1, 1}, "square": {64, 64}} {
		q, err := New("https://example.com/shape", WithLogo(testLogoPNG(t, dims[0], dims[1])), WithSize(512))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		l := q.logo
		if float64(l.box.w)/float64(l.box.h) < 0 {
			t.Fatal("bad box")
		}
		// Aspect ratio preserved within a pixel of rounding.
		want := float64(dims[0]) / float64(dims[1])
		got := float64(l.box.w) / float64(l.box.h)
		if d := got/want - 1; d > 0.2 || d < -0.2 {
			if l.box.h > 2 && l.box.w > 2 {
				t.Errorf("%s: aspect %.2f, want %.2f", name, got, want)
			}
		}
		if l.box.w < 1 || l.box.h < 1 {
			t.Errorf("%s: empty box", name)
		}
		if _, err := q.PNG(); err != nil {
			t.Fatal(err)
		}
		if _, err := q.SVG(); err != nil {
			t.Fatal(err)
		}
	}
}

// --- SVG ---------------------------------------------------------------

// checkSVG parses the document as XML and asserts it carries nothing active
// or external. It returns the root attributes.
func checkSVG(t *testing.T, data []byte) map[string]string {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	root := map[string]string{}
	first := true
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("SVG is not well-formed: %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "svg", "rect", "path", "g", "clipPath", "image":
		default:
			t.Errorf("unexpected element <%s>", se.Name.Local)
		}
		for _, a := range se.Attr {
			n := strings.ToLower(a.Name.Local)
			if strings.HasPrefix(n, "on") && n != "overflow" {
				t.Errorf("event attribute %q", n)
			}
			v := strings.ToLower(a.Value)
			if (n == "href") && !strings.HasPrefix(v, "data:image/png;base64,") {
				t.Errorf("external reference %q", a.Value)
			}
			if strings.Contains(v, "javascript:") || (strings.Contains(v, "http") && a.Name.Space != "xmlns" && n != "xmlns:xlink" && n != "xmlns") {
				t.Errorf("suspicious attribute %s=%q", n, a.Value)
			}
		}
		if first {
			first = false
			for _, a := range se.Attr {
				root[a.Name.Local] = a.Value
			}
		}
	}
	s := strings.ToLower(string(data))
	for _, bad := range []string{"<script", "foreignobject", "<!doctype", "<!entity", "<style", "<use"} {
		if strings.Contains(s, bad) {
			t.Errorf("SVG contains %q", bad)
		}
	}
	return root
}

var squareRun = regexp.MustCompile(`M(\d+) (\d+)h(\d+)v(\d+)h-(\d+)z`)

func TestSVGStructureAndMatrix(t *testing.T) {
	q, _ := New("https://example.com/svg", WithSize(500))
	b, err := q.SVG()
	if err != nil {
		t.Fatal(err)
	}
	root := checkSVG(t, b)
	px := strconv.Itoa(q.Size())
	if root["width"] != px || root["height"] != px || root["viewBox"] != "0 0 "+px+" "+px {
		t.Errorf("dimensions %v", root)
	}
	if strings.Contains(string(b), "https://example.com/svg") {
		t.Error("the payload must not appear in the SVG")
	}
	// Rebuild the dark modules from the square-module path and compare them
	// with the matrix (finders are drawn as shapes and checked separately).
	n, s, off := q.sym.size, q.scale, q.margin*q.scale
	got := map[[2]int]bool{}
	sqPath := regexp.MustCompile(`shape-rendering="crispEdges" d="([^"]*)"`).FindStringSubmatch(string(b))
	if sqPath == nil {
		t.Fatal("no square-module path")
	}
	for _, m := range squareRun.FindAllStringSubmatch(sqPath[1], -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		w, _ := strconv.Atoi(m[3])
		for i := 0; i < w/s; i++ {
			got[[2]int{(x-off)/s + i, (y - off) / s}] = true
		}
	}
	want := 0
	for y := range n {
		for x := range n {
			if q.sym.at(x, y) && !inFinder(x, y, n) {
				want++
				if !got[[2]int{x, y}] {
					t.Fatalf("module (%d,%d) missing from the SVG", x, y)
				}
			}
		}
	}
	if len(got) != want {
		t.Errorf("SVG has %d square modules, matrix %d", len(got), want)
	}
}

func TestSVGRoundedAndLogo(t *testing.T) {
	for name, opts := range map[string][]Option{
		"rounded":     {WithRoundedModules(0.3), WithRoundedFinders(0.2)},
		"logo-png":    {WithLogo(testLogoPNG(t, 40, 40), LogoBorderRadius(0.2), LogoBackgroundRadius(0.1))},
		"logo-jpeg":   {WithLogo(testLogoJPEG(t))},
		"logo-svg":    {WithLogo([]byte(testLogoSVG), LogoBorderRadius(0.15))},
		"logo-noplat": {WithLogo([]byte(testLogoSVG), LogoBackground(false))},
	} {
		q, err := New("https://example.com/svg2", opts...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := q.SVG()
		checkSVG(t, b)
	}
	q, _ := New("x", WithLogo([]byte(testLogoSVG)))
	b, _ := q.SVG()
	if strings.Contains(string(b), "c81e1e") == false {
		t.Error("vector logo shapes missing from the SVG")
	}
}

func TestPNGAndSVGShareGeometry(t *testing.T) {
	q, _ := New("https://example.com/same", WithRoundedModules(0.3), WithRoundedFinders(0.2), WithLogo(testLogoPNG(t, 50, 30)))
	b, _ := q.SVG()
	root := checkSVG(t, b)
	if root["width"] != strconv.Itoa(q.px) {
		t.Fatal("SVG and PNG disagree about the size")
	}
	l := q.logo
	want := `x="` + strconv.Itoa(l.box.x) + `" y="` + strconv.Itoa(l.box.y) + `" width="` + strconv.Itoa(l.box.w) + `" height="` + strconv.Itoa(l.box.h) + `"`
	if !strings.Contains(string(b), want) {
		t.Error("logo box differs between PNG and SVG")
	}
}

func TestTransparentColorsInSVG(t *testing.T) {
	q, _ := New("t", WithColors(color.NRGBA{0, 0, 0, 200}, color.NRGBA{255, 255, 255, 0}))
	b, _ := q.SVG()
	if !strings.Contains(string(b), `fill-opacity="0.784"`) {
		t.Error("foreground alpha lost")
	}
}

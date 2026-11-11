package qrcode

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"
	"testing"

	bqr "github.com/boombuler/barcode/qr"
)

// TestBlockTable checks the block table against the raw capacity formula of
// the standard, independently of where the table came from.
func TestBlockTable(t *testing.T) {
	for v := 1; v <= 40; v++ {
		raw := (16*v+128)*v + 64
		if v >= 2 {
			na := v/7 + 2
			raw -= (25*na-10)*na - 55
			if v >= 7 {
				raw -= 36
			}
		}
		total := raw / 8
		for l := range 4 {
			bi := blockTable[v-1][l]
			got := (bi.blocks1+bi.blocks2)*bi.ecPerBlock + bi.blocks1*bi.data1 + bi.blocks2*bi.data2
			if got != total {
				t.Errorf("v%d level %d: %d codewords, want %d", v, l, got, total)
			}
			if bi.blocks2 > 0 && bi.data2 != bi.data1+1 {
				t.Errorf("v%d level %d: group 2 size %d, group 1 %d", v, l, bi.data2, bi.data1)
			}
		}
	}
}

// TestEncoderAllVersions decodes, with an independent decoder, a payload that
// just fills each version at each level, so every block layout, alignment
// grid, version-information block and mask is exercised.
func TestEncoderAllVersions(t *testing.T) {
	for v := 1; v <= 40; v++ {
		for l := Low; l <= High; l++ {
			cb := countBits(modeByte, v)
			n := (dataCapacity(v, l)*8 - 4 - cb) / 8
			payload := make([]byte, n)
			for i := range payload {
				payload[i] = byte(i*31 + v*7 + int(l)) // includes every byte value over time
			}
			got, mode := minimalVersion(payload, l, 1)
			if pickMode(payload) != modeByte || got != v {
				// Random bytes can fall into byte mode only; a payload that fills
				// the version must not fit a smaller one.
				t.Fatalf("v%d %s: minimal version %d mode %d", v, levelName(l), got, mode)
			}
			sym, err := encodeSymbol(payload, modeByte, v, l)
			if err != nil {
				t.Fatal(err)
			}
			// Independent reference: the matrix must equal the one boombuler/barcode
			// builds for the same bytes, level and (minimal) version.
			ref, err := bqr.Encode(string(payload), [4]bqr.ErrorCorrectionLevel{bqr.L, bqr.M, bqr.Q, bqr.H}[l], bqr.Unicode)
			if err != nil {
				t.Fatalf("reference v%d %s: %v", v, levelName(l), err)
			}
			if ref.Bounds().Dx() != sym.size {
				t.Fatalf("v%d %s: reference size %d, ours %d", v, levelName(l), ref.Bounds().Dx(), sym.size)
			}
			for y := range sym.size {
				for x := range sym.size {
					if (ref.At(x, y) == color.Black) != sym.at(x, y) {
						t.Fatalf("v%d %s: module (%d,%d) differs from the reference", v, levelName(l), x, y)
					}
				}
			}
			if v == 1 || v == 7 || v == 14 || v == 21 || v == 28 || v == 35 { // decoding is slow on big symbols: sample
				// Letters, not the stress bytes above: the decoder (a port of ZXing)
				// occasionally misses a symbol whose modules happen to resemble a
				// finder, which says nothing about the encoder the reference checks.
				text := strings.Repeat("ab", n)[:n]
				s2, err := encodeSymbol([]byte(text), modeByte, v, l)
				if err != nil {
					t.Fatal(err)
				}
				got, err := decodeImage(matrixImage((&QRCode{sym: s2}).Modules(), 4))
				if err != nil || got != text {
					t.Fatalf("v%d %s: decode %v", v, levelName(l), err)
				}
			}
		}
	}
}

func TestPayloadModes(t *testing.T) {
	cases := map[string]string{
		"numeric":      "0123456789012345678901234567890",
		"numeric-1":    "7",
		"numeric-2":    "42",
		"alnum":        "HELLO WORLD $%*+-./: 123",
		"alnum-odd":    "ABC",
		"ascii":        "https://example.com/path?q=1&r=teste123",
		"lower":        "teste123",
		"unicode":      "São Paulo — ação ✓",
		"emoji":        "QR 😀 code",
		"single":       "a",
		"long-url":     "https://example.com/a/b?token=Zx9Qm2LkP7vRt4YhN8cWd3BfJ6sGu1EoAa5Ii0Xy-Tt_Vv.Hh~Kk&next=%2Fdashboard%2Fsettings&ref=campaign-2026-spring&utm=qr",
		"newline":      "line1\nline2",
		"pix-like":     "00020126580014br.gov.bcb.pix0136123e4567-e89b-12d3-a456-4266141740005204000053039865802BR5913Fulano de Tal6008BRASILIA62070503***63041D3D",
		"leading-zero": "000123",
	}
	for name, payload := range cases {
		for _, l := range []ErrorCorrection{Low, Medium, Quartile, High} {
			q, err := New(payload, WithErrorCorrection(l), WithSize(1024))
			if err != nil {
				t.Fatalf("%s %s: %v", name, levelName(l), err)
			}
			b, err := q.PNG()
			if err != nil {
				t.Fatal(err)
			}
			img, _ := png.Decode(bytes.NewReader(b))
			got, err := decodeImage(img)
			if err != nil || got != payload {
				t.Errorf("%s %s (v%d): got %q err %v", name, levelName(l), q.Version(), got, err)
			}
		}
	}
}

func TestBinaryPayload(t *testing.T) {
	data := []byte{0x00, 0x01, 0xFE, 0xFF, 'a', 0x80}
	q, err := NewBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 9 // the input is copied
	q2, _ := NewBytes([]byte{0x00, 0x01, 0xFE, 0xFF, 'a', 0x80})
	if !equalMatrix(q.Modules(), q2.Modules()) {
		t.Fatal("NewBytes did not copy its input")
	}
}

func equalMatrix(a, b [][]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for y := range a {
		for x := range a[y] {
			if a[y][x] != b[y][x] {
				return false
			}
		}
	}
	return true
}

func TestSizes(t *testing.T) {
	for _, size := range []int{256, 512, 1024} {
		q, err := New("https://example.com/size", WithSize(size))
		if err != nil {
			t.Fatal(err)
		}
		b, err := q.PNG()
		if err != nil {
			t.Fatal(err)
		}
		if q.Size() > size || q.Size() <= size-(q.sym.size+8) {
			t.Errorf("size %d gave %d", size, q.Size())
		}
		if q.Size()%(q.sym.size+8) != 0 {
			t.Errorf("size %d is not a whole number of pixels per module", q.Size())
		}
		if got := decodePNG(t, b); got != "https://example.com/size" {
			t.Errorf("size %d: %q", size, got)
		}
	}
}

func TestDefaultsAndDeterminism(t *testing.T) {
	a, err := New("teste123")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New("teste123")
	pa, _ := a.PNG()
	pb, _ := b.PNG()
	if !bytes.Equal(pa, pb) {
		t.Fatal("PNG output is not deterministic")
	}
	if a.ErrorCorrection() != Medium {
		t.Errorf("default level %v", a.ErrorCorrection())
	}
	if a.Size() < DefaultSize-(a.sym.size+8) {
		t.Errorf("default size %d", a.Size())
	}
}

func TestErrors(t *testing.T) {
	huge := strings.Repeat("x", 4000)
	cases := []struct {
		name    string
		payload string
		opts    []Option
		want    error
	}{
		{"empty", "", nil, ErrEmptyPayload},
		{"too large", huge, nil, ErrTooLarge},
		{"too large for level", strings.Repeat("x", 2000), []Option{WithErrorCorrection(High)}, ErrTooLarge},
		{"size 0", "a", []Option{WithSize(0)}, ErrInvalidOption},
		{"size big", "a", []Option{WithSize(MaxSize + 1)}, ErrInvalidOption},
		{"size too small", "a", []Option{WithSize(10)}, ErrInvalidOption},
		{"margin", "a", []Option{WithMargin(-1)}, ErrInvalidOption},
		{"level", "a", []Option{WithErrorCorrection(9)}, ErrInvalidOption},
		{"nan radius", "a", []Option{WithRoundedModules(math.NaN())}, ErrInvalidOption},
		{"inf radius", "a", []Option{WithRoundedFinders(math.Inf(1))}, ErrInvalidOption},
		{"neg radius", "a", []Option{WithRoundedModules(-0.1)}, ErrInvalidOption},
		{"big radius", "a", []Option{WithRoundedModules(0.51)}, ErrInvalidOption},
		{"big finder radius", "a", []Option{WithRoundedFinders(0.26)}, ErrInvalidOption},
		{"nil color", "a", []Option{WithColors(nil, color.White)}, ErrInvalidOption},
		{"same colors", "a", []Option{WithColors(color.Black, color.Black)}, ErrInvalidOption},
		{"low contrast", "a", []Option{WithColors(color.Gray{120}, color.Gray{140})}, ErrInvalidOption},
		{"nil option", "a", []Option{nil}, ErrInvalidOption},
		{"empty logo", "a", []Option{WithLogo(nil)}, ErrUnsupportedLogo},
		{"logo size", "a", []Option{WithLogo([]byte("x"), LogoSize(0.5))}, ErrInvalidOption},
		{"logo nan", "a", []Option{WithLogo([]byte("x"), LogoPadding(math.NaN()))}, ErrInvalidOption},
		{"logo plate transparent bg", "a", []Option{WithColors(color.Black, color.Transparent), WithLogo(testLogoPNG(t, 32, 32))}, ErrInvalidOption},
	}
	for _, tc := range cases {
		_, err := New(tc.payload, tc.opts...)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := New(strings.Repeat("x", maxPayloadBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the payload bound: %v", err)
	}
	q, _ := New("a")
	if _, err := q.Base64(Format(9)); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("unknown format: %v", err)
	}
}

func TestColorsAndAlpha(t *testing.T) {
	q, err := New("colors", WithColors(color.NRGBA{0, 60, 120, 255}, color.NRGBA{250, 250, 230, 255}), WithSize(512))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := q.PNG()
	if got := decodePNG(t, b); got != "colors" {
		t.Fatalf("got %q", got)
	}
	// A transparent background is accepted (composited over white by decodePNG).
	q, err = New("alpha", WithColors(color.Black, color.NRGBA{0, 0, 0, 0}))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = q.PNG()
	if got := decodePNG(t, b); got != "alpha" {
		t.Fatalf("got %q", got)
	}
	s, _ := q.SVG()
	if strings.Contains(string(s), "<rect") {
		t.Error("transparent background should not emit a rect")
	}
}

func TestMarginZero(t *testing.T) {
	q, err := New("m", WithMargin(0))
	if err != nil {
		t.Fatal(err)
	}
	if q.Size()%q.sym.size != 0 {
		t.Errorf("size %d for %d modules", q.Size(), q.sym.size)
	}
}

func TestBase64AndDataURI(t *testing.T) {
	q, _ := New("round trip", WithRoundedModules(0.3))
	for _, f := range []Format{FormatPNG, FormatSVG} {
		raw, _ := q.render(f)
		b64, err := q.Base64(f)
		if err != nil {
			t.Fatal(err)
		}
		back, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || !bytes.Equal(back, raw) {
			t.Errorf("format %d: Base64 does not round-trip", f)
		}
		uri, _ := q.DataURI(f)
		prefix := map[Format]string{FormatPNG: "data:image/png;base64,", FormatSVG: "data:image/svg+xml;base64,"}[f]
		if uri != prefix+b64 {
			t.Errorf("format %d: data URI %q", f, uri[:40])
		}
	}
}

func TestFunctionMaskGeometry(t *testing.T) {
	for _, v := range []int{1, 2, 7, 14, 32, 40} {
		fn := functionMask(v)
		n := 17 + 4*v
		count := 0
		for _, f := range fn {
			if f {
				count++
			}
		}
		// Data modules = raw modules − function modules, which must equal the
		// codeword capacity in bits plus the remainder bits.
		raw := (16*v+128)*v + 64
		if v >= 2 {
			na := v/7 + 2
			raw -= (25*na-10)*na - 55
			if v >= 7 {
				raw -= 36
			}
		}
		if n*n-count != raw {
			t.Errorf("v%d: %d data modules, want %d", v, n*n-count, raw)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	q, err := New("concurrent", WithRoundedModules(0.3), WithRoundedFinders(0.2), WithLogo(testLogoPNG(t, 40, 40)))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := q.PNG()
	wantSVG, _ := q.SVG()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := q.PNG()
			s, err2 := q.SVG()
			_ = q.Modules()
			if err != nil || err2 != nil || !bytes.Equal(p, want) || !bytes.Equal(s, wantSVG) {
				t.Error("concurrent render differs")
			}
		}()
	}
	wg.Wait()
}

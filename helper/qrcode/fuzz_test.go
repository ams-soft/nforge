package qrcode

import (
	"bytes"
	"errors"
	"image/png"
	"testing"
	"unicode/utf8"
)

// FuzzPayload feeds arbitrary payloads and option values. Invariants: no
// panic, errors are the documented sentinels, both renderers succeed, the SVG
// is well-formed and inert, and a short UTF-8 payload that the independent
// decoder can read comes back byte for byte.
func FuzzPayload(f *testing.F) {
	for _, s := range []string{"a", "teste123", "https://example.com", "0123456789", "HELLO WORLD", "São Paulo ✓", "\x00\xff"} {
		f.Add(s, uint8(1), uint16(256), uint8(0), uint8(0), uint8(4))
	}
	f.Fuzz(func(t *testing.T, payload string, level uint8, size uint16, rm, rf uint8, margin uint8) {
		q, err := New(payload,
			WithErrorCorrection(ErrorCorrection(level%4)),
			WithSize(int(size)%1200+1),
			WithMargin(int(margin)%12),
			WithRoundedModules(float64(rm%51)/100),
			WithRoundedFinders(float64(rf%21)/100),
		)
		if err != nil {
			if !errors.Is(err, ErrEmptyPayload) && !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrInvalidOption) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		raw, err := q.PNG()
		if err != nil {
			t.Fatal(err)
		}
		svg, err := q.SVG()
		if err != nil {
			t.Fatal(err)
		}
		checkSVG(t, svg)
		if len(payload) <= 40 && utf8.ValidString(payload) && q.scale >= 3 && q.margin >= 4 && rm%51 < 40 {
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			// A decoder miss is not an encoder failure (see TestEncoderAllVersions);
			// a wrong answer is.
			if got, err := decodeImage(img); err == nil && got != payload && !isNoisy(payload) {
				t.Fatalf("decoded %q from %q", got, payload)
			}
		}
	})
}

// isNoisy reports payloads whose decoded text ZXing may legitimately present
// differently (control characters, charset guessing).
func isNoisy(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			return true
		}
	}
	return false
}

// FuzzLogo feeds arbitrary logo bytes (PNG, JPEG, SVG and junk).
func FuzzLogo(f *testing.F) {
	f.Add(testLogoPNG(f, 16, 16))
	f.Add(testLogoJPEG(f))
	f.Add([]byte(testLogoSVG))
	f.Add([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script>x</script></svg>`))
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		q, err := New("fuzz", WithLogo(data), WithSize(256))
		if err != nil {
			if !errors.Is(err, ErrUnsupportedLogo) && !errors.Is(err, ErrLogoOverlap) && !errors.Is(err, ErrInvalidOption) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		if _, err := q.PNG(); err != nil {
			t.Fatal(err)
		}
		svg, err := q.SVG()
		if err != nil {
			t.Fatal(err)
		}
		checkSVG(t, svg)
	})
}

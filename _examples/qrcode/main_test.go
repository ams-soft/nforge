package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	zxqr "github.com/makiuchi-d/gozxing/qrcode"

	"github.com/ams-soft/nforge/helper/qrcode"
)

func decode(t *testing.T, img image.Image) string {
	t.Helper()
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := zxqr.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res.GetText()
}

// textImage turns the terminal rendering back into a bitmap (4x4 px per
// module, 8x16 px per character cell) so it can be decoded.
func textImage(t *testing.T, text string, invert bool) image.Image {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	w := len([]rune(lines[0]))
	img := image.NewGray(image.Rect(0, 0, w*4, len(lines)*8))
	for ly, line := range lines {
		rs := []rune(line)
		if len(rs) != w {
			t.Fatalf("line %d has %d columns, want %d", ly, len(rs), w)
		}
		for x, r := range rs {
			top, bottom := false, false // glyph present
			switch r {
			case '█':
				top, bottom = true, true
			case '▀':
				top = true
			case '▄':
				bottom = true
			case ' ':
			default:
				t.Fatalf("unexpected rune %q", r)
			}
			for half, glyph := range []bool{top, bottom} {
				dark := glyph == invert // glyph = light unless inverted
				v := uint8(255)
				if dark {
					v = 0
				}
				for dy := range 4 {
					for dx := range 4 {
						img.SetGray(x*4+dx, ly*8+half*4+dy, color.Gray{v})
					}
				}
			}
		}
	}
	return img
}

func TestRenderTerminalGeometry(t *testing.T) {
	q, err := qrcode.New("teste123")
	if err != nil {
		t.Fatal(err)
	}
	m := q.Modules()
	out := renderTerminal(m, false)
	if strings.Contains(out, "\x1b") {
		t.Fatal("output must not need ANSI escapes")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	side := len(m) + 2*quietModules
	if want := (side + 1) / 2; len(lines) != want {
		t.Errorf("%d lines, want %d", len(lines), want)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n != side {
			t.Fatalf("line %d: %d columns, want %d", i, n, side)
		}
	}
	// Quiet zone: with the default (glyph = light module) the first 2 lines
	// (4 module rows) are solid and so are the 4 edge columns.
	for _, l := range lines[:quietModules/2] {
		if strings.Trim(l, "█") != "" {
			t.Errorf("top quiet zone is not solid: %q", l)
		}
	}
	for _, l := range lines {
		r := []rune(l)
		if strings.Trim(string(r[:quietModules]), "█") != "" || strings.Trim(string(r[side-quietModules:]), "█") != "" {
			t.Fatalf("side quiet zone is not solid: %q", l)
		}
	}
	// Inversion is the exact complement.
	inv := renderTerminal(m, true)
	swap := strings.NewReplacer("█", " ", " ", "█", "▀", "▄", "▄", "▀")
	if swap.Replace(out) != inv {
		t.Error("invert is not the complement of the default rendering")
	}
}

func TestRenderTerminalDecodes(t *testing.T) {
	for _, payload := range []string{"teste123", "https://example.com", "São Paulo ✓", "x"} {
		q, err := qrcode.New(payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, invert := range []bool{false, true} {
			got := decode(t, textImage(t, renderTerminal(q.Modules(), invert), invert))
			if got != payload {
				t.Errorf("payload %q invert=%v: decoded %q", payload, invert, got)
			}
		}
	}
}

func chdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func TestTerminalOnlyDefaultPayload(t *testing.T) {
	dir := chdir(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-terminal-only"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr: %s", stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "wrote") || strings.Contains(out, "teste123") {
		t.Errorf("terminal-only output has more than the QR: %q", out[:min(len(out), 80)])
	}
	if got := decode(t, textImage(t, out, false)); got != "teste123" {
		t.Errorf("decoded %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("terminal-only created files: %v", entries)
	}
}

func TestTerminalOnlyCustomPayload(t *testing.T) {
	chdir(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-terminal-only", "-payload", "https://example.com"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := decode(t, textImage(t, stdout.String(), false)); got != "https://example.com" {
		t.Errorf("decoded %q", got)
	}
}

func TestExplicitEmptyPayloadIsRejected(t *testing.T) {
	chdir(t)
	for _, args := range [][]string{{"-terminal-only", "-payload", ""}, {"-payload", ""}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 {
			t.Errorf("%v: exit %d", args, code)
		}
		if !strings.Contains(stderr.String(), "empty payload") {
			t.Errorf("%v: stderr %q", args, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("%v: wrote to stdout: %q", args, stdout.String())
		}
	}
}

func TestFlagErrors(t *testing.T) {
	chdir(t)
	for _, args := range [][]string{{"-terminal-only", "-terminal=false"}, {"-bogus"}, {"extra"}, {"-size", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("%v: exit 0", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-terminal=false", "-size", "5"}, &stdout, &stderr); code != 1 || stderr.Len() == 0 {
		t.Errorf("tiny size: exit %d stderr %q", code, stderr.String())
	}
}

func TestGallery(t *testing.T) {
	dir := chdir(t)
	out := filepath.Join(dir, "gal")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-terminal=false", "-out", out, "-size", "512"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, name := range []string{"01-traditional", "02-rounded-modules", "03-rounded-finders", "04-rounded-both", "05-logo", "06-logo-rounded", "07-logo-svg", "08-colors"} {
		raw, err := os.ReadFile(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := decode(t, img); got != "teste123" {
			t.Errorf("%s: decoded %q", name, got)
		}
		svg, err := os.ReadFile(filepath.Join(out, name+".svg"))
		if err != nil || !bytes.HasPrefix(svg, []byte("<svg ")) {
			t.Errorf("%s.svg: %v", name, err)
		}
	}
	for _, name := range []string{"base64.txt", "datauri.txt", "index.html"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Error(err)
		}
	}
	index, _ := os.ReadFile(filepath.Join(out, "index.html"))
	s := strings.ToLower(string(index))
	if strings.Contains(s, "<script") || strings.Contains(s, "http://") || strings.Contains(s, "https://") {
		t.Error("index.html must be static and local")
	}
	if !strings.Contains(stdout.String(), "index.html") {
		t.Error("the output does not point to index.html")
	}
}

func TestGalleryWithLogoFile(t *testing.T) {
	dir := chdir(t)
	logo, err := assets.ReadFile("assets/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	lp := filepath.Join(dir, "my-logo.svg")
	if err := os.WriteFile(lp, logo, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-terminal=false", "-out", filepath.Join(dir, "g"), "-logo", lp, "-payload", "https://example.com/p"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"-terminal=false", "-out", filepath.Join(dir, "g2"), "-logo", filepath.Join(dir, "missing.png")}, &stdout, &stderr); code != 1 {
		t.Errorf("missing logo: exit %d", code)
	}
}

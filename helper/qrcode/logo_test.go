package qrcode

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

func svgWrap(body string) []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` + body + `</svg>`)
}

func TestHostileSVGLogosAreRejected(t *testing.T) {
	cases := map[string][]byte{
		"script":         svgWrap(`<script>alert(1)</script><rect width="5" height="5"/>`),
		"onload":         []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" onload="alert(1)"><rect width="5" height="5"/></svg>`),
		"onclick-shape":  svgWrap(`<rect width="5" height="5" onclick="x()"/>`),
		"foreignObject":  svgWrap(`<foreignObject width="5" height="5"><div>hi</div></foreignObject>`),
		"image-remote":   svgWrap(`<image href="http://evil.example/x.png" width="5" height="5"/>`),
		"image-file":     svgWrap(`<image href="file:///etc/passwd" width="5" height="5"/>`),
		"use":            svgWrap(`<use href="#a"/>`),
		"style-elem":     svgWrap(`<style>rect{fill:url(http://evil.example)}</style><rect width="5" height="5"/>`),
		"style-attr":     svgWrap(`<rect width="5" height="5" style="fill:red"/>`),
		"class":          svgWrap(`<rect width="5" height="5" class="a"/>`),
		"fill-url":       svgWrap(`<rect width="5" height="5" fill="url(#g)"/>`),
		"fill-remote":    svgWrap(`<rect width="5" height="5" fill="url(http://evil.example/x)"/>`),
		"stroke":         svgWrap(`<rect width="5" height="5" stroke="red"/>`),
		"transform":      svgWrap(`<rect width="5" height="5" transform="scale(100)"/>`),
		"text":           svgWrap(`<text>hello</text>`),
		"anim":           svgWrap(`<rect width="5" height="5"><animate attributeName="x" to="1"/></rect>`),
		"nested-svg":     svgWrap(`<svg><rect width="5" height="5"/></svg>`),
		"doctype":        []byte(`<!DOCTYPE svg [<!ENTITY x "boom">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="5" height="5"/></svg>`),
		"external-ent":   []byte(`<!DOCTYPE svg SYSTEM "http://evil.example/x.dtd"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="5" height="5"/></svg>`),
		"proc-inst":      []byte(`<?xml-stylesheet href="http://evil.example/s.css"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="5" height="5"/></svg>`),
		"xlink-prefix":   []byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10"><rect width="5" height="5"/></svg>`),
		"foreign-ns":     []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><x:div xmlns:x="http://www.w3.org/1999/xhtml"/></svg>`),
		"not-svg-root":   []byte(`<html><body><rect/></body></html>`),
		"two-roots":      []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="5" height="5"/></svg><svg/>`),
		"arc":            svgWrap(`<path d="M0 0 A5 5 0 0 1 10 10"/>`),
		"no-moveto":      svgWrap(`<path d="L5 5"/>`),
		"bad-number":     svgWrap(`<path d="M0 0 L NaN 5"/>`),
		"inf":            svgWrap(`<path d="M0 0 L 1e999 5"/>`),
		"huge-coord":     svgWrap(`<path d="M0 0 L 9999999 5 Z"/>`),
		"no-viewbox":     []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="5" height="5"/></svg>`),
		"zero-viewbox":   []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 0 0"><rect width="5" height="5"/></svg>`),
		"bad-opacity":    svgWrap(`<rect width="5" height="5" opacity="9"/>`),
		"text-content":   []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">boo<rect width="5" height="5"/></svg>`),
		"empty":          svgWrap(``),
		"truncated":      []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="5"`),
		"odd-polygon":    svgWrap(`<polygon points="1,2,3"/>`),
		"billion-laughs": []byte(`<!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="5" height="5" id="&lol2;"/></svg>`),
	}
	for name, data := range cases {
		_, err := New("logo", WithLogo(data))
		if !errors.Is(err, ErrUnsupportedLogo) {
			t.Errorf("%s: err = %v, want ErrUnsupportedLogo", name, err)
		}
	}
}

func TestSVGLogoSubsetAccepted(t *testing.T) {
	ok := map[string][]byte{
		"shapes":   []byte(testLogoSVG),
		"xml-decl": []byte(`<?xml version="1.0" encoding="UTF-8"?><!-- note --><svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><g fill="red" opacity="0.5"><ellipse cx="20" cy="20" rx="10" ry="15"/></g></svg>`),
		"curves":   svgWrap(`<path d="M1 1 C 2 0, 4 0, 5 1 S 8 3 9 1 q1 2 2 0 t2 0 h-3 v2 H1 V5 z" fill="#0af"/>`),
		"polygon":  svgWrap(`<polygon points="1,1 9,1 5,9" fill="#fa0"/>`),
		"bom":      append([]byte("\xef\xbb\xbf"), svgWrap(`<rect width="4" height="4"/>`)...),
		"rel-move": svgWrap(`<path d="m1 1 l3 0 0 3 -3 0z m5 5 l2 0 0 2 -2 0z"/>`),
	}
	for name, data := range ok {
		q, err := New("logo", WithLogo(data))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		s, _ := q.SVG()
		checkSVG(t, s)
		if _, err := q.PNG(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSVGLogoOutputNeverContainsInput(t *testing.T) {
	// id attributes are accepted and dropped; nothing from the source text may
	// reach the output besides numbers and colors rebuilt from the model.
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" id="SECRET-ID" viewBox="0 0 10 10"><!-- SECRET-COMMENT --><rect id="x-SECRET" width="5" height="5"/></svg>`)
	q, err := New("logo", WithLogo(src))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := q.SVG()
	if strings.Contains(string(s), "SECRET") {
		t.Error("source text leaked into the output")
	}
}

func TestRasterLogoRejections(t *testing.T) {
	good := testLogoPNG(t, 32, 32)
	// A valid 5000x1 PNG: tiny file, dimensions over the limit.
	wide := image.NewNRGBA(image.Rect(0, 0, 5000, 1))
	var wideBuf bytes.Buffer
	if err := png.Encode(&wideBuf, wide); err != nil {
		t.Fatal(err)
	}
	// Too many pixels while each side is within the limit (4096x4096 = 16M).
	area := image.NewGray(image.Rect(0, 0, 4096, 4096))
	var areaBuf bytes.Buffer
	if err := png.Encode(&areaBuf, area); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"truncated-png": good[:len(good)/2],
		"png-header":    good[:12],
		"fake-png":      []byte("\x89PNG\r\n\x1a\nnot really"),
		"fake-jpeg":     {0xFF, 0xD8, 0xFF, 0xE0, 0, 1},
		"gif":           []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"text":          []byte("just some text"),
		"zeros":         make([]byte, 64),
		"too-wide":      wideBuf.Bytes(),
		"too-many-px":   areaBuf.Bytes(),
		"oversized":     append(append([]byte{}, good...), make([]byte, maxLogoBytes)...),
		"bmp":           []byte("BM\x00\x00"),
		"html":          []byte("<html><script>alert(1)</script></html>"),
	}
	for name, data := range cases {
		_, err := New("logo", WithLogo(data))
		if !errors.Is(err, ErrUnsupportedLogo) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestLargeRasterLogoIsDownscaled(t *testing.T) {
	big := testLogoPNG(t, 2000, 1000)
	q, err := New("https://example.com/big", WithLogo(big))
	if err != nil {
		t.Fatal(err)
	}
	b := q.logo.img.raster.Bounds()
	if max(b.Dx(), b.Dy()) > embedLogoMaxSide {
		t.Errorf("stored logo is %v", b)
	}
	if aspect := q.logo.img.aspect; aspect < 1.99 || aspect > 2.01 {
		t.Errorf("aspect %v", aspect)
	}
	s, _ := q.SVG()
	if len(s) > 400<<10 {
		t.Errorf("SVG is %d bytes", len(s))
	}
}

func TestSVGComplexityLimits(t *testing.T) {
	var many strings.Builder
	for range maxSVGElements + 1 {
		many.WriteString(`<rect width="1" height="1"/>`)
	}
	if _, err := New("x", WithLogo(svgWrap(many.String()))); !errors.Is(err, ErrUnsupportedLogo) {
		t.Errorf("many elements: %v", err)
	}
	deep := strings.Repeat("<g>", maxSVGDepth+2) + `<rect width="1" height="1"/>` + strings.Repeat("</g>", maxSVGDepth+2)
	if _, err := New("x", WithLogo(svgWrap(deep))); !errors.Is(err, ErrUnsupportedLogo) {
		t.Errorf("deep nesting: %v", err)
	}
	path := `<path d="M0 0` + strings.Repeat(" L1 1", maxSVGSegments+1) + `"/>`
	if _, err := New("x", WithLogo(svgWrap(path))); !errors.Is(err, ErrUnsupportedLogo) {
		t.Errorf("many segments: %v", err)
	}
}

func TestLogoErrorsDoNotEchoInput(t *testing.T) {
	_, err := New("x", WithLogo(svgWrap(`<rect width="5" height="5" onclick="LEAK-ME()"/>`)))
	if err == nil || strings.Contains(err.Error(), "LEAK-ME") {
		t.Errorf("err = %v", err)
	}
}

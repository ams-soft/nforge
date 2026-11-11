package qrcode

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// inFinder reports whether module (x, y) belongs to one of the three 7x7
// position markers. Their separators are function modules too, but they are
// light and need no drawing.
func inFinder(x, y, n int) bool {
	return (x < 7 && y < 7) || (x >= n-7 && y < 7) || (x < 7 && y >= n-7)
}

func finderOrigins(n int) [3][2]int { return [3][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} }

// --- PNG ---------------------------------------------------------------

func blend(img *image.RGBA, x, y int, c color.NRGBA, cov float64) {
	a := float64(c.A) / 255 * cov
	if a <= 0 {
		return
	}
	p := img.Pix[img.PixOffset(x, y):]
	inv := 1 - a
	p[0] = uint8(float64(c.R)*a + float64(p[0])*inv + 0.5)
	p[1] = uint8(float64(c.G)*a + float64(p[1])*inv + 0.5)
	p[2] = uint8(float64(c.B)*a + float64(p[2])*inv + 0.5)
	p[3] = uint8(255*a + float64(p[3])*inv + 0.5)
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			blend(img, x, y, c, 1)
		}
	}
}

// sdfRRect is the signed distance, in pixels, from p to a rounded rectangle
// (negative inside).
func sdfRRect(px, py, x, y, w, h, r float64) float64 {
	hx, hy := w/2, h/2
	qx, qy := math.Abs(px-(x+hx))-(hx-r), math.Abs(py-(y+hy))-(hy-r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func cover(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }

// fillSDF paints c over bounds wherever sdf says the pixel is inside, with a
// one-pixel antialiased edge and no blur in the interior.
func fillSDF(img *image.RGBA, b rect, c color.NRGBA, sdf func(x, y float64) float64) {
	for y := max(b.y, 0); y < min(b.y1(), img.Rect.Dy()); y++ {
		for x := max(b.x, 0); x < min(b.x1(), img.Rect.Dx()); x++ {
			blend(img, x, y, c, cover(sdf(float64(x)+0.5, float64(y)+0.5)))
		}
	}
}

// PNG renders the code as a PNG image of Size() by Size() pixels, with a whole
// number of pixels per module.
func (q *QRCode) PNG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, q.px, q.px))
	fillRect(img, 0, 0, q.px, q.px, q.opt.bg)
	s, n, off := q.scale, q.sym.size, q.margin*q.scale
	fs := float64(s)

	rf := q.opt.finderRadius
	for _, o := range finderOrigins(n) {
		fx, fy := float64(off+o[0]*s), float64(off+o[1]*s)
		b := rect{off + o[0]*s, off + o[1]*s, 7 * s, 7 * s}
		fillSDF(img, b, q.opt.fg, func(px, py float64) float64 {
			outer := sdfRRect(px, py, fx, fy, 7*fs, 7*fs, rf*7*fs)
			inner := sdfRRect(px, py, fx+fs, fy+fs, 5*fs, 5*fs, rf*5*fs)
			core := sdfRRect(px, py, fx+2*fs, fy+2*fs, 3*fs, 3*fs, rf*3*fs)
			return math.Min(math.Max(outer, -inner), core)
		})
	}

	rm := q.opt.moduleRadius
	for y := range n {
		for x := range n {
			if !q.sym.at(x, y) || inFinder(x, y, n) {
				continue
			}
			b := rect{off + x*s, off + y*s, s, s}
			if q.sym.fn[y*n+x] || rm == 0 {
				fillRect(img, b.x, b.y, b.x1(), b.y1(), q.opt.fg)
				continue
			}
			bx, by := float64(b.x), float64(b.y)
			fillSDF(img, b, q.opt.fg, func(px, py float64) float64 {
				return sdfRRect(px, py, bx, by, fs, fs, rm*fs)
			})
		}
	}

	if l := q.logo; l != nil {
		if l.opts.background {
			p := l.plate
			fillSDF(img, p, q.opt.bg, func(px, py float64) float64 {
				return sdfRRect(px, py, float64(p.x), float64(p.y), float64(p.w), float64(p.h), l.pRad)
			})
		}
		var tile *image.NRGBA
		if l.img.raster != nil {
			tile = image.NewNRGBA(image.Rect(0, 0, l.box.w, l.box.h))
			xdraw.CatmullRom.Scale(tile, tile.Bounds(), l.img.raster, l.img.raster.Bounds(), xdraw.Src, nil)
		} else {
			tile = l.img.vec.rasterize(l.box.w, l.box.h)
		}
		for ty := range l.box.h {
			for tx := range l.box.w {
				c := tile.NRGBAAt(tx, ty)
				cov := 1.0
				if l.radius > 0 {
					cov = cover(sdfRRect(float64(tx)+0.5, float64(ty)+0.5, 0, 0, float64(l.box.w), float64(l.box.h), l.radius))
				}
				blend(img, l.box.x+tx, l.box.y+ty, c, cov)
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("qrcode: encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// --- SVG ---------------------------------------------------------------

func num(v float64) string {
	v = math.Round(v*1000) / 1000
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func hexColor(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func fillAttrs(c color.NRGBA) string {
	s := `fill="` + hexColor(c) + `"`
	if c.A < 255 {
		s += ` fill-opacity="` + num(float64(c.A)/255) + `"`
	}
	return s
}

// rrectPath is a closed rounded-rectangle subpath (arcs, no raster).
func rrectPath(x, y, w, h, r float64) string {
	if r <= 0 {
		return fmt.Sprintf("M%s %sh%sv%sh-%sz", num(x), num(y), num(w), num(h), num(w))
	}
	r = math.Min(r, math.Min(w, h)/2)
	var b strings.Builder
	fmt.Fprintf(&b, "M%s %s", num(x+r), num(y))
	fmt.Fprintf(&b, "h%s", num(w-2*r))
	fmt.Fprintf(&b, "a%s %s 0 0 1 %s %s", num(r), num(r), num(r), num(r))
	fmt.Fprintf(&b, "v%s", num(h-2*r))
	fmt.Fprintf(&b, "a%s %s 0 0 1 -%s %s", num(r), num(r), num(r), num(r))
	fmt.Fprintf(&b, "h-%s", num(w-2*r))
	fmt.Fprintf(&b, "a%s %s 0 0 1 -%s -%s", num(r), num(r), num(r), num(r))
	fmt.Fprintf(&b, "v-%s", num(h-2*r))
	fmt.Fprintf(&b, "a%s %s 0 0 1 %s -%s", num(r), num(r), num(r), num(r))
	b.WriteString("z")
	return b.String()
}

// SVG renders the code as a self-contained SVG document: no scripts, no
// external references, no payload text. The viewBox is in the same pixel
// units as the PNG, so both share geometry. Raster logos are embedded as
// PNG data URIs; SVG logos are rebuilt from their validated shapes.
func (q *QRCode) SVG() ([]byte, error) {
	s, n, off := q.scale, q.sym.size, q.margin*q.scale
	fs := float64(s)
	var b strings.Builder
	W := strconv.Itoa(q.px)
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="` + W + `" height="` + W + `" viewBox="0 0 ` + W + ` ` + W + `" role="img" aria-label="QR Code">`)
	if q.opt.bg.A > 0 {
		b.WriteString(`<rect width="` + W + `" height="` + W + `" ` + fillAttrs(q.opt.bg) + `/>`)
	}

	var d strings.Builder
	rf := q.opt.finderRadius
	for _, o := range finderOrigins(n) {
		fx, fy := float64(off+o[0]*s), float64(off+o[1]*s)
		d.WriteString(rrectPath(fx, fy, 7*fs, 7*fs, rf*7*fs))
		d.WriteString(rrectPath(fx+fs, fy+fs, 5*fs, 5*fs, rf*5*fs))
		d.WriteString(rrectPath(fx+2*fs, fy+2*fs, 3*fs, 3*fs, rf*3*fs))
	}
	b.WriteString(`<path ` + fillAttrs(q.opt.fg) + ` fill-rule="evenodd" d="` + d.String() + `"/>`)

	rm := q.opt.moduleRadius
	var sq, rd strings.Builder
	for y := range n {
		for x := 0; x < n; x++ {
			if !q.sym.at(x, y) || inFinder(x, y, n) {
				continue
			}
			if q.sym.fn[y*n+x] || rm == 0 {
				run := 1
				for x+run < n && q.sym.at(x+run, y) && !inFinder(x+run, y, n) && (q.sym.fn[y*n+x+run] || rm == 0) {
					run++
				}
				fmt.Fprintf(&sq, "M%d %dh%dv%dh-%dz", off+x*s, off+y*s, run*s, s, run*s)
				x += run - 1
				continue
			}
			rd.WriteString(rrectPath(float64(off+x*s), float64(off+y*s), fs, fs, rm*fs))
		}
	}
	if sq.Len() > 0 {
		b.WriteString(`<path ` + fillAttrs(q.opt.fg) + ` shape-rendering="crispEdges" d="` + sq.String() + `"/>`)
	}
	if rd.Len() > 0 {
		b.WriteString(`<path ` + fillAttrs(q.opt.fg) + ` d="` + rd.String() + `"/>`)
	}

	if l := q.logo; l != nil {
		if l.opts.background {
			p := l.plate
			b.WriteString(`<path ` + fillAttrs(q.opt.bg) + ` d="` + rrectPath(float64(p.x), float64(p.y), float64(p.w), float64(p.h), l.pRad) + `"/>`)
		}
		bx := l.box
		open, closeTag := "", ""
		if l.radius > 0 {
			b.WriteString(`<clipPath id="qr-logo-clip"><path d="` + rrectPath(float64(bx.x), float64(bx.y), float64(bx.w), float64(bx.h), l.radius) + `"/></clipPath>`)
			open, closeTag = `<g clip-path="url(#qr-logo-clip)">`, `</g>`
		}
		b.WriteString(open)
		geo := `x="` + strconv.Itoa(bx.x) + `" y="` + strconv.Itoa(bx.y) + `" width="` + strconv.Itoa(bx.w) + `" height="` + strconv.Itoa(bx.h) + `"`
		if l.img.raster != nil {
			b.WriteString(`<image ` + geo + ` preserveAspectRatio="none" xlink:href="data:image/png;base64,` + l.img.pngB64 + `"/>`)
		} else {
			v := l.img.vec
			b.WriteString(`<svg ` + geo + ` viewBox="0 0 ` + num(v.w) + ` ` + num(v.h) + `" preserveAspectRatio="none" overflow="hidden">`)
			for _, sh := range v.shapes {
				b.WriteString(`<path ` + fillAttrs(sh.fill) + ` d="` + sh.pathData(v.vx, v.vy) + `"/>`)
			}
			b.WriteString(`</svg>`)
		}
		b.WriteString(closeTag)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String()), nil
}

// Base64 returns the standard Base64 (RFC 4648, padded) of the rendered image
// of the given format: the encoding of the file bytes, not of the matrix.
func (q *QRCode) Base64(format Format) (string, error) {
	raw, err := q.render(format)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DataURI returns the rendered image as a data: URI, ready for an img src:
// data:image/png;base64,... or data:image/svg+xml;base64,...
func (q *QRCode) DataURI(format Format) (string, error) {
	b64, err := q.Base64(format)
	if err != nil {
		return "", err
	}
	if format == FormatSVG {
		return "data:image/svg+xml;base64," + b64, nil
	}
	return "data:image/png;base64," + b64, nil
}

func (q *QRCode) render(format Format) ([]byte, error) {
	switch format {
	case FormatPNG:
		return q.PNG()
	case FormatSVG:
		return q.SVG()
	}
	return nil, fmt.Errorf("%w: %d", ErrUnknownFormat, format)
}

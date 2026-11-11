// Package qrcode encodes a payload as a QR Code and renders it as PNG or SVG,
// with optional rounded modules and finder patterns and a centered logo.
//
// It is a standalone helper: no configuration, no module wiring, no network
// and no files. Logos arrive as bytes and results leave as bytes or strings;
// persisting them is the caller's business.
//
// The encoder is a self-contained implementation of the QR Code Model 2
// symbology (ISO/IEC 18004): numeric, alphanumeric and byte modes, versions
// 1-40, levels L/M/Q/H and the eight masks. It does not claim certification.
// Byte mode carries UTF-8 text without an ECI segment, the de facto
// convention, and mixed-mode segmentation is not attempted, so a symbol can be
// one version larger than the theoretical minimum for mixed content.
//
// A QRCode is immutable after New and safe for concurrent use. Rendering is
// recomputed on every call, which is cheap next to a PNG encode; cache the
// bytes if you serve the same code repeatedly.
//
// Legibility comes first. Only data modules are rounded: finders are redrawn
// with their exact proportions, and timing, alignment, format and version
// modules stay square. A logo is never drawn over a function pattern; when the
// centered logo would touch one the symbol moves to the next larger version.
// Styling can reduce scanner compatibility, and no result is guaranteed to
// scan on every device: test the codes you publish.
package qrcode

import (
	"errors"
	"fmt"
	"image/color"
	"math"
)

// Sentinel errors, matched with errors.Is.
var (
	// ErrInvalidOption reports an invalid or conflicting option.
	ErrInvalidOption = errors.New("qrcode: invalid option")
	// ErrEmptyPayload reports an empty payload.
	ErrEmptyPayload = errors.New("qrcode: empty payload")
	// ErrTooLarge reports a payload that does not fit any QR Code version at
	// the requested error correction level.
	ErrTooLarge = errors.New("qrcode: payload too large")
	// ErrUnsupportedLogo reports a logo that is invalid, over a limit, or
	// outside the supported formats and SVG subset.
	ErrUnsupportedLogo = errors.New("qrcode: unsupported logo")
	// ErrLogoOverlap reports a logo that cannot be placed without covering a
	// function pattern of any version able to carry the payload.
	ErrLogoOverlap = errors.New("qrcode: logo overlaps a function pattern")
	// ErrUnknownFormat reports a Format other than FormatPNG or FormatSVG.
	ErrUnknownFormat = errors.New("qrcode: unknown format")
)

// Format selects the image format of Base64 and DataURI.
type Format uint8

// Output formats.
const (
	FormatPNG Format = iota
	FormatSVG
)

// ErrorCorrection is a QR Code error correction level.
type ErrorCorrection uint8

// Error correction levels, with the approximate share of codewords that can be
// recovered under ideal conditions. A level is not a share of image area.
const (
	Low      ErrorCorrection = iota // about 7%
	Medium                          // about 15%
	Quartile                        // about 25%
	High                            // about 30%
)

// Limits and defaults.
const (
	// DefaultSize is the PNG/SVG side in pixels when WithSize is not used.
	DefaultSize = 512
	// MaxSize is the largest accepted WithSize.
	MaxSize = 4096
	// DefaultMargin is the quiet zone in modules; the standard asks for 4.
	DefaultMargin = 4
	// MaxMargin is the largest accepted WithMargin.
	MaxMargin = 32
	// DefaultLogoSize is the logo's longest side as a fraction of the module
	// area width.
	DefaultLogoSize = 0.15
	// MaxLogoSize is the largest accepted LogoSize.
	MaxLogoSize = 0.20
	// MaxFinderRadius is the largest accepted WithRoundedFinders radius.
	MaxFinderRadius = 0.2
	// MaxLogoPadding is the largest accepted LogoPadding, in modules.
	MaxLogoPadding = 4

	maxPayloadBytes = 8192 // bounds work before the capacity check
)

type options struct {
	size         int
	margin       int
	level        ErrorCorrection
	levelSet     bool
	fg, bg       color.NRGBA
	moduleRadius float64
	finderRadius float64
	logoData     []byte
	logo         *logoOptions
}

type logoOptions struct {
	size       float64
	background bool
	padding    float64
	radius     float64
	bgRadius   float64
}

// Option configures New.
type Option func(*options) error

// LogoOption configures WithLogo.
type LogoOption func(*logoOptions) error

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOption, fmt.Sprintf(format, args...))
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// WithSize sets the side of the image in pixels (default DefaultSize, at most
// MaxSize). The PNG uses a whole number of pixels per module, so its side is
// the largest multiple of (modules + 2*margin) not above pixels; the SVG
// reports the same width and height. A size smaller than one pixel per module
// is an error.
func WithSize(pixels int) Option {
	return func(o *options) error {
		if pixels < 1 || pixels > MaxSize {
			return invalid("size must be between 1 and %d", MaxSize)
		}
		o.size = pixels
		return nil
	}
}

// WithMargin sets the quiet zone in modules (default DefaultMargin, at most
// MaxMargin). Fewer than 4 modules is accepted because the caller asked for it,
// but the standard requires 4 and many scanners fail without it.
func WithMargin(modules int) Option {
	return func(o *options) error {
		if modules < 0 || modules > MaxMargin {
			return invalid("margin must be between 0 and %d modules", MaxMargin)
		}
		o.margin = modules
		return nil
	}
}

// WithErrorCorrection fixes the error correction level. Without it the level
// is Medium, or High when a logo is set. A fixed level is never lowered: a
// payload that does not fit returns ErrTooLarge.
func WithErrorCorrection(level ErrorCorrection) Option {
	return func(o *options) error {
		if level > High {
			return invalid("unknown error correction level %d", level)
		}
		o.level, o.levelSet = level, true
		return nil
	}
}

// WithColors sets the module and background colors (default black on white).
// Alpha is supported; a transparent or translucent code is only as legible as
// whatever it is placed over, which the helper cannot know. Two opaque colors
// with a contrast ratio below 3:1 are rejected.
func WithColors(foreground, background color.Color) Option {
	return func(o *options) error {
		if foreground == nil || background == nil {
			return invalid("colors must not be nil")
		}
		o.fg = color.NRGBAModel.Convert(foreground).(color.NRGBA)
		o.bg = color.NRGBAModel.Convert(background).(color.NRGBA)
		return nil
	}
}

// WithRoundedModules rounds the corners of data and error correction modules.
// radius is a fraction of the module side: 0 is square (the default), 0.5 is a
// circle. Function patterns other than the finders stay square.
func WithRoundedModules(radius float64) Option {
	return func(o *options) error {
		if !finite(radius) || radius < 0 || radius > 0.5 {
			return invalid("module radius must be between 0 and 0.5")
		}
		o.moduleRadius = radius
		return nil
	}
}

// WithRoundedFinders rounds the three position markers. radius is a fraction
// of the 7-module finder side: 0 is square (the default) and 0.2, the
// maximum, rounds the outer corners by 1.4 modules. The cap keeps the center
// of every finder module dark, so the 1:1:3:1:1 ratio along any scan line
// through the finder is intact. The ring, its light gap and the 3x3 core keep
// their proportions: each is rounded in proportion to its own side.
func WithRoundedFinders(radius float64) Option {
	return func(o *options) error {
		if !finite(radius) || radius < 0 || radius > MaxFinderRadius {
			return invalid("finder radius must be between 0 and %.2f", MaxFinderRadius)
		}
		o.finderRadius = radius
		return nil
	}
}

// WithLogo places a logo in the center. data is a PNG, JPEG or SVG, detected
// from its content; it is copied, validated and decoded by New. A raster logo
// is re-encoded, never embedded as received. An SVG logo must stay inside a
// small subset (see the package documentation of logos) and is rebuilt from
// its parsed shapes, never copied. EXIF orientation of JPEG logos is not
// applied.
//
// Setting a logo selects error correction High unless WithErrorCorrection was
// used. H recovers about 30% of codewords under ideal conditions, not 30% of
// the image: a logo can still make a code unreadable.
func WithLogo(data []byte, opts ...LogoOption) Option {
	return func(o *options) error {
		if len(data) == 0 {
			return fmt.Errorf("%w: empty logo", ErrUnsupportedLogo)
		}
		lo := &logoOptions{size: DefaultLogoSize, padding: 1, background: true}
		for _, opt := range opts {
			if opt == nil {
				return invalid("nil logo option")
			}
			if err := opt(lo); err != nil {
				return err
			}
		}
		o.logoData = append([]byte(nil), data...)
		o.logo = lo
		return nil
	}
}

// LogoSize sets the logo's longest side as a fraction of the width of the
// module area, in (0, MaxLogoSize] (default DefaultLogoSize). The limit is a
// heuristic, not a promise that the code scans.
func LogoSize(fraction float64) LogoOption {
	return func(l *logoOptions) error {
		if !finite(fraction) || fraction <= 0 || fraction > MaxLogoSize {
			return invalid("logo size must be in (0, %.2f]", MaxLogoSize)
		}
		l.size = fraction
		return nil
	}
}

// LogoBackground enables (the default) or disables the opaque plate behind the
// logo. The plate is drawn in the background color, so it requires an opaque
// background. Without it the logo is composited directly over the modules,
// which is less legible for transparent logos.
func LogoBackground(enabled bool) LogoOption {
	return func(l *logoOptions) error {
		l.background = enabled
		return nil
	}
}

// LogoPadding sets the plate's margin around the logo in modules, in
// [0, MaxLogoPadding] (default 1).
func LogoPadding(modules float64) LogoOption {
	return func(l *logoOptions) error {
		if !finite(modules) || modules < 0 || modules > MaxLogoPadding {
			return invalid("logo padding must be between 0 and %d modules", MaxLogoPadding)
		}
		l.padding = modules
		return nil
	}
}

// LogoBorderRadius rounds the corners of the logo image itself, as a fraction
// of its shorter side in [0, 0.5] (default 0).
func LogoBorderRadius(radius float64) LogoOption {
	return func(l *logoOptions) error {
		if !finite(radius) || radius < 0 || radius > 0.5 {
			return invalid("logo radius must be between 0 and 0.5")
		}
		l.radius = radius
		return nil
	}
}

// LogoBackgroundRadius rounds the corners of the plate, as a fraction of its
// shorter side in [0, 0.5] (default 0). It is independent of LogoBorderRadius.
func LogoBackgroundRadius(radius float64) LogoOption {
	return func(l *logoOptions) error {
		if !finite(radius) || radius < 0 || radius > 0.5 {
			return invalid("logo background radius must be between 0 and 0.5")
		}
		l.bgRadius = radius
		return nil
	}
}

// rect is an integer pixel rectangle.
type rect struct{ x, y, w, h int }

func (r rect) x1() int { return r.x + r.w }
func (r rect) y1() int { return r.y + r.h }

// logoLayout is the resolved placement of the logo in pixels.
type logoLayout struct {
	img    *logoImage
	box    rect // the logo image
	plate  rect // the plate (equals box expanded by the padding); drawn only when opts.background
	opts   logoOptions
	radius float64 // pixels, logo image
	pRad   float64 // pixels, plate
}

// QRCode is an encoded and laid-out QR Code. Build it with New.
type QRCode struct {
	sym    *symbol
	opt    options
	scale  int // pixels per module
	margin int
	px     int // image side in pixels: scale * (size + 2*margin)
	logo   *logoLayout
}

// New encodes payload (UTF-8 text, kept byte for byte) and lays it out. It
// returns ErrEmptyPayload for an empty payload, ErrTooLarge when no version
// holds it, and errors wrapping ErrInvalidOption, ErrUnsupportedLogo or
// ErrLogoOverlap for the other failures. It never panics on caller input.
func New(payload string, opts ...Option) (*QRCode, error) {
	return build([]byte(payload), opts)
}

// NewBytes is New for arbitrary binary data, encoded in byte mode exactly as
// given. Scanners usually present binary content as text or hex.
func NewBytes(payload []byte, opts ...Option) (*QRCode, error) {
	return build(append([]byte(nil), payload...), opts)
}

func contrast(a, b color.NRGBA) float64 {
	lum := func(c color.NRGBA) float64 {
		f := func(v uint8) float64 {
			x := float64(v) / 255
			if x <= 0.03928 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*f(c.R) + 0.7152*f(c.G) + 0.0722*f(c.B)
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func build(data []byte, opts []Option) (*QRCode, error) {
	o := options{
		size: DefaultSize, margin: DefaultMargin, level: Medium,
		fg: color.NRGBA{0, 0, 0, 255}, bg: color.NRGBA{255, 255, 255, 255},
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, invalid("nil option")
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	if len(data) == 0 {
		return nil, ErrEmptyPayload
	}
	if len(data) > maxPayloadBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	if o.fg.A == 255 && o.bg.A == 255 && contrast(o.fg, o.bg) < 3 {
		return nil, invalid("foreground and background contrast is below 3:1")
	}
	if o.fg == o.bg {
		return nil, invalid("foreground and background colors are identical")
	}
	var logo *logoImage
	if o.logo != nil {
		if o.logo.background && o.bg.A != 255 {
			return nil, invalid("a logo plate requires an opaque background")
		}
		var err error
		if logo, err = loadLogo(o.logoData); err != nil {
			return nil, err
		}
		if !o.levelSet {
			o.level = High
		}
	}
	v0, mode := minimalVersion(data, o.level, minVersion)
	if v0 == 0 {
		return nil, fmt.Errorf("%w: %d bytes at level %s", ErrTooLarge, len(data), levelName(o.level))
	}
	q := &QRCode{opt: o, margin: o.margin}
	for v := v0; v <= maxVersion; v++ {
		n := 17 + 4*v
		total := n + 2*o.margin
		scale := o.size / total
		if scale < 1 {
			return nil, invalid("size %d is smaller than one pixel per module (%d modules wide at version %d)", o.size, total, v)
		}
		q.scale, q.px = scale, scale*total
		if logo == nil {
			q.sym, _ = encodeSymbol(data, mode, v, o.level)
			return q, nil
		}
		ll := q.layoutLogo(logo, n)
		if q.logoClear(ll, functionMask(v), n) {
			q.logo = ll
			sym, err := encodeSymbol(data, mode, v, o.level)
			if err != nil {
				return nil, err
			}
			q.sym = sym
			return q, nil
		}
	}
	return nil, fmt.Errorf("%w: no version up to %d keeps the logo clear of them", ErrLogoOverlap, maxVersion)
}

func levelName(l ErrorCorrection) string { return [4]string{"L", "M", "Q", "H"}[l&3] }

func (q *QRCode) layoutLogo(img *logoImage, n int) *logoLayout {
	lo := *q.opt.logo
	maxSide := lo.size * float64(n*q.scale)
	w, h := maxSide, maxSide
	if img.aspect >= 1 {
		h = maxSide / img.aspect
	} else {
		w = maxSide * img.aspect
	}
	wi, hi := max(1, int(math.Round(w))), max(1, int(math.Round(h)))
	cx, cy := q.px/2, q.px/2
	box := rect{cx - wi/2, cy - hi/2, wi, hi}
	pad := int(math.Round(lo.padding * float64(q.scale)))
	plate := rect{box.x - pad, box.y - pad, wi + 2*pad, hi + 2*pad}
	return &logoLayout{
		img: img, box: box, plate: plate, opts: lo,
		radius: lo.radius * float64(min(box.w, box.h)),
		pRad:   lo.bgRadius * float64(min(plate.w, plate.h)),
	}
}

// logoClear reports whether the area the logo (or its plate) covers touches no
// function module.
func (q *QRCode) logoClear(l *logoLayout, fn []bool, n int) bool {
	r := l.box
	if l.opts.background {
		r = l.plate
	}
	off := q.margin * q.scale
	x0, y0 := floorDiv(r.x-off, q.scale), floorDiv(r.y-off, q.scale)
	x1, y1 := floorDiv(r.x1()-1-off, q.scale), floorDiv(r.y1()-1-off, q.scale)
	if x0 < 0 || y0 < 0 || x1 >= n || y1 >= n {
		return false
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if fn[y*n+x] {
				return false
			}
		}
	}
	return true
}

func floorDiv(a, b int) int {
	d := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		d--
	}
	return d
}

// Modules returns a copy of the module matrix without the quiet zone:
// Modules()[y][x] is true for a dark module. It is the same matrix both
// renderers draw, and lets callers build other renderings (a terminal, say).
func (q *QRCode) Modules() [][]bool {
	n := q.sym.size
	out := make([][]bool, n)
	for y := range out {
		out[y] = make([]bool, n)
		for x := range n {
			out[y][x] = q.sym.at(x, y)
		}
	}
	return out
}

// Version returns the QR Code version (1-40). With a logo it can exceed the
// minimum version of the payload (see the package documentation).
func (q *QRCode) Version() int { return q.sym.version }

// ErrorCorrection returns the level the symbol was encoded with.
func (q *QRCode) ErrorCorrection() ErrorCorrection { return q.sym.level }

// Size returns the side in pixels of the PNG and the SVG width and height.
func (q *QRCode) Size() int { return q.px }

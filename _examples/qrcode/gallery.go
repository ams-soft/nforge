package main

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/ams-soft/nforge/helper/qrcode"
)

//go:embed assets/logo.svg
var assets embed.FS

// demoLogoPNG draws a small neutral mark in code (no third-party artwork).
func demoLogoPNG() ([]byte, error) {
	const n = 96
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			dx, dy := float64(x)-n/2+0.5, float64(y)-n/2+0.5
			switch d := dx*dx + dy*dy; {
			case d < 22*22:
				img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			case d < 44*44:
				img.SetNRGBA(x, y, color.NRGBA{11, 110, 138, 255})
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

type galleryCase struct {
	name    string // stable file stem
	caption string
	opts    []qrcode.Option
}

// galleryCases lists the styles shown. logo is the bytes used by the logo
// cases: the -logo file, or the built-in demo logo.
func galleryCases(size int, logo, svgLogo []byte) []galleryCase {
	base := []qrcode.Option{qrcode.WithSize(size)}
	with := func(extra ...qrcode.Option) []qrcode.Option {
		return append(append([]qrcode.Option{}, base...), extra...)
	}
	return []galleryCase{
		{"01-traditional", "Traditional (default): square modules, level M, 4-module quiet zone", with()},
		{"02-rounded-modules", "Rounded data modules (radius 0.3); function patterns stay square", with(qrcode.WithRoundedModules(0.3))},
		{"03-rounded-finders", "Rounded finder patterns (radius 0.2)", with(qrcode.WithRoundedFinders(0.2))},
		{"04-rounded-both", "Rounded modules and finders", with(qrcode.WithRoundedModules(0.3), qrcode.WithRoundedFinders(0.2))},
		{"05-logo", "Centered logo on a plate, level H selected automatically", with(qrcode.WithLogo(logo))},
		{"06-logo-rounded", "Logo and plate with rounded corners, rounded modules and finders",
			with(qrcode.WithRoundedModules(0.3), qrcode.WithRoundedFinders(0.2),
				qrcode.WithLogo(logo, qrcode.LogoSize(0.15), qrcode.LogoPadding(1), qrcode.LogoBorderRadius(0.2), qrcode.LogoBackgroundRadius(0.2)))},
		{"07-logo-svg", "SVG logo (rebuilt from a validated subset, never copied)", with(qrcode.WithLogo(svgLogo))},
		{"08-colors", "Custom colors", with(qrcode.WithColors(color.NRGBA{R: 11, G: 80, B: 110, A: 255}, color.NRGBA{R: 250, G: 248, B: 235, A: 255}), qrcode.WithRoundedModules(0.2))},
	}
}

// writeGallery renders every case as PNG and SVG into dir, writes the Base64
// and Data URI texts and an index.html that shows them, and returns the paths
// it created. It is the only place the example writes files.
func writeGallery(dir, payload string, size int, logoPath string) ([]string, error) {
	logo, err := demoLogoPNG()
	if err != nil {
		return nil, fmt.Errorf("build demo logo: %w", err)
	}
	if logoPath != "" {
		if logo, err = os.ReadFile(logoPath); err != nil { //nolint:gosec // G304: the example reads the file the user named with -logo
			return nil, fmt.Errorf("read logo: %w", err)
		}
	}
	svgLogo, err := assets.ReadFile("assets/logo.svg")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}

	var (
		created        []string
		b64, uri, page strings.Builder
	)
	write := func(name string, data []byte) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		created = append(created, p)
		return nil
	}
	page.WriteString("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><title>nForge qrcode gallery</title>\n" +
		"<style>body{font:16px system-ui,sans-serif;margin:2rem}.c{display:inline-block;margin:0 1.5rem 2rem 0;vertical-align:top}" +
		"img{width:256px;height:256px;border:1px solid #ccc;background:#fff}p{max-width:540px}</style></head><body>\n" +
		"<h1>nForge helper/qrcode</h1>\n<p>Each row shows the PNG and the SVG of one configuration. Scan them with a phone to check legibility: styling can reduce compatibility.</p>\n")

	for _, c := range galleryCases(size, logo, svgLogo) {
		q, err := qrcode.New(payload, c.opts...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.name, err)
		}
		pngB, err := q.PNG()
		if err != nil {
			return nil, fmt.Errorf("%s: png: %w", c.name, err)
		}
		svgB, err := q.SVG()
		if err != nil {
			return nil, fmt.Errorf("%s: svg: %w", c.name, err)
		}
		for _, f := range []struct {
			name string
			data []byte
		}{{c.name + ".png", pngB}, {c.name + ".svg", svgB}} {
			if err := write(f.name, f.data); err != nil {
				return nil, err
			}
		}
		for _, f := range []struct {
			label  string
			format qrcode.Format
		}{{"png", qrcode.FormatPNG}, {"svg", qrcode.FormatSVG}} {
			s, err := q.Base64(f.format)
			if err != nil {
				return nil, fmt.Errorf("%s: base64 %s: %w", c.name, f.label, err)
			}
			d, err := q.DataURI(f.format)
			if err != nil {
				return nil, fmt.Errorf("%s: data uri %s: %w", c.name, f.label, err)
			}
			fmt.Fprintf(&b64, "%s.%s\n%s\n\n", c.name, f.label, s)
			fmt.Fprintf(&uri, "%s.%s\n%s\n\n", c.name, f.label, d)
		}
		fmt.Fprintf(&page, "<div class=\"c\"><p>%s (version %d)</p><img src=\"%s.png\" alt=\"PNG\"> <img src=\"%s.svg\" alt=\"SVG\"></div>\n",
			html.EscapeString(c.caption), q.Version(), c.name, c.name)
	}
	page.WriteString("</body></html>\n")
	for _, f := range []struct {
		name string
		data string
	}{{"base64.txt", b64.String()}, {"datauri.txt", uri.String()}, {"index.html", page.String()}} {
		if err := write(f.name, []byte(f.data)); err != nil {
			return nil, err
		}
	}
	return created, nil
}

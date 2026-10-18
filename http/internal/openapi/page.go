package openapi

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed docs.html
var docsHTML string

var docsTmpl = template.Must(template.New("docs").Parse(docsHTML))

// Page describes the documentation page.
type Page struct {
	Title   string
	Version string
	SpecURL string
	YAMLURL string
	Routes  int
	// Accent is injected as CSS: it must have been validated as a hex color
	// (config.validateRequiredConfig does it).
	Accent string
	// Hosts are the documented servers ("Try it out" targets), allowed in
	// the page's connect-src.
	Hosts []string
}

type pageData struct {
	Title    string
	Version  string
	Initial  string
	SpecURL  string
	YAMLURL  string
	NumRoute int
	Accent   template.CSS
	Nonce    string
}

// RenderPage renders the HTML documentation page and the Content-Security-Policy
// that fits it: its inline scripts carry a fresh nonce, Swagger UI comes from
// jsDelivr and the fonts from Google Fonts. The server sends the policy only
// when http.security sends one, replacing the API-wide policy (whose
// script-src 'self' would block the page).
func RenderPage(p Page) (page []byte, csp string, err error) {
	initial, _ := utf8.DecodeRuneInString(strings.TrimSpace(p.Title))
	if initial == utf8.RuneError {
		initial = 'A'
	}
	nonce := base64.RawURLEncoding.EncodeToString(randomBytes(16)) // no "+" or "/": html/template would escape them in the attribute
	var buf bytes.Buffer
	err = docsTmpl.Execute(&buf, pageData{
		Title:    p.Title,
		Version:  p.Version,
		Initial:  string(unicode.ToUpper(initial)),
		SpecURL:  p.SpecURL,
		YAMLURL:  p.YAMLURL,
		NumRoute: p.Routes,
		Accent:   template.CSS(p.Accent), //nolint:gosec // validated as hex in config.validateRequiredConfig
		Nonce:    nonce,
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), pageCSP(nonce, p.Hosts), nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails (crypto/rand panics on an unusable source)
	return b
}

// pageCSP is the policy of docs.html; keep it in sync with what the page loads.
func pageCSP(nonce string, hosts []string) string {
	connect := []string{"'self'"}
	for _, h := range hosts {
		if u, err := url.Parse(h); err == nil && u.Scheme != "" && u.Host != "" {
			connect = append(connect, u.Scheme+"://"+u.Host)
		}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"base-uri 'self'",
		"object-src 'none'",
		"frame-ancestors 'self'",
		"form-action 'self'",
		"script-src 'nonce-" + nonce + "' https://cdn.jsdelivr.net",
		"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com",
		"font-src 'self' data: https://fonts.gstatic.com",
		"img-src 'self' data:",
		"connect-src " + strings.Join(connect, " "),
	}, ";")
}

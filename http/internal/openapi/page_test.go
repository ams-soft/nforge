package openapi

import (
	"strings"
	"testing"
)

func TestRenderPagePinsSwaggerUIAndDropsTheScriptHost(t *testing.T) {
	page, csp, err := RenderPage(Page{Title: "T", Version: "1", SpecURL: "/docs/swagger.json", YAMLURL: "/docs/swagger.yaml", Accent: "#007d9c"})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, file := range []string{"swagger-ui-bundle.js", "swagger-ui.css"} {
		want := "swagger-ui-dist@" + swaggerUIVersion + "/" + file
		if !strings.Contains(html, want) {
			t.Errorf("docs.html must load %s (swaggerUIVersion and the page disagree)", want)
		}
	}
	if strings.Contains(html, "swagger-ui-dist@5/") {
		t.Error("an unpinned major version is back in docs.html")
	}
	if !strings.Contains(csp, "script-src 'nonce-") || strings.Contains(csp, "script-src 'nonce-") && strings.Contains(strings.SplitN(strings.SplitN(csp, "script-src ", 2)[1], ";", 2)[0], "http") {
		t.Errorf("script-src must be the nonce alone: %q", csp)
	}
}

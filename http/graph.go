//go:build nforge_graph

package nhttp

import (
	"context"
	stdhttp "net/http"
	"strings"
	"sync"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/http/internal/graph"
	"github.com/ams-soft/nforge/module"
)

const (
	// graphStatusTTL is how long a status sample serves the page, so a burst of
	// requests costs one probe of each dependency.
	graphStatusTTL = 5 * time.Second
	// graphProbeTimeout bounds one sample, like readinessTimeout.
	graphProbeTimeout = 2 * time.Second
)

// graphPage holds the latest render of the graph. The structure never changes
// after bootstrap; only the status part is refreshed.
type graphPage struct {
	mu       sync.Mutex
	sampled  time.Time
	rendered graph.Rendered
}

// current returns the render, refreshing it when it is older than graphStatusTTL.
// The lock is held across the probe on purpose: concurrent requests wait for one
// sample instead of each starting their own.
func (g *graphPage) current(s *Server, snap module.Snapshot) (graph.Rendered, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if !g.sampled.IsZero() && now.Sub(g.sampled) < graphStatusTTL {
		return g.rendered, nil
	}
	// Not the request context: a client that disconnects must not leave a sample
	// that shows every module as not ready.
	ctx, cancel := context.WithTimeout(context.Background(), graphProbeTimeout)
	defer cancel()
	report := s.status.Status(ctx)
	status := report.Modules
	for _, st := range status {
		if st.Availability == module.NotReady {
			s.logger.WarnContext(ctx, "[HTTPModule] Graph: module not ready", "module", string(st.ID))
		}
	}
	rendered, err := graph.Render(graph.NewDocument(snap, status, now))
	if err != nil {
		return graph.Rendered{}, err
	}
	g.rendered, g.sampled = rendered, now
	return rendered, nil
}

// registerGraph serves the module graph page at http.routes.graph.path and its
// Mermaid script. It is public: the operator restricts it (see AGENTS.md §7.13).
func (s *Server) registerGraph(r router.Router) {
	cfg := &s.cfg
	jwks := ""
	if s.jose != nil && s.jose.JWKS().Enabled {
		jwks = s.jose.JWKS().Path
	}
	if err := ValidateGraphRoute(cfg, jwks); err != nil { // a Config built by hand skips the module
		panic("nhttp: " + err.Error())
	}
	if s.status == nil {
		panic("nhttp: http.routes.graph needs a server built by nhttp.Module in an application")
	}
	snap := s.status.Snapshot()
	path := cfg.Routes.Graph.Path
	if path == "" {
		path = DefaultGraphPath
	}
	assetURL := path + GraphAssetSuffix
	title := s.app.Name
	page := &s.graph

	r.Get(path, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		rendered, err := page.current(s, snap)
		var html []byte
		var csp string
		if err == nil {
			html, csp, err = graph.Page(rendered, title, assetURL)
		}
		if err != nil {
			s.logger.ErrorContext(req.Context(), "[HTTPModule] Failed to render the module graph", "error", err)
			stdhttp.Error(w, "failed to render the graph", stdhttp.StatusInternalServerError)
			return
		}
		h := w.Header()
		// Replaces the API policy of http.security, whose script-src would block
		// the page. It is always enforced: this page must not depend on that section.
		h.Del("Content-Security-Policy-Report-Only")
		h.Set("Content-Security-Policy", csp)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store") // the state changes, and it is not for shared caches
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(html)
	})

	etag := `"mermaid-` + graph.MermaidVersion + `"`
	r.Get(assetURL, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "no-cache") // revalidated: the ETag makes that a 304
		h.Set("ETag", etag)
		h.Set("Vary", "Accept-Encoding")
		h.Set("X-Content-Type-Options", "nosniff")
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(stdhttp.StatusNotModified)
			return
		}
		if acceptsGzip(req.Header.Get("Accept-Encoding")) {
			h.Set("Content-Encoding", "gzip")
			_, _ = w.Write(graph.MermaidGzip())
			return
		}
		if err := graph.WriteMermaid(w); err != nil {
			s.logger.ErrorContext(req.Context(), "[HTTPModule] Failed to serve the Mermaid script", "error", err)
		}
	})
}

// acceptsGzip reports whether an Accept-Encoding value lists gzip, unless with
// a zero weight.
func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q := strings.ReplaceAll(strings.ToLower(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

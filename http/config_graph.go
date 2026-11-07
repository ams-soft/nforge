package nhttp

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	// DefaultGraphPath is http.routes.graph.path when enabled without a path.
	DefaultGraphPath = "/graph"
	// GraphAssetSuffix follows the graph path to form the URL of the Mermaid
	// script, which the page loads from the application itself.
	GraphAssetSuffix = "/mermaid.min.js"
)

// graphPathPattern accepts literal paths only: a router parameter or wildcard
// would make the page answer on paths nobody configured. It is compiled on use,
// because the route is off by default.
const graphPathPattern = `^(/[A-Za-z0-9._~-]+)+$`

// ValidateGraphRoute checks http.routes.graph: a literal path that collides with
// no built-in route (jwksPath is the JWKS route, "" when it is not served). It
// checks nothing when the route is disabled. Collisions with application routes
// surface as a router panic at registration.
func ValidateGraphRoute(cfg *Config, jwksPath string) error {
	graph := cfg.Routes.Graph
	if !graph.Enabled {
		return nil
	}
	path := graph.Path
	if path == "" {
		path = DefaultGraphPath
	}
	if !regexp.MustCompile(graphPathPattern).MatchString(path) {
		return fmt.Errorf("http.routes.graph.path must be a literal path such as /graph, got %q", path)
	}
	for segment := range strings.SplitSeq(path[1:], "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("http.routes.graph.path must not have dot segments, got %q", path)
		}
	}
	taken := builtinPaths(cfg, jwksPath)
	for _, p := range []string{path, path + GraphAssetSuffix} {
		if i := slices.IndexFunc(taken, func(b builtinPath) bool { return b.path == p }); i >= 0 {
			return fmt.Errorf("http.routes.graph.path %s collides with %s", path, taken[i].owner)
		}
	}
	return nil
}

type builtinPath struct{ path, owner string }

// builtinPaths lists the enabled built-in routes other than the graph.
func builtinPaths(cfg *Config, jwksPath string) []builtinPath {
	var taken []builtinPath
	if cfg.Docs.Enabled {
		base := "/" + strings.Trim(cfg.Docs.Path, "/")
		if base == "/" {
			base = "/docs"
		}
		for _, p := range []string{base, base + "/swagger.json", base + "/swagger.yaml"} {
			taken = append(taken, builtinPath{p, "http.docs"})
		}
	}
	if cfg.Routes.Health.Enabled {
		taken = append(taken, builtinPath{cfg.Routes.Health.Path, "http.routes.health"})
	}
	if cfg.Routes.Readiness.Enabled {
		taken = append(taken, builtinPath{cfg.Routes.Readiness.Path, "http.routes.readiness"})
	}
	if jwksPath != "" {
		taken = append(taken, builtinPath{jwksPath, "jose.jwks"})
	}
	return taken
}

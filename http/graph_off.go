//go:build !nforge_graph

package nhttp

import router "github.com/ams-soft/go-ams-router"

// graphPage is empty in a build without the graph page.
type graphPage struct{} //nolint:unused // the field of Server needs the type in every build

// registerGraph refuses to start: answering 404 on a route the configuration
// enables would look like a broken gateway, not like a missing build tag.
func (s *Server) registerGraph(router.Router) {
	panic("nhttp: http.routes.graph.enabled needs a binary built with -tags nforge_graph")
}

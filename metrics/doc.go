// Package metrics is a small, dependency-free metrics registry that renders the
// Prometheus text exposition format.
//
// metrics.Module publishes a *Registry (metrics.Capability). The framework's
// own modules register their instruments on it when it is present: the HTTP
// server (request count and duration by route, requests in flight), the SSE
// manager, the Messaging client, the database pool and the cache. The
// application adds its own with Counter, Gauge and Histogram. nhttp serves the
// registry at http.routes.metrics.path when that route is enabled; the page is
// unauthenticated like /health, so keep it on an internal port or restrict it
// at the gateway.
//
// Label cardinality is the caller's responsibility; each instrument drops new
// label sets past a fixed cap (and counts the drops) instead of growing without bound.
package metrics

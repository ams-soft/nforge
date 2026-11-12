package cache

import "github.com/ams-soft/nforge/metrics"

// registerMetrics exposes read hits and misses (Get and GetBytes) and the
// operations that failed, by driver.
func (c *Cache) registerMetrics(reg *metrics.Registry) {
	driver := c.backend.name()
	one := func(f func() uint64) func() []metrics.Sample {
		return func() []metrics.Sample {
			return []metrics.Sample{{Labels: map[string]string{"driver": driver}, Value: float64(f())}}
		}
	}
	reg.Func("nforge_cache_hits_total", "Reads (Get, GetBytes) that found the key.", "counter", one(c.hits.Load))
	reg.Func("nforge_cache_misses_total", "Reads (Get, GetBytes) that did not find the key.", "counter", one(c.misses.Load))
	reg.Func("nforge_cache_errors_total", "Operations that failed (not counting a missing key).", "counter", one(c.errs.Load))
}

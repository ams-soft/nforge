package database

import "github.com/ams-soft/nforge/metrics"

// registerMetrics exposes the pgx pool: connections by state and the acquire
// counters, read from pgxpool.Stat at scrape time.
func (db *DB) registerMetrics(reg *metrics.Registry) {
	if db.pool == nil {
		return
	}
	reg.Func("nforge_database_pool_connections", "Pool connections by state.", "gauge", func() []metrics.Sample {
		s := db.pool.Stat()
		return []metrics.Sample{
			{Labels: map[string]string{"state": "acquired"}, Value: float64(s.AcquiredConns())},
			{Labels: map[string]string{"state": "idle"}, Value: float64(s.IdleConns())},
			{Labels: map[string]string{"state": "constructing"}, Value: float64(s.ConstructingConns())},
		}
	})
	reg.Value("nforge_database_pool_max_connections", "Configured maximum pool size.", "gauge", func() float64 { return float64(db.pool.Stat().MaxConns()) })
	reg.Value("nforge_database_pool_acquires_total", "Connection acquisitions.", "counter", func() float64 { return float64(db.pool.Stat().AcquireCount()) })
	reg.Value("nforge_database_pool_empty_acquires_total", "Acquisitions that had to wait because no connection was idle.", "counter", func() float64 { return float64(db.pool.Stat().EmptyAcquireCount()) })
	reg.Value("nforge_database_pool_canceled_acquires_total", "Acquisitions canceled while waiting.", "counter", func() float64 { return float64(db.pool.Stat().CanceledAcquireCount()) })
	reg.Value("nforge_database_pool_acquire_seconds_total", "Total time spent acquiring connections.", "counter", func() float64 { return db.pool.Stat().AcquireDuration().Seconds() })
}

package sse

import "github.com/ams-soft/nforge/metrics"

// registerMetrics exposes Stats() at scrape time. User IDs never appear: the
// per-user distribution is a histogram of connection counts.
func (m *Manager) registerMetrics(reg *metrics.Registry) {
	g := func(name, help string, f func(Stats) float64) {
		reg.Value(name, help, "gauge", func() float64 { return f(m.Stats()) })
	}
	c := func(name, help string, f func(Stats) uint64) {
		reg.Value(name, help, "counter", func() float64 { return float64(f(m.Stats())) })
	}
	g("nforge_sse_connections", "Open event streams.", func(s Stats) float64 { return float64(s.ActiveConnections) })
	g("nforge_sse_users", "Users with at least one open stream.", func(s Stats) float64 { return float64(s.ActiveUsers) })
	reg.Func("nforge_sse_users_by_connections", "Users holding exactly N streams.", "gauge", func() []metrics.Sample {
		var out []metrics.Sample
		for n, users := range m.Stats().ConnectionsPerUser {
			out = append(out, metrics.Sample{Labels: map[string]string{"connections": itoa(n)}, Value: float64(users)})
		}
		return out
	})
	c("nforge_sse_connections_accepted_total", "Streams accepted.", func(s Stats) uint64 { return s.ConnectionsTotal })
	c("nforge_sse_connections_rejected_total", "Streams refused by a limit, the rate limit or shutdown.", func(s Stats) uint64 { return s.ConnectionsRejected })
	c("nforge_sse_events_published_total", "PublishToUser calls with a valid event.", func(s Stats) uint64 { return s.Published })
	c("nforge_sse_events_accepted_total", "Publications accepted by the driver.", func(s Stats) uint64 { return s.PublishAccepted })
	c("nforge_sse_events_failed_total", "Publications refused or failed at the driver.", func(s Stats) uint64 { return s.PublishFailed })
	c("nforge_sse_frames_enqueued_total", "Frames queued to local connections.", func(s Stats) uint64 { return s.LocalEnqueued })
	c("nforge_sse_slow_client_disconnects_total", "Clients disconnected because their queue filled.", func(s Stats) uint64 { return s.SlowClientDisconnect })
	c("nforge_sse_heartbeat_errors_total", "Heartbeat writes that failed.", func(s Stats) uint64 { return s.HeartbeatErrors })
	c("nforge_sse_write_errors_total", "Frame writes that failed.", func(s Stats) uint64 { return s.WriteErrors })
	c("nforge_sse_malformed_messages_total", "Distribution messages dropped as invalid.", func(s Stats) uint64 { return s.MalformedMessages })
	c("nforge_sse_lifetime_closes_total", "Streams closed at max_lifetime or token expiry.", func(s Stats) uint64 { return s.LifetimeCloses })
	c("nforge_sse_broker_reconnects_total", "Times distribution was restored after an outage.", func(s Stats) uint64 { return s.BrokerReconnects })
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

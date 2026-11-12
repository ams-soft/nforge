package message

import (
	"errors"
	"sync/atomic"

	"github.com/ams-soft/nforge/metrics"
)

// counters are the outcomes the client observes. They are plain atomics so the
// hot paths pay one add; the registry reads them at scrape time.
type counters struct {
	pubOK, pubNacked, pubUnroutable, pubUncertain, pubUnavailable atomic.Uint64
	consumed, failed, retried, dropped                            atomic.Uint64
}

func (c *Client) countPublish(err error) {
	switch {
	case err == nil:
		c.stats.pubOK.Add(1)
	case errors.Is(err, ErrUnroutable):
		c.stats.pubUnroutable.Add(1)
	case errors.Is(err, ErrNacked):
		c.stats.pubNacked.Add(1)
	case errors.Is(err, ErrPublishUncertain):
		c.stats.pubUncertain.Add(1)
	case errors.Is(err, ErrTransportUnavailable):
		c.stats.pubUnavailable.Add(1)
	}
}

// registerMetrics exposes publish and consume outcomes and the connection state.
func (c *Client) registerMetrics(reg *metrics.Registry) {
	reg.Func("nforge_message_publishes_total", "Publishes by outcome (ok, nacked, unroutable, uncertain, unavailable).", "counter", func() []metrics.Sample {
		out := func(name string, v uint64) metrics.Sample {
			return metrics.Sample{Labels: map[string]string{"outcome": name}, Value: float64(v)}
		}
		return []metrics.Sample{
			out("ok", c.stats.pubOK.Load()), out("nacked", c.stats.pubNacked.Load()), out("unroutable", c.stats.pubUnroutable.Load()),
			out("uncertain", c.stats.pubUncertain.Load()), out("unavailable", c.stats.pubUnavailable.Load()),
		}
	})
	reg.Value("nforge_message_deliveries_total", "Deliveries handed to consumer handlers.", "counter", func() float64 { return float64(c.stats.consumed.Load()) })
	reg.Value("nforge_message_handler_failures_total", "Deliveries whose handler failed, panicked or timed out.", "counter", func() float64 { return float64(c.stats.failed.Load()) })
	reg.Value("nforge_message_retries_total", "Failed deliveries republished to the retry queue.", "counter", func() float64 { return float64(c.stats.retried.Load()) })
	reg.Value("nforge_message_nacked_total", "Failed deliveries rejected (dead-lettered or dropped).", "counter", func() float64 { return float64(c.stats.dropped.Load()) })
	reg.Value("nforge_message_connection_ready", "1 while the connection and publisher channel are usable.", "gauge", func() float64 {
		if c.Status().Ready {
			return 1
		}
		return 0
	})
	reg.Value("nforge_message_connection_generation", "Connections established since start; it grows on every reconnection.", "counter", func() float64 { return float64(c.generation.Load()) })
}

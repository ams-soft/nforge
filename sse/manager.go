package sse

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ams-soft/nforge/codec"
)

// Manager owns the local connections, their bounded queues, the heartbeat and
// the shutdown of the Server-Sent Events of one application instance. It is the
// sse.Capability.
type Manager struct {
	cfg    Config
	codec  *codec.Registry
	logger *slog.Logger
	drv    driver

	mu        sync.Mutex // guards users, total, accepting; never held across I/O
	users     map[string]map[*conn]struct{}
	total     int
	accepting bool
	wg        sync.WaitGroup // active stream handlers; Add only under mu while accepting
	closing   chan struct{}
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error

	limiter *bucket
	stats   counters
}

// driver distributes an event to the instances that may hold the user.
type driver interface {
	// start makes distribution active; the manager accepts streams only after.
	start(ctx context.Context, m *Manager) error
	// publish distributes w. Returning nil means accepted by the driver.
	publish(ctx context.Context, w wire) error
	ready(ctx context.Context) error
	// sync is called when a user's first local connection opens or its last
	// closes; present reports the current truth. Drivers that route per user
	// bind or unbind the user; the others ignore it.
	sync(ctx context.Context, user string, present func() bool) error
	// reconnects is the number of times distribution was restored after an outage.
	reconnects() uint64
	stop(ctx context.Context) error
}

type counters struct {
	connections, rejected                   atomic.Uint64
	published, accepted, failed             atomic.Uint64
	received                                atomic.Uint64
	enqueued, slowClients, heartbeatErrors  atomic.Uint64
	writeErrors, malformed, expiredLifetime atomic.Uint64
}

// Stats is a snapshot of the manager's metrics. User IDs never appear in it.
type Stats struct {
	ActiveConnections int
	ActiveUsers       int
	// ConnectionsPerUser maps a connection count to the number of users that
	// hold exactly that many streams (low cardinality: bounded by the per-user limit).
	ConnectionsPerUser map[int]int

	ConnectionsTotal     uint64 // streams accepted since start
	ConnectionsRejected  uint64 // refused by a limit, the rate limit or shutdown
	Published            uint64 // PublishToUser calls with a valid event
	PublishAccepted      uint64 // accepted by the driver
	PublishFailed        uint64 // refused or failed at the driver
	LocalEnqueued        uint64 // frames queued to local connections
	DistributionReceived uint64 // messages this instance received from the distribution (rabbitmq)
	SlowClientDisconnect uint64
	HeartbeatErrors      uint64
	WriteErrors          uint64
	MalformedMessages    uint64 // broker messages dropped as invalid
	LifetimeCloses       uint64
	BrokerReconnects     uint64 // distribution restored after an outage
}

func newManager(cfg Config, reg *codec.Registry, logger *slog.Logger, drv driver) *Manager {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Manager{
		cfg: cfg, codec: reg, logger: logger, drv: drv,
		users:     map[string]map[*conn]struct{}{},
		closing:   make(chan struct{}),
		closeDone: make(chan struct{}),
		limiter:   newBucket(cfg.MaxConnectsPerSecond),
	}
}

// conn is one stream. Frames travel through ch; nothing ever closes ch, so a
// publisher can never send on a closed channel: the stream stops through done.
type conn struct {
	user   string
	ch     chan []byte
	done   chan struct{}
	once   sync.Once
	reason string // written once before done closes
}

func newConn(user string, buffer int) *conn {
	return &conn{user: user, ch: make(chan []byte, buffer), done: make(chan struct{})}
}

// close ends the stream with a sanitized reason code; the first reason wins.
func (c *conn) close(reason string) {
	c.once.Do(func() {
		c.reason = reason
		close(c.done)
	})
}

// register adds c unless the manager is not accepting or a limit applies.
// register adds c; first reports that it is the user's first local connection.
func (m *Manager) register(c *conn) (first bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !m.accepting:
		return false, errRejectClosed
	case m.total >= m.cfg.MaxConnections:
		return false, errRejectGlobal
	case len(m.users[c.user]) >= m.cfg.MaxConnectionsPerUser:
		return false, errRejectUser
	}
	set := m.users[c.user]
	first = set == nil
	if set == nil {
		set = map[*conn]struct{}{}
		m.users[c.user] = set
	}
	set[c] = struct{}{}
	m.total++
	m.wg.Add(1)
	return first, nil
}

// unregister removes c; last reports that the user has no local connection left.
func (m *Manager) unregister(c *conn) (last bool) {
	m.mu.Lock()
	if set, ok := m.users[c.user]; ok {
		if _, in := set[c]; in {
			delete(set, c)
			m.total--
			if len(set) == 0 {
				delete(m.users, c.user)
				last = true
			}
		}
	}
	m.mu.Unlock()
	m.wg.Done()
	return last
}

// present reports whether the user has a local connection.
func (m *Manager) present(user string) func() bool {
	return func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.users[user]) > 0
	}
}

// deliver queues the frame of w to every local connection of its user. Slow
// clients are disconnected, never partially fed. It returns the number of
// connections that took the frame.
func (m *Manager) deliver(w wire) int {
	m.mu.Lock()
	set := m.users[w.User]
	if len(set) == 0 {
		m.mu.Unlock()
		return 0
	}
	conns := make([]*conn, 0, len(set))
	for c := range set {
		conns = append(conns, c)
	}
	m.mu.Unlock()

	frame, err := m.frame(w)
	if err != nil {
		m.stats.malformed.Add(1)
		return 0
	}
	n := 0
	for _, c := range conns {
		select {
		case <-c.done:
			continue
		default:
		}
		select {
		case c.ch <- frame:
			n++
			m.stats.enqueued.Add(1)
		default:
			m.stats.slowClients.Add(1)
			m.logger.WarnContext(context.Background(), "[SSEModule] slow client disconnected", "reason", "queue_full")
			c.close("queue_full")
		}
	}
	return n
}

// PublishToUser sends event to every stream the user has open, on every
// instance of the application (with the rabbitmq driver) or on this one (memory).
// An event for a user nobody has connected is discarded and is still a
// successful publication, as long as the driver accepted it. A broker outage is
// ErrTransportUnavailable, never a false success.
func (m *Manager) PublishToUser(ctx context.Context, userID string, event Event) (PublishResult, error) {
	if err := m.validUser(userID); err != nil {
		return PublishResult{}, err
	}
	w, err := m.newWire(userID, event)
	if err != nil {
		return PublishResult{}, err
	}
	m.mu.Lock()
	open := m.accepting
	m.mu.Unlock()
	if !open {
		return PublishResult{}, ErrClosed
	}
	m.stats.published.Add(1)
	if err := ctx.Err(); err != nil {
		m.stats.failed.Add(1)
		return PublishResult{}, err
	}
	if err := m.drv.publish(ctx, w); err != nil {
		m.stats.failed.Add(1)
		return PublishResult{}, err
	}
	m.stats.accepted.Add(1)
	return PublishResult{Accepted: true}, nil
}

// start activates distribution, then opens the manager to streams.
func (m *Manager) start(ctx context.Context) error {
	if err := m.drv.start(ctx, m); err != nil {
		return err
	}
	m.mu.Lock()
	m.accepting = true
	m.mu.Unlock()
	return nil
}

// close stops accepting streams and publications, ends every open stream, waits
// for the handlers (bounded by ctx), then releases the driver. It is idempotent.
func (m *Manager) close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.accepting = false
		m.mu.Unlock()
		close(m.closing)
		go func() {
			defer close(m.closeDone)
			m.wg.Wait()
			m.closeErr = m.drv.stop(context.WithoutCancel(ctx))
		}()
	})
	select {
	case <-m.closeDone:
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns a snapshot of the metrics.
func (m *Manager) Stats() Stats {
	s := Stats{
		ConnectionsPerUser:   map[int]int{},
		ConnectionsTotal:     m.stats.connections.Load(),
		ConnectionsRejected:  m.stats.rejected.Load(),
		Published:            m.stats.published.Load(),
		PublishAccepted:      m.stats.accepted.Load(),
		PublishFailed:        m.stats.failed.Load(),
		LocalEnqueued:        m.stats.enqueued.Load(),
		DistributionReceived: m.stats.received.Load(),
		SlowClientDisconnect: m.stats.slowClients.Load(),
		HeartbeatErrors:      m.stats.heartbeatErrors.Load(),
		WriteErrors:          m.stats.writeErrors.Load(),
		MalformedMessages:    m.stats.malformed.Load(),
		LifetimeCloses:       m.stats.expiredLifetime.Load(),
		BrokerReconnects:     m.drv.reconnects(),
	}
	m.mu.Lock()
	s.ActiveConnections, s.ActiveUsers = m.total, len(m.users)
	for _, set := range m.users {
		s.ConnectionsPerUser[len(set)]++
	}
	m.mu.Unlock()
	return s
}

// ready is the module's readiness probe: the driver's.
func (m *Manager) ready(ctx context.Context) error {
	m.mu.Lock()
	open := m.accepting
	m.mu.Unlock()
	if !open {
		return ErrClosed
	}
	return m.drv.ready(ctx)
}

// bucket is a token bucket for connection attempts.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(perSecond int) *bucket {
	return &bucket{rate: float64(perSecond), burst: float64(perSecond), tokens: float64(perSecond), last: time.Now()}
}

func (b *bucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

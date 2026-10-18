package cache

import (
	"container/heap"
	"container/list"
	"context"
	"strconv"
	"sync"
	"time"
)

// cleanupBatch bounds how many expired entries one lock acquisition of the
// cleanup worker removes, so a large expiry wave never stalls requests.
const cleanupBatch = 256

// memory is the in-process driver: a map indexed by full key, an LRU list
// and a min-heap of deadlines, all under one mutex so every read-modify-write
// (NX, counters, replacement accounting) is atomic. Data is lost on restart.
//
// Accounting counts len(key)+len(value) of every stored entry; the map, list,
// heap and allocator overhead come on top, so max_bytes is a deterministic
// payload budget, not a cap on the process memory.
type memory struct {
	mu    sync.Mutex
	items map[string]*memEntry
	lru   list.List // front = most recently used
	exp   expHeap   // expiring entries only, earliest deadline first
	bytes int64

	maxEntries int
	maxBytes   int64
	maxValue   int64
	now        func() time.Time

	quit     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type memEntry struct {
	key      string
	value    []byte
	deadline time.Time // zero = persistent; carries the monotonic reading of now()
	elem     *list.Element
	heapIdx  int // -1 when not in the heap
}

func (e *memEntry) size() int64 { return int64(len(e.key) + len(e.value)) }

func newMemory(cfg Config, now func() time.Time) *memory {
	m := &memory{
		items:      make(map[string]*memEntry),
		maxEntries: cfg.Memory.MaxEntries,
		maxBytes:   int64(cfg.Memory.MaxBytes),
		maxValue:   int64(cfg.Limits.MaxValueBytes),
		now:        now,
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go m.cleanupLoop(cfg.Memory.CleanupInterval)
	return m
}

func (m *memory) name() string { return DriverMemory }

func (m *memory) cleanupLoop(interval time.Duration) {
	defer close(m.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-m.quit:
			return
		case <-t.C:
			for m.cleanupOnce() == cleanupBatch {
				select {
				case <-m.quit:
					return
				default:
				}
			}
		}
	}
}

// cleanupOnce removes up to cleanupBatch expired entries and reports how
// many it removed.
func (m *memory) cleanupOnce() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	n := 0
	for n < cleanupBatch && len(m.exp) > 0 && expired(m.exp[0], now) {
		m.removeLocked(m.exp[0])
		n++
	}
	return n
}

func expired(e *memEntry, now time.Time) bool {
	return !e.deadline.IsZero() && !now.Before(e.deadline)
}

// liveLocked returns the entry if it exists and has not expired; an expired
// one is removed on the spot (it is never returned between cleanups).
func (m *memory) liveLocked(key string, now time.Time) *memEntry {
	e := m.items[key]
	if e == nil {
		return nil
	}
	if expired(e, now) {
		m.removeLocked(e)
		return nil
	}
	return e
}

func (m *memory) removeLocked(e *memEntry) {
	delete(m.items, e.key)
	m.lru.Remove(e.elem)
	if e.heapIdx >= 0 {
		heap.Remove(&m.exp, e.heapIdx)
	}
	m.bytes -= e.size()
}

// checkFitLocked rejects a value that can never be stored, before anything
// is evicted or replaced.
func (m *memory) checkFitLocked(key string, value []byte) error {
	if int64(len(value)) > m.maxValue {
		return newError(ErrValueTooLarge, "max_value_bytes")
	}
	if int64(len(key)+len(value)) > m.maxBytes {
		return newError(ErrValueTooLarge, "memory_capacity")
	}
	return nil
}

// writeLocked replaces (or creates) key. The caller already ran
// checkFitLocked, so after removing the old entry, reclaiming expired ones
// and evicting LRU entries the new one always fits.
func (m *memory) writeLocked(key string, value []byte, deadline, now time.Time) {
	if old := m.items[key]; old != nil {
		m.removeLocked(old)
	}
	size := int64(len(key) + len(value))
	for m.bytes+size > m.maxBytes || len(m.items)+1 > m.maxEntries {
		if len(m.exp) > 0 && expired(m.exp[0], now) {
			m.removeLocked(m.exp[0])
			continue
		}
		back := m.lru.Back()
		if back == nil {
			break
		}
		m.removeLocked(back.Value.(*memEntry))
	}
	e := &memEntry{key: key, value: value, deadline: deadline, heapIdx: -1}
	e.elem = m.lru.PushFront(e)
	if !deadline.IsZero() {
		heap.Push(&m.exp, e)
	}
	m.items[key] = e
	m.bytes += size
}

func deadlineFor(now time.Time, ttl time.Duration) time.Time {
	if ttl == 0 {
		return time.Time{}
	}
	return now.Add(ttl)
}

func (m *memory) get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.liveLocked(key, m.now())
	if e == nil {
		return nil, newError(ErrNotFound, "")
	}
	m.lru.MoveToFront(e.elem)
	return append([]byte{}, e.value...), nil // non-nil even when empty
}

func (m *memory) set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFitLocked(key, value); err != nil {
		return err
	}
	now := m.now()
	m.writeLocked(key, value, deadlineFor(now, ttl), now)
	return nil
}

func (m *memory) setNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.liveLocked(key, now) != nil {
		return false, nil
	}
	if err := m.checkFitLocked(key, value); err != nil {
		return false, err
	}
	m.writeLocked(key, value, deadlineFor(now, ttl), now)
	return true, nil
}

func (m *memory) del(_ context.Context, keys []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var n int64
	for _, k := range keys {
		if e := m.liveLocked(k, now); e != nil {
			m.removeLocked(e)
			n++
		}
	}
	return n, nil
}

// exists, expire and ttl are metadata probes: they do not change recency.
func (m *memory) exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveLocked(key, m.now()) != nil, nil
}

func (m *memory) expire(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	e := m.liveLocked(key, now)
	if e == nil {
		return false, nil
	}
	e.deadline = now.Add(ttl)
	if e.heapIdx >= 0 {
		heap.Fix(&m.exp, e.heapIdx)
	} else {
		heap.Push(&m.exp, e)
	}
	return true, nil
}

func (m *memory) ttl(_ context.Context, key string) (Expiration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	e := m.liveLocked(key, now)
	switch {
	case e == nil:
		return Expiration{}, nil
	case e.deadline.IsZero():
		return Expiration{Exists: true, Persistent: true}, nil
	default:
		return Expiration{Exists: true, Remaining: max(e.deadline.Sub(now), 0)}, nil
	}
}

func (m *memory) incr(_ context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var cur int64
	var deadline time.Time
	if e := m.liveLocked(key, now); e != nil {
		v, ok := parseCounter(e.value)
		if !ok {
			return 0, newError(ErrNotInteger, "")
		}
		cur, deadline = v, e.deadline
	}
	next, ok := addInt64(cur, delta)
	if !ok {
		return 0, newError(ErrCounterOverflow, "")
	}
	value := strconv.AppendInt(nil, next, 10)
	if err := m.checkFitLocked(key, value); err != nil {
		return 0, err
	}
	m.writeLocked(key, value, deadline, now) // the TTL is preserved
	return next, nil
}

func (m *memory) ping(context.Context) error { return nil }

func (m *memory) stop() {
	m.stopOnce.Do(func() { close(m.quit) })
	<-m.done
}

func (m *memory) close() error {
	m.stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.items)
	m.lru.Init()
	m.exp = nil
	m.bytes = 0
	return nil
}

// parseCounter accepts exactly what Redis INCRBY accepts: a signed base-10
// int64 without sign '+', spaces or leading zeros ("0" itself is fine, "-0"
// is not).
func parseCounter(b []byte) (int64, bool) {
	if len(b) == 0 || len(b) > 20 {
		return 0, false
	}
	digits := b
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 || (digits[0] == '0' && len(b) != 1) {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	return n, err == nil
}

func addInt64(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// expHeap is a container/heap of entries ordered by deadline.
type expHeap []*memEntry

func (h expHeap) Len() int           { return len(h) }
func (h expHeap) Less(i, j int) bool { return h[i].deadline.Before(h[j].deadline) }
func (h expHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIdx = i
	h[j].heapIdx = j
}
func (h *expHeap) Push(x any) {
	e := x.(*memEntry)
	e.heapIdx = len(*h)
	*h = append(*h, e)
}
func (h *expHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	e.heapIdx = -1
	return e
}

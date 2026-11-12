package sse

import (
	"bufio"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Hundreds of live streams, concurrent publishers and churn: every event is
// delivered to every tab of its user, nothing leaks once the clients leave.
func TestStressManyStreamsDeliveryAndNoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	const users, tabs, events = 150, 3, 20
	m := startManager(t, func(c *Config) {
		c.MaxConnections = 10000
		c.MaxConnectionsPerUser = 10
		c.MaxConnectsPerSecond = 100000
	})
	srv := serverFor(t, m)
	addr := strings.TrimPrefix(srv.URL, "http://")
	before := settledGoroutinesSSE()

	var got atomic.Int64
	var wg sync.WaitGroup
	var conns []net.Conn
	for u := range users {
		for range tabs {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, c)
			fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nX-Test-User: u%d\r\n\r\n", u)
			wg.Go(func() {
				rd := bufio.NewReader(c)
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(line, "data: ") {
						got.Add(1)
					}
				}
			})
		}
	}
	waitFor(t, "all streams", func() bool { return m.Stats().ActiveConnections == users*tabs })

	var pub sync.WaitGroup
	for u := range users {
		pub.Go(func() {
			for i := range events {
				if _, err := m.PublishToUser(t.Context(), "u"+strconv.Itoa(u), Event{Data: strconv.Itoa(i)}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	pub.Wait()
	waitFor(t, "every delivery", func() bool { return got.Load() == int64(users*tabs*events) })

	for _, c := range conns {
		_ = c.Close()
	}
	waitFor(t, "all streams to close", func() bool { return m.Stats().ActiveConnections == 0 && m.Stats().ActiveUsers == 0 })
	wg.Wait()
	if after := settledGoroutinesSSE(); after > before+5 {
		t.Fatalf("goroutines %d -> %d after every client left", before, after)
	}
	if st := m.Stats(); st.SlowClientDisconnect != 0 || st.WriteErrors > int64ToUint(users*tabs) {
		t.Errorf("unexpected failures: %+v", st)
	}
}

func int64ToUint(n int) uint64 { return uint64(n) }

func settledGoroutinesSSE() int {
	var n int
	for range 20 {
		runtime.GC()
		n = runtime.NumGoroutine()
		time.Sleep(25 * time.Millisecond)
		if runtime.NumGoroutine() == n {
			return n
		}
	}
	return n
}

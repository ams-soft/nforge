package cache

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeRedis speaks just enough RESP2 to drive failure scenarios that a
// real server cannot produce on demand (a reply lost after the command was
// received). Real command semantics are covered by redis_integration_test.go.
type fakeRedis struct {
	ln   net.Listener
	addr string

	mu       sync.Mutex
	commands map[string]int
	conns    []net.Conn
	// reply returns the RESP reply for a command, or "" to drop the
	// connection without replying.
	reply func(cmd []string) string
}

func newFakeRedis(t *testing.T, reply func(cmd []string) string) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{ln: ln, addr: ln.Addr().String(), commands: map[string]int{}, reply: reply}
	go f.serve()
	t.Cleanup(func() { _ = f.ln.Close() })
	return f
}

func (f *fakeRedis) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		go f.handle(conn)
	}
}

func (f *fakeRedis) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for {
		cmd, err := readCommand(r)
		if err != nil {
			return
		}
		name := strings.ToUpper(cmd[0])
		f.mu.Lock()
		f.commands[name]++
		f.mu.Unlock()
		var out string
		switch name {
		case "HELLO":
			out = "-ERR unknown command 'HELLO'\r\n" // forces the RESP2 fallback
		case "PING":
			out = "+PONG\r\n"
		default:
			out = f.reply(cmd)
		}
		if out == "" {
			return // drop: the command was received, the reply is lost
		}
		if _, err := io.WriteString(conn, out); err != nil {
			return
		}
	}
}

func (f *fakeRedis) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commands[name]
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, errors.New("not an array")
	}
	n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
	out := make([]string, 0, n)
	for range n {
		if _, err := r.ReadString('\n'); err != nil { // $len
			return nil, err
		}
		arg, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSuffix(arg, "\r\n"))
	}
	return out, nil
}

func redisConfigFor(addr string, mutate func(*Config)) Config {
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	cfg := Config{Enabled: true, Driver: DriverRedis, Redis: RedisConfig{Host: host, Port: p}}
	cfg.Redis.Timeouts.Operation = 2 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

func openRedis(t *testing.T, cfg Config, opts ...Option) *Cache {
	t.Helper()
	c, err := Open(bg, cfg, append([]Option{WithNamespace("test", "app")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(bg) })
	return c
}

func TestRedisMutationIsNotReplayedAfterLostReply(t *testing.T) {
	f := newFakeRedis(t, func(cmd []string) string { return "" }) // every data command loses its reply
	c := openRedis(t, redisConfigFor(f.addr, nil))

	_, err := c.Increment(bg, "n", 1)
	mustIs(t, err, ErrOutcomeUnknown)
	mustIs(t, err, ErrUnavailable)
	if n := f.count("INCRBY"); n != 1 {
		t.Fatalf("INCRBY sent %d times, want exactly 1", n)
	}

	ok, err := c.SetBytesIfAbsent(bg, "k", []byte("v"), time.Second)
	if ok || !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("a lost SET NX reply must not become false, nil: %v %v", ok, err)
	}
	for _, op := range []func() error{
		func() error { return c.SetBytes(bg, "k", []byte("v"), 0) },
		func() error { _, err := c.Delete(bg, "k"); return err },
		func() error { _, err := c.Expire(bg, "k", time.Second); return err },
	} {
		mustIs(t, op(), ErrOutcomeUnknown)
	}
	if f.count("SET") != 2 || f.count("DEL") != 1 || f.count("PEXPIRE") != 1 {
		t.Fatalf("mutations replayed: %v", f.commands)
	}
}

func TestRedisReadsAreRetriedWithinBudget(t *testing.T) {
	for _, retries := range []int{0, 1, 3} {
		t.Run(strconv.Itoa(retries), func(t *testing.T) {
			f := newFakeRedis(t, func(cmd []string) string { return "" })
			c := openRedis(t, redisConfigFor(f.addr, func(cfg *Config) {
				cfg.Redis.Retry.ReadRetries = &retries
				cfg.Redis.Retry.MinBackoff = time.Millisecond
				cfg.Redis.Retry.MaxBackoff = 2 * time.Millisecond
			}))
			_, err := c.GetBytes(bg, "k")
			mustIs(t, err, ErrUnavailable)
			if errors.Is(err, ErrOutcomeUnknown) {
				t.Fatal("a read is never 'outcome unknown'")
			}
			if n := f.count("GET"); n != retries+1 {
				t.Fatalf("GET sent %d times, want %d", n, retries+1)
			}
		})
	}
}

func TestRedisServerRepliesAreAuthoritative(t *testing.T) {
	f := newFakeRedis(t, func(cmd []string) string {
		switch strings.ToUpper(cmd[0]) {
		case "INCRBY":
			return "-ERR value is not an integer or out of range\r\n"
		case "GET":
			return "-NOPERM this user has no permissions to run the 'get' command with password hunter2\r\n"
		case "SET":
			return "$-1\r\n" // SET NX on an existing key
		}
		return "-ERR unexpected\r\n"
	})
	c := openRedis(t, redisConfigFor(f.addr, nil))

	_, err := c.Increment(bg, "n", 1)
	mustIs(t, err, ErrNotInteger)
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("a server rejection is not an unknown outcome")
	}
	_, err = c.GetBytes(bg, "k")
	mustIs(t, err, ErrUnavailable)
	var ce *Error
	if !errors.As(err, &ce) || ce.Reason != "auth" || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("auth error: %v", err)
	}
	if n := f.count("GET"); n != 1 {
		t.Fatalf("auth errors must not be retried: %d GETs", n)
	}
	if ok, err := c.SetBytesIfAbsent(bg, "k", []byte("v"), 0); ok || err != nil {
		t.Fatalf("existing key: %v %v", ok, err)
	}
}

func TestRedisStartupFailureAndRecovery(t *testing.T) {
	// reserve a port, then close it: connection refused
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cfg := redisConfigFor(addr, func(cfg *Config) { cfg.Redis.Timeouts.Dial = 200 * time.Millisecond })

	_, err = Open(bg, cfg, WithNamespace("e", "a"))
	mustIs(t, err, ErrUnavailable)
	if strings.Contains(err.Error(), addr) {
		t.Fatalf("error leaks the address: %v", err)
	}

	// Open with Redis up, take it down, bring it back on the same address:
	// the same Cache recovers without being recreated.
	f := newFakeRedisAt(t, addr, func(cmd []string) string { return "+OK\r\n" })
	c := openRedis(t, cfg)
	if err := c.SetBytes(bg, "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	f.stop()
	if err := c.Ping(bg); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ping while down: %v", err)
	}
	newFakeRedisAt(t, addr, func(cmd []string) string { return "+OK\r\n" })
	waitFor(t, func() bool { return c.Ping(bg) == nil })
	if err := c.SetBytes(bg, "k", []byte("v"), 0); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

// newFakeRedisAt listens on a fixed address (for the recovery test).
func newFakeRedisAt(t *testing.T, addr string, reply func(cmd []string) string) *fakeRedis {
	t.Helper()
	var ln net.Listener
	var err error
	for range 50 { // the port was just released; retry briefly
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{ln: ln, addr: addr, commands: map[string]int{}, reply: reply}
	go f.serve()
	t.Cleanup(func() { _ = f.ln.Close() })
	return f
}

// stop closes the listener and every open connection.
func (f *fakeRedis) stop() {
	_ = f.ln.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
}

func TestRedisOperationDeadline(t *testing.T) {
	block := make(chan struct{})
	f := newFakeRedis(t, func(cmd []string) string { <-block; return "+OK\r\n" })
	zero := 0
	c := openRedis(t, redisConfigFor(f.addr, func(cfg *Config) {
		cfg.Redis.Timeouts.Operation = 100 * time.Millisecond
		cfg.Redis.Timeouts.Read = 5 * time.Second
		cfg.Redis.Retry.ReadRetries = &zero
	}))
	t.Cleanup(func() { close(block) })
	start := time.Now()
	_, err := c.GetBytes(bg, "k")
	mustIs(t, err, ErrUnavailable)
	mustIs(t, err, context.DeadlineExceeded)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("operation timeout ignored: %v", d)
	}

	// an earlier caller deadline wins
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	err = c.SetBytes(ctx, "k", []byte("v"), 0)
	mustIs(t, err, ErrOutcomeUnknown)
	mustIs(t, err, context.DeadlineExceeded)
	if d := time.Since(start); d > 90*time.Millisecond {
		t.Fatalf("caller deadline ignored: %v", d)
	}
}

func TestClassify(t *testing.T) {
	live := context.Background()
	cases := []struct {
		name     string
		err      error
		mutation bool
		want     error
		unknown  bool
		reason   string
	}{
		{"nil reply", redis.Nil, false, ErrNotFound, false, ""},
		{"pool timeout", redis.ErrPoolTimeout, true, ErrUnavailable, false, "pool_timeout"},
		{"closed", redis.ErrClosed, true, ErrUnavailable, false, "closed"},
		{"dial", &net.OpError{Op: "dial", Err: errors.New("refused")}, true, ErrUnavailable, false, "connect"},
		{"eof read", io.EOF, false, ErrUnavailable, false, "connection"},
		{"eof write", io.EOF, true, ErrOutcomeUnknown, true, "connection"},
		{"overflow", redisErr("ERR increment or decrement would overflow"), true, ErrCounterOverflow, false, ""},
		{"wrongpass", redisErr("WRONGPASS invalid username-password pair"), false, ErrUnavailable, false, "auth"},
		{"expire", redisErr("ERR invalid expire time in 'set' command"), true, ErrInvalidTTL, false, "out_of_range"},
		{"other server error", redisErr("LOADING Redis is loading"), true, ErrUnavailable, false, "server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classify(live, tc.err, tc.mutation)
			mustIs(t, err, tc.want)
			if errors.Is(err, ErrOutcomeUnknown) != tc.unknown {
				t.Fatalf("unknown = %v", !tc.unknown)
			}
			var ce *Error
			errors.As(err, &ce)
			if ce.Reason != tc.reason {
				t.Fatalf("reason = %q", ce.Reason)
			}
		})
	}
}

type redisErr string

func (e redisErr) Error() string { return string(e) }
func (redisErr) RedisError()     {}

func TestRedisRetryableReasons(t *testing.T) {
	for reason, want := range map[string]bool{"connection": true, "timeout": true, "connect": true, "pool_timeout": true, "auth": false, "server": false} {
		if got := retryable(newError(ErrUnavailable, reason)); got != want {
			t.Errorf("%s: %v", reason, got)
		}
	}
	if retryable(newError(ErrNotFound, "")) {
		t.Error("not found is not retryable")
	}
}

func TestRedisPoolTimeoutIsUnsent(t *testing.T) {
	release := make(chan struct{})
	f := newFakeRedis(t, func(cmd []string) string { <-release; return "+OK\r\n" })
	c := openRedis(t, redisConfigFor(f.addr, func(cfg *Config) {
		cfg.Redis.Pool = RedisPoolConfig{Size: 1, MinIdleConns: 1, MaxIdleConns: 1, MaxActiveConns: 1}
		cfg.Redis.Timeouts.Pool = 50 * time.Millisecond
		cfg.Redis.Timeouts.Read = 5 * time.Second
	}))
	t.Cleanup(func() { close(release) }) // runs before the Shutdown registered by openRedis
	busy := make(chan error, 1)
	go func() { busy <- c.SetBytes(bg, "slow", []byte("v"), 0) }() // holds the only connection
	waitFor(t, func() bool { return f.count("SET") == 1 })

	_, err := c.Increment(bg, "n", 1)
	mustIs(t, err, ErrUnavailable)
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("a command that never left the pool is not an unknown outcome")
	}
	if f.count("INCRBY") != 0 {
		t.Fatal("INCRBY must not have been sent")
	}
}

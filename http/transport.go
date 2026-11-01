package nhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/ams-soft/nforge/config"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	shutdownTimeout = 10 * time.Second
	// redirectMaxHeaderBytes bounds the request headers of the redirect-only
	// listener, which never needs more than a request line and a Host.
	redirectMaxHeaderBytes = 16 << 10
)

// StartContext serves every enabled transport and blocks until ctx is done or
// one of them fails (Start passes a context canceled by SIGINT/SIGTERM).
//
// Startup is transactional: the certificates are loaded and every TCP and UDP
// listener is bound before any request is accepted, so a bad certificate or an
// occupied port leaves nothing running. All transports share the application
// handler (Srv.Handler); the redirect-only HTTP listener has its own handler
// and serves no application route or middleware.
//
// On the way out, in every case, the servers stop accepting and drain for up
// to 10 s (HTTP/3 sends GOAWAY and then closes its connections), and then
// Deps.Shutdown runs exactly once. ServerHttp.ShutdownFunc is not called.
func (s *ServerHttp) StartContext(ctx context.Context) error {
	cfg := s.Deps.Config.HTTP.Server
	config.ApplyServerDefaults(&cfg)
	run, err := s.prepare(ctx, cfg)
	if err != nil {
		s.Deps.Logger.ErrorContext(ctx, "[HTTPModule] Failed to start server", "err", err)
		return errors.Join(err, s.shutdownDeps())
	}

	errCh := make(chan error, len(run.servers))
	var wg sync.WaitGroup
	for _, srv := range run.servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Deps.Logger.InfoContext(ctx, "[HTTPModule] Server running", "transport", srv.name, "addr", srv.addr)
			if err := srv.serve(); err != nil {
				errCh <- fmt.Errorf("%s: %w", srv.name, err)
			}
		}()
	}

	var serveErr error
	select {
	case serveErr = <-errCh:
		s.Deps.Logger.ErrorContext(ctx, "[HTTPModule] Server failed", "err", serveErr)
	case <-ctx.Done():
		s.Deps.Logger.InfoContext(ctx, "[HTTPModule] Shutting down server...")
	}

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	stopErr := run.shutdown(sctx)
	wg.Wait()
	if stopErr != nil {
		s.Deps.Logger.ErrorContext(ctx, "[HTTPModule] Forced shutdown", "err", stopErr)
	} else if serveErr == nil {
		s.Deps.Logger.InfoContext(ctx, "[HTTPModule] Server stopped successfully")
	}

	// Only after every listener is released: close the cache, JOSE, database,
	// tracing and logs (LIFO). Logs from here on may be lost.
	return errors.Join(serveErr, stopErr, s.shutdownDeps())
}

func (s *ServerHttp) shutdownDeps() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return s.Deps.Shutdown(ctx)
}

// transport is one bound listener and how to serve and stop it.
type transport struct {
	name  string
	addr  string
	serve func() error
	stop  func(ctx context.Context) error
}

type running struct{ servers []transport }

func (r *running) shutdown(ctx context.Context) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for _, t := range r.servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := t.stop(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", t.name, err))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// closers collects what is bound so far, to release it if a later step fails.
type closers []func() error

func (c closers) closeAll() error {
	var errs []error
	for _, fn := range c {
		errs = append(errs, fn())
	}
	return errors.Join(errs...)
}

// prepare validates the configuration, loads the certificates and binds every
// listener. On any error nothing stays open.
func (s *ServerHttp) prepare(ctx context.Context, cfg config.ServerConfig) (_ *running, err error) {
	if err := config.ValidateServer(cfg); err != nil {
		return nil, fmt.Errorf("nhttp: %w", err)
	}
	var (
		lc      net.ListenConfig
		run     running
		bound   closers
		app     = s.Srv.Handler
		h3Certs []tls.Certificate
		tlsCert []tls.Certificate
	)
	defer func() {
		if err != nil {
			err = errors.Join(err, bound.closeAll())
		}
	}()

	if cfg.TLS.Enabled {
		c, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("nhttp: load tls certificate: %w", err)
		}
		tlsCert = []tls.Certificate{c}
	}
	if cfg.HTTP3.Enabled {
		certFile, keyFile := cfg.HTTP3Credentials()
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("nhttp: load http3 certificate: %w", err)
		}
		h3Certs = []tls.Certificate{c}
	}

	// HTTP/3 first: its Alt-Svc advertisement depends on it being bound.
	var h3 *http3.Server
	if cfg.HTTP3.Enabled {
		h3Addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.HTTP3.Port))
		pc, err := lc.ListenPacket(ctx, "udp", h3Addr)
		if err != nil {
			return nil, fmt.Errorf("nhttp: listen http3 (udp %s): %w", h3Addr, err)
		}
		bound = append(bound, pc.Close)
		h3 = &http3.Server{
			Addr:        h3Addr,
			Port:        cfg.HTTP3.Port,
			TLSConfig:   &tls.Config{Certificates: h3Certs, MinVersion: tls.VersionTLS13},
			QUICConfig:  &quic.Config{MaxIdleTimeout: cfg.HTTP3.MaxIdleTimeout, Allow0RTT: false},
			Handler:     streamTimeout(app, cfg.HTTP3.MaxStreamTimeout),
			IdleTimeout: cfg.IdleTimeout,
		}
		run.servers = append(run.servers, transport{
			name: "http3", addr: h3Addr,
			serve: func() error { return ignoreClosed(h3.Serve(pc)) },
			stop: func(ctx context.Context) error {
				err := h3.Shutdown(ctx)
				_ = pc.Close() // Serve(conn) does not own the socket; a second close is harmless
				return err
			},
		})
	}

	if cfg.TLS.Enabled {
		addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.TLS.Port))
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("nhttp: listen https (tcp %s): %w", addr, err)
		}
		bound = append(bound, ln.Close)
		handler := app
		if h3 != nil && cfg.HTTP3.AdvertiseAltSvc() {
			handler = altSvc(app, h3)
		}
		srv := s.newTCPServer(cfg, addr, handler)
		srv.TLSConfig = &tls.Config{Certificates: tlsCert, MinVersion: minTLS(cfg.TLS.MinVersion)}
		run.servers = append(run.servers, tcpTransport("https", addr, srv, func() error { return srv.ServeTLS(ln, "", "") }))
	}

	if cfg.HTTPListener() {
		addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("nhttp: listen http (tcp %s): %w", addr, err)
		}
		bound = append(bound, ln.Close)
		name, handler := "http", app
		if cfg.TLS.RedirectHTTP {
			authority, err := cfg.RedirectAuthority()
			if err != nil {
				return nil, fmt.Errorf("nhttp: %w", err)
			}
			name, handler = "http-redirect", redirectHandler(authority)
		}
		var srv *stdhttp.Server
		if name == "http" {
			srv = s.Srv // the exported Srv stays the application's plain HTTP server
		} else {
			srv = s.newTCPServer(cfg, addr, handler)
			srv.MaxHeaderBytes = redirectMaxHeaderBytes
		}
		run.servers = append(run.servers, tcpTransport(name, addr, srv, func() error { return srv.Serve(ln) }))
	}
	return &run, nil
}

func (s *ServerHttp) newTCPServer(cfg config.ServerConfig, addr string, h stdhttp.Handler) *stdhttp.Server {
	return &stdhttp.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

// tcpTransport drains with Shutdown and, if the deadline passes, Close.
func tcpTransport(name, addr string, srv *stdhttp.Server, serve func() error) transport {
	return transport{
		name: name, addr: addr,
		serve: func() error { return ignoreClosed(serve()) },
		stop: func(ctx context.Context) error {
			err := srv.Shutdown(ctx)
			if err != nil {
				err = errors.Join(err, srv.Close())
			}
			return err
		},
	}
}

func ignoreClosed(err error) error {
	// http3.Server.Serve also reports http.ErrServerClosed
	if errors.Is(err, stdhttp.ErrServerClosed) || errors.Is(err, quic.ErrServerClosed) {
		return nil
	}
	return err
}

func minTLS(v string) uint16 {
	if v == config.TLSVersion12 {
		return tls.VersionTLS12
	}
	return tls.VersionTLS13
}

// altSvc announces HTTP/3 on HTTPS responses. SetQUICHeaders yields nothing
// until the QUIC listener is registered, so there is never an advertisement
// for an endpoint that failed to bind. The header is set before the handler
// so an application may still override it.
func altSvc(next stdhttp.Handler, h3 *http3.Server) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = h3.SetQUICHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

// streamTimeout bounds one HTTP/3 request: its context expires after d and the
// stream's read and write deadlines are set to the same instant. A handler
// that legitimately runs longer extends them with http.ResponseController.
func streamTimeout(next stdhttp.Handler, d time.Duration) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		rc := stdhttp.NewResponseController(w)
		deadline := time.Now().Add(d)
		_ = rc.SetReadDeadline(deadline)
		_ = rc.SetWriteDeadline(deadline)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// redirectHandler answers every request with 308 to the same path and query on
// the fixed HTTPS authority. 308 keeps the method and body; the authority is
// configuration, never the Host header or a forwarded header.
func redirectHandler(authority string) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		target := url.URL{Scheme: "https", Host: authority, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
		if target.Path == "" || target.Path[0] != '/' {
			target.Path, target.RawPath = "/"+target.Path, ""
		}
		w.Header().Set("Location", target.String())
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(stdhttp.StatusPermanentRedirect)
	})
}

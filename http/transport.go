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

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	shutdownTimeout = 10 * time.Second
	// redirectMaxHeaderBytes bounds the request headers of the redirect-only
	// listener, which never needs more than a request line and a Host.
	redirectMaxHeaderBytes = 16 << 10
)

// Start binds every enabled transport (plain HTTP, HTTPS, HTTP/3) and serves
// them in the background; it returns once they are all bound, so the
// application is reachable when it returns. Start is transactional: the
// certificates are loaded and every TCP and UDP listener is bound before any
// request is accepted, so a bad certificate or an occupied port leaves
// nothing running. All transports share the application handler (Srv.Handler);
// the redirect-only HTTP listener has its own handler and serves no
// application route or middleware. A listener that fails later is reported to
// Dependencies.Failed (the module turns it into an application failure).
// Start does not handle signals: app.Run does.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		return errors.New("nhttp: server already started")
	}
	if err := s.checkFileMounts(); err != nil {
		s.logger.ErrorContext(ctx, "[HTTPModule] Failed to start server", "err", err)
		return err
	}
	cfg := s.cfg.Server
	ApplyServerDefaults(&cfg)
	run, err := s.prepare(ctx, cfg)
	if err != nil {
		s.logger.ErrorContext(ctx, "[HTTPModule] Failed to start server", "err", err)
		return err
	}
	s.run = run
	for _, srv := range run.servers {
		s.wg.Go(func() {
			s.logger.InfoContext(ctx, "[HTTPModule] Server running", "transport", srv.name, "addr", srv.addr)
			if err := srv.serve(); err != nil {
				err = fmt.Errorf("nhttp: %s: %w", srv.name, err)
				s.logger.ErrorContext(ctx, "[HTTPModule] Server failed", "err", err)
				if s.failed != nil {
					s.failed(err)
				}
			}
		})
	}
	return nil
}

// Stop stops every transport: they stop accepting, drain the requests in
// flight for up to 10 s or until ctx ends (HTTP/3 sends GOAWAY and then closes
// its connections), are then closed, and Stop waits for the serve loops. Only
// the first call does anything; a server never started stops at once.
func (s *Server) Stop(ctx context.Context) error {
	var err error
	s.stop.Do(func() {
		defer s.closeFiles()
		s.mu.Lock()
		run := s.run
		s.mu.Unlock()
		if run == nil {
			return
		}
		if d := s.cfg.Server.ShutdownDelay; d > 0 {
			s.draining.Store(true)
			s.logger.InfoContext(ctx, "[HTTPModule] Draining: still serving, not ready", "delay", d)
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
			}
		}
		s.logger.InfoContext(ctx, "[HTTPModule] Shutting down server...")
		sctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
		err = run.shutdown(sctx)
		s.wg.Wait()
		if err != nil {
			s.logger.ErrorContext(ctx, "[HTTPModule] Forced shutdown", "err", err)
			return
		}
		s.logger.InfoContext(ctx, "[HTTPModule] Server stopped successfully")
	})
	return err
}

// Reload re-reads the TLS certificate files now (the module's Reload hook, run
// on SIGHUP by app.Run), so a rotation needs no restart and drops no
// connection. A pair that fails to load keeps the previous certificate; the
// errors carry no path or key material.
func (s *Server) Reload(ctx context.Context) error {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	if run == nil {
		return nil
	}
	var errs []error
	for _, rl := range run.reloaders {
		if err := rl.Reload(); err != nil {
			s.logger.WarnContext(ctx, "[HTTPModule] TLS certificate reload failed; keeping the previous one", "error", err)
			errs = append(errs, err)
			continue
		}
		s.logger.InfoContext(ctx, "[HTTPModule] TLS certificate reloaded")
	}
	return errors.Join(errs...)
}

// transport is one bound listener and how to serve and stop it.
type transport struct {
	name  string
	addr  string
	serve func() error
	stop  func(ctx context.Context) error
}

type running struct {
	servers   []transport
	reloaders []*certReloader
}

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
func (s *Server) prepare(ctx context.Context, cfg ServerConfig) (_ *running, err error) {
	if err := ValidateServer(cfg); err != nil {
		return nil, fmt.Errorf("nhttp: %w", err)
	}
	var (
		lc      net.ListenConfig
		run     running
		bound   closers
		app     = s.Srv.Handler
		tlsCert *certReloader
		h3Cert  *certReloader
	)
	defer func() {
		if err != nil {
			err = errors.Join(err, bound.closeAll())
		}
	}()

	warnReload := func(err error) {
		s.logger.WarnContext(ctx, "[HTTPModule] TLS certificate reload failed; keeping the previous one", "error", err)
	}
	if cfg.TLS.Enabled {
		c, err := newCertReloader(cfg.TLS.CertFile, cfg.TLS.KeyFile, warnReload)
		if err != nil {
			return nil, fmt.Errorf("nhttp: %w", err)
		}
		tlsCert = c
		run.reloaders = append(run.reloaders, c)
	}
	if cfg.HTTP3.Enabled {
		certFile, keyFile := cfg.HTTP3Credentials()
		if tlsCert != nil && certFile == cfg.TLS.CertFile && keyFile == cfg.TLS.KeyFile {
			h3Cert = tlsCert // the same pair: one reloader, one reload
		} else {
			c, err := newCertReloader(certFile, keyFile, warnReload)
			if err != nil {
				return nil, fmt.Errorf("nhttp: http3: %w", err)
			}
			h3Cert = c
			run.reloaders = append(run.reloaders, c)
		}
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
			Addr:           h3Addr,
			Port:           cfg.HTTP3.Port,
			TLSConfig:      &tls.Config{GetCertificate: h3Cert.GetCertificate, MinVersion: tls.VersionTLS13},
			QUICConfig:     &quic.Config{MaxIdleTimeout: cfg.HTTP3.MaxIdleTimeout, Allow0RTT: false},
			Handler:        streamTimeout(app, cfg.HTTP3.MaxStreamTimeout),
			IdleTimeout:    cfg.IdleTimeout,
			MaxHeaderBytes: s.Srv.MaxHeaderBytes,
			Logger:         s.logger,
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
		srv.TLSConfig = &tls.Config{GetCertificate: tlsCert.GetCertificate, MinVersion: minTLS(cfg.TLS.MinVersion)}
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

// newTCPServer builds the HTTPS (or redirect) server. http.Server holds locks
// and cannot be copied, so the settings an application may have put on the
// exported Srv before Start (header limit, error log, connection hooks,
// protocol and HTTP/2 settings) are carried over one by one: the application
// configures Srv once and every transport behaves the same.
func (s *Server) newTCPServer(cfg ServerConfig, addr string, h stdhttp.Handler) *stdhttp.Server {
	return &stdhttp.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,

		MaxHeaderBytes:               s.Srv.MaxHeaderBytes,
		ErrorLog:                     s.Srv.ErrorLog,
		ConnState:                    s.Srv.ConnState,
		BaseContext:                  s.Srv.BaseContext,
		ConnContext:                  s.Srv.ConnContext,
		DisableGeneralOptionsHandler: s.Srv.DisableGeneralOptionsHandler,
		Protocols:                    s.Srv.Protocols,
		HTTP2:                        s.Srv.HTTP2,
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
	if v == TLSVersion12 {
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
// that legitimately runs longer extends them with http.ResponseController, and
// a long-lived stream releases all of it with BeginStream.
func streamTimeout(next stdhttp.Handler, d time.Duration) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		ctx, ex := withExemptions(r.Context())
		rd, cancel := newDeadlineCtx(ctx, d)
		defer cancel()
		ex.add(rd.release)
		rc := stdhttp.NewResponseController(w)
		deadline := rd.deadline
		_ = rc.SetReadDeadline(deadline)
		_ = rc.SetWriteDeadline(deadline)
		next.ServeHTTP(w, r.WithContext(rd))
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

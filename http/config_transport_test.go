package nhttp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testPair writes a throwaway key pair and returns its paths.
func testPair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "c.crt"), filepath.Join(dir, "c.key")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600)
	return certFile, keyFile
}

func TestMissingServerSectionUsesDefaults(t *testing.T) {
	cfg, err := loadTenantConfig(t, "", "") // no http section at all
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.HTTP.Server
	if s.Port != 8080 || s.TLS.Port != 8443 || s.HTTP3.Port != 8443 {
		t.Errorf("ports = %d/%d/%d, want 8080/8443/8443", s.Port, s.TLS.Port, s.HTTP3.Port)
	}
	if s.TLS.Enabled || s.HTTP3.Enabled || s.TLS.MinVersion != "1.3" {
		t.Errorf("tls/http3 must be off by default with min 1.3: %+v %+v", s.TLS, s.HTTP3)
	}
	if !s.HTTP3.SameKeyCert() || !s.HTTP3.AdvertiseAltSvc() ||
		s.HTTP3.MaxIdleTimeout != 30*time.Second || s.HTTP3.MaxStreamTimeout != 30*time.Second || s.HTTP3.Allow0RTT {
		t.Errorf("http3 defaults: %+v", s.HTTP3)
	}
	if got := cfg.HTTP.Docs.Hosts; len(got) != 1 || got[0] != "http://localhost:8080" {
		t.Errorf("docs hosts = %v", got)
	}
}

func TestTransportPortsDefaultIndependently(t *testing.T) {
	cert, key := testPair(t)
	cfg, err := loadTenantConfig(t, "http: {server: {tls: {enabled: true, port: 9443, cert_file: "+cert+", key_file: "+key+"}, http3: {enabled: true}}}", "")
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.HTTP.Server; s.TLS.Port != 9443 || s.HTTP3.Port != 8443 || s.Port != 8080 {
		t.Fatalf("ports = %d/%d/%d, want 8080/9443/8443", s.Port, s.TLS.Port, s.HTTP3.Port)
	}
}

func TestTransportBoolOverridesSurvive(t *testing.T) {
	cert, key := testPair(t)
	yaml := "http: {server: {tls: {enabled: true, cert_file: " + cert + ", key_file: " + key + "}, http3: {enabled: true}}}"
	cfg, err := loadTenantConfig(t, yaml, "HTTP_SERVER_HTTP3_ADVERTISE=false\nHTTP_SERVER_PORT=9090\nHTTP_SERVER_TLS_PORT=9444\nHTTP_SERVER_HTTP3_PORT=9445\n")
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.HTTP.Server
	if s.HTTP3.AdvertiseAltSvc() || !s.HTTP3.SameKeyCert() || s.Port != 9090 || s.TLS.Port != 9444 || s.HTTP3.Port != 9445 {
		t.Errorf("env file overrides: %+v %+v port=%d", s.TLS, s.HTTP3, s.Port)
	}
	// same_keycert_tls: false from YAML needs its own pair; false must stay false
	c2, k2 := testPair(t)
	cfg, err = loadTenantConfig(t, "http: {server: {http3: {enabled: true, same_keycert_tls: false, cert_file: "+c2+", key_file: "+k2+"}}}", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Server.HTTP3.SameKeyCert() {
		t.Error("explicit same_keycert_tls: false was replaced by the default")
	}
}

func TestDocsHostsFollowTransport(t *testing.T) {
	cert, key := testPair(t)
	tlsOn := "tls: {enabled: true, cert_file: " + cert + ", key_file: " + key
	for yaml, want := range map[string]string{
		"http: {server: {" + tlsOn + "}}}": "http://localhost:8080",
		"http: {server: {host: 0.0.0.0, " + tlsOn + ", redirect_http: true, redirect_host: example.com}}}": "https://localhost:8443",
		"http: {server: {" + tlsOn + ", disable_insecure_http: true}}}":                                    "https://localhost:8443",
	} {
		cfg, err := loadTenantConfig(t, yaml, "")
		if err != nil {
			t.Fatalf("%s: %v", yaml, err)
		}
		// host is empty or wildcard here, so the docs default uses localhost
		if got := cfg.HTTP.Docs.Hosts[0]; got != want {
			t.Errorf("%s: docs host = %s, want %s", yaml, got, want)
		}
	}
}

func TestValidateServerTransports(t *testing.T) {
	cert, key := testPair(t)
	otherCert, _ := testPair(t)
	badKey := filepath.Join(t.TempDir(), "bad.key")
	_ = os.WriteFile(badKey, []byte("not pem"), 0o600)
	tlsOK := ServerTLSConfig{Enabled: true, CertFile: cert, KeyFile: key}
	with := func(f func(*ServerConfig)) ServerConfig {
		s := ServerConfig{TLS: tlsOK}
		f(&s)
		return s
	}
	cases := map[string]struct {
		cfg  ServerConfig
		want string // "" = valid
	}{
		"http only":        {ServerConfig{}, ""},
		"tls ok":           {with(func(*ServerConfig) {}), ""},
		"port negative":    {ServerConfig{Port: -1}, "http.server.port"},
		"port too big":     {ServerConfig{Port: 70000}, "http.server.port"},
		"tls port big":     {with(func(s *ServerConfig) { s.TLS.Port = 65536 }), "tls.port"},
		"http3 port big":   {ServerConfig{HTTP3: ServerHTTP3Config{Port: 99999}}, "http3.port"},
		"min 1.1":          {with(func(s *ServerConfig) { s.TLS.MinVersion = "1.1" }), "min_version"},
		"min 1.0":          {with(func(s *ServerConfig) { s.TLS.MinVersion = "1.0" }), "min_version"},
		"min 1.2 ok":       {with(func(s *ServerConfig) { s.TLS.MinVersion = "1.2" }), ""},
		"tls without cert": {ServerConfig{TLS: ServerTLSConfig{Enabled: true}}, "cert_file and key_file are both required"},
		"tls partial pair": {ServerConfig{TLS: ServerTLSConfig{Enabled: true, CertFile: cert}}, "both required"},
		"tls bad key":      {ServerConfig{TLS: ServerTLSConfig{Enabled: true, CertFile: cert, KeyFile: badKey}}, "cannot load the certificate"},
		"tls mismatched":   {ServerConfig{TLS: ServerTLSConfig{Enabled: true, CertFile: otherCert, KeyFile: key}}, "cannot load the certificate"},
		"tls missing file": {ServerConfig{TLS: ServerTLSConfig{Enabled: true, CertFile: cert + ".x", KeyFile: key}}, "cannot load the certificate"},
		"redirect+disable": {with(func(s *ServerConfig) { s.TLS.RedirectHTTP, s.TLS.DisableInsecureHTTP, s.Host = true, true, "127.0.0.1" }), "cannot both be true"},
		"redirect no tls":  {ServerConfig{TLS: ServerTLSConfig{RedirectHTTP: true}}, "requires http.server.tls.enabled"},
		"redirect no host": {with(func(s *ServerConfig) { s.TLS.RedirectHTTP = true }), "redirect_host is required"},
		"redirect wildcard host": {with(func(s *ServerConfig) {
			s.TLS.RedirectHTTP, s.TLS.RedirectHost = true, "0.0.0.0"
		}), "wildcard"},
		"redirect host with port": {with(func(s *ServerConfig) {
			s.TLS.RedirectHTTP, s.TLS.RedirectHost = true, "a.com:443"
		}), "without scheme, port or path"},
		"redirect host with path": {with(func(s *ServerConfig) {
			s.TLS.RedirectHTTP, s.TLS.RedirectHost = true, "a.com/x"
		}), "without scheme"},
		"redirect loopback ok": {with(func(s *ServerConfig) { s.TLS.RedirectHTTP, s.Host = true, "127.0.0.1" }), ""},
		"redirect named ok": {with(func(s *ServerConfig) {
			s.TLS.RedirectHTTP, s.TLS.RedirectHost = true, "api.example.com"
		}), ""},
		"no transport":   {ServerConfig{TLS: ServerTLSConfig{DisableInsecureHTTP: true}}, "no transport"},
		"tcp collision":  {with(func(s *ServerConfig) { s.Port, s.TLS.Port = 9000, 9000 }), "both 9000 on TCP"},
		"collision moot": {with(func(s *ServerConfig) { s.Port, s.TLS.Port, s.TLS.DisableInsecureHTTP = 9000, 9000, true }), ""},
		"udp+tcp same ok": {with(func(s *ServerConfig) {
			s.TLS.Port, s.HTTP3 = 8443, ServerHTTP3Config{Enabled: true, Port: 8443}
		}), ""},
		"0rtt rejected": {with(func(s *ServerConfig) { s.HTTP3 = ServerHTTP3Config{Enabled: true, Allow0RTT: true} }), "allow_0rtt"},
		"h3 negative idle": {with(func(s *ServerConfig) {
			s.HTTP3 = ServerHTTP3Config{Enabled: true, MaxIdleTimeout: -time.Second}
		}), "timeouts must be positive"},
		"h3 reuse without tls cert": {ServerConfig{HTTP3: ServerHTTP3Config{Enabled: true}}, "reused by http3"},
		"h3 reuse tls off ok": {ServerConfig{TLS: ServerTLSConfig{CertFile: cert, KeyFile: key},
			HTTP3: ServerHTTP3Config{Enabled: true}}, ""},
		"h3 reuse + own files": {with(func(s *ServerConfig) { s.HTTP3 = ServerHTTP3Config{Enabled: true, CertFile: cert} }), "conflict with same_keycert_tls"},
		"h3 separate ok":       {ServerConfig{HTTP3: ServerHTTP3Config{Enabled: true, SameKeyCertTLS: new(false), CertFile: cert, KeyFile: key}}, ""},
		"h3 separate missing":  {with(func(s *ServerConfig) { s.HTTP3 = ServerHTTP3Config{Enabled: true, SameKeyCertTLS: new(false)} }), "http.server.http3: cert_file"},
		"h3 only no http ok": {ServerConfig{TLS: ServerTLSConfig{DisableInsecureHTTP: true},
			HTTP3: ServerHTTP3Config{Enabled: true, SameKeyCertTLS: new(false), CertFile: cert, KeyFile: key}}, ""},
	}
	for name, tc := range cases {
		err := ValidateServer(tc.cfg)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestRedirectAuthority(t *testing.T) {
	for _, tc := range []struct {
		host, redirect string
		port           int
		want           string
	}{
		{"127.0.0.1", "", 8443, "127.0.0.1:8443"},
		{"localhost", "", 0, "localhost:8443"},
		{"::1", "", 8443, "[::1]:8443"},
		{"0.0.0.0", "example.com", 443, "example.com"},
		{"", "2001:db8::1", 443, "[2001:db8::1]"},
		{"", "api.example.com", 9443, "api.example.com:9443"},
	} {
		s := ServerConfig{Host: tc.host, TLS: ServerTLSConfig{Port: tc.port, RedirectHost: tc.redirect}}
		got, err := s.RedirectAuthority()
		if err != nil || got != tc.want {
			t.Errorf("%+v → %q, %v; want %q", tc, got, err, tc.want)
		}
	}
	if _, err := (ServerConfig{Host: "0.0.0.0"}).RedirectAuthority(); err == nil {
		t.Error("a wildcard bind address must not become a redirect host")
	}
}

func TestLoadRejectsBadTransportConfig(t *testing.T) {
	cert, key := testPair(t)
	_, err := loadTenantConfig(t, "http: {server: {tls: {enabled: true, cert_file: "+cert+", key_file: "+key+", min_version: '1.1'}}}", "")
	if err == nil || !strings.Contains(err.Error(), "min_version") {
		t.Fatalf("err = %v", err)
	}
	_, err = loadTenantConfig(t, "http: {server: {port: 70000}}", "")
	if err == nil || !strings.Contains(err.Error(), "http.server.port") {
		t.Fatalf("err = %v", err)
	}
}

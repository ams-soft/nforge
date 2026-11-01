package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const baseYAML = "app: {name: a, version: '1'}\n"

func writeEnv(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A typo in any section must fail the load (G-02): http.cors.origns used to
// leave origins empty, which means "any origin".
func TestUnknownYAMLKeysAreRejected(t *testing.T) {
	cases := map[string]string{
		"security.enabld":        baseYAML + "http: {security: {enabld: true}}\n",
		"cors.origns":            baseYAML + "http: {cors: {enabled: true, origns: ['https://a.example']}}\n",
		"cors.credentails":       baseYAML + "http: {cors: {enabled: true, origins: ['https://a.example'], credentails: true}}\n", //nolint:misspell // deliberate typo
		"tls.min_verison":        baseYAML + "http: {server: {tls: {min_verison: '1.2'}}}\n",                                      //nolint:misspell // deliberate typo
		"tenancy.modee":          baseYAML + "database: {tenancy: {modee: rls}}\n",
		"top level":              baseYAML + "htpp: {server: {port: 1}}\n",
		"codec.strict_unknown":   baseYAML + "codec: {strict_unknown: true}\n",
		"logging.output.termnal": baseYAML + "logging: {output: {termnal: {enabled: true}}}\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFlags(writeConfig(t, text), "")
			if err == nil {
				t.Fatal("want an error for the misspelt key")
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("the error must name the field: %v", err)
			}
		})
	}
}

func TestExtensionKeysAreIgnored(t *testing.T) {
	text := baseYAML + "x-feature-flags:\n  beta: true\nx-team: payments\n"
	cfg, err := LoadFlags(writeConfig(t, text), "")
	if err != nil {
		t.Fatalf("top-level x- keys are for the application: %v", err)
	}
	if cfg.App.Name != "a" {
		t.Fatalf("app = %+v", cfg.App)
	}
	// only at the top level
	if _, err := LoadFlags(writeConfig(t, baseYAML+"http: {x-extra: 1}\n"), ""); err == nil {
		t.Fatal("x- keys are allowed only at the top level")
	}
}

func TestUnknownEnvFileKeysAreRejected(t *testing.T) {
	yaml := writeConfig(t, baseYAML)
	for name, text := range map[string]string{
		"typo":       "HTTP_CORS_ORIGNS=https://a.example\n",
		"lower case": "http_server_port=9090\n",
		"stale":      "SOME_OTHER_TOOL_VAR=1\n",
		"mixed":      "HTTP_SERVER_PORT=9090\nHTTP_SERVER_PROT=1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFlags(yaml, writeEnv(t, text))
			if err == nil || !strings.Contains(err.Error(), "unknown keys") {
				t.Fatalf("want an unknown keys error, got %v", err)
			}
		})
	}
	cfg, err := LoadFlags(yaml, writeEnv(t, "HTTP_SERVER_PORT=9090\nDATABASE_POOL_MAX_CONNS=5\n"))
	if err != nil {
		t.Fatalf("valid keys: %v", err)
	}
	if cfg.HTTP.Server.Port != 9090 || cfg.Database.Pool.MaxConns != 5 {
		t.Fatalf("env values not applied: %+v", cfg.HTTP.Server)
	}
	// load-time fields cannot be set from the file
	if _, err := LoadFlags(yaml, writeEnv(t, "WARNINGS=x\nAPP_ONLINESINCE=y\n")); err == nil {
		t.Fatal("load-time fields are not configuration")
	}
}

// env decides the overlay and the log level, so a misspelling must not pass (G-16).
func TestEnvIsValidatedAndNormalized(t *testing.T) {
	for _, bad := range []string{"production", "prod", "development", "test", "stage"} {
		_, err := LoadFlags(writeConfig(t, "env: "+`"`+bad+`"`+"\n"+baseYAML), "")
		if err == nil || !strings.Contains(err.Error(), "env must be one of") {
			t.Errorf("env %q must be rejected, got %v", bad, err)
		}
	}
	for in, want := range map[string]string{"PRD": "prd", "Dev": "dev", "stg": "stg", "local": "local"} {
		cfg, err := LoadFlags(writeConfig(t, "env: "+in+"\n"+baseYAML), "")
		if err != nil {
			t.Fatalf("env %q: %v", in, err)
		}
		if cfg.Env != want {
			t.Errorf("env %q normalized to %q, want %q", in, cfg.Env, want)
		}
	}
	// env can come from the env file; it is validated there too
	if _, err := LoadFlags(writeConfig(t, baseYAML), writeEnv(t, "ENV=production\n")); err == nil {
		t.Error("env from the env file must be validated")
	}
}

// A missing overlay is skipped; an unreadable one is an error (G-25).
func TestOverlayErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	yaml := writeConfig(t, "env: prd\n"+baseYAML)
	if _, err := LoadFlags(yaml, ""); err != nil {
		t.Fatalf("no overlay file is fine: %v", err)
	}
	if err := os.WriteFile("config.prd.yaml", []byte("codec: {strict_unknown_fields: true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFlags(yaml, "")
	if err != nil || !cfg.Codec.StrictUnknownFields {
		t.Fatalf("overlay must apply: %v %+v", err, cfg)
	}
	if err := os.WriteFile("config.prd.yaml", []byte("codec: {strict_unknown_field: true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFlags(yaml, ""); err == nil {
		t.Fatal("a typo in the overlay must fail too")
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(dir, 0o000); err == nil {
			defer func() { _ = os.Chmod(dir, 0o700) }()
			if _, err := LoadFlags(yaml, ""); err == nil {
				t.Fatal("an overlay that cannot be statted must be an error, not silently skipped")
			}
		}
	}
}

// -config naming a file that does not exist is an error; the default
// ./config.yaml may be absent (G-13).
func TestExplicitConfigMustExist(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	missing := filepath.Join(dir, "nope.yaml")
	if _, err := LoadFlags(missing, ""); err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Fatalf("a missing -config file must fail with its name: %v", err)
	}
	// default path, absent: configuration comes from the env file and defaults
	env := writeEnv(t, "APP_NAME=a\nAPP_VERSION=1\n")
	if _, err := LoadFlags("", env); err != nil {
		t.Fatalf("absent default config.yaml is allowed: %v", err)
	}
}

// An unset ${VAR} is an error naming every missing variable, unless it carries
// a default (G-13).
func TestUnsetEnvVariablesAreAnError(t *testing.T) {
	t.Setenv("NF_TEST_SET", "x")
	t.Setenv("NF_TEST_EMPTY", "")
	text := baseYAML + "http: {docs: {title: '${NF_TEST_SET}-${NF_TEST_UNSET_A}', version: '${NF_TEST_UNSET_B}${NF_TEST_UNSET_A}'}}\n"
	_, err := LoadFlags(writeConfig(t, text), "")
	if err == nil || !strings.Contains(err.Error(), "NF_TEST_UNSET_A, NF_TEST_UNSET_B") || strings.Contains(err.Error(), "NF_TEST_SET") {
		t.Fatalf("err = %v", err)
	}

	text = baseYAML + "http: {docs: {title: '${NF_TEST_SET:-d}-${NF_TEST_UNSET_A:-fallback}-${NF_TEST_UNSET_B:-}', version: '${NF_TEST_EMPTY}${NF_TEST_EMPTY:-v}'}}\n"
	cfg, err := LoadFlags(writeConfig(t, text), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Docs.Title != "x-fallback-" || cfg.HTTP.Docs.Version != "v" {
		t.Errorf("title = %q, version = %q", cfg.HTTP.Docs.Title, cfg.HTTP.Docs.Version)
	}
}

// "$" is an ordinary character in env-file values: godotenv used to expand
// $NAME from earlier keys of the file (an unknown name became empty), so
// "pa$SWORD" lost a piece of the password (G-21).
func TestEnvFileValuesAreLiteral(t *testing.T) {
	env := writeEnv(t, strings.Join([]string{
		"DATABASE_PASSWORD=pa$SWORD$HOME",
		`DATABASE_HOST="h$USER"`,
		"DATABASE_NAME='lit$X'",
		"DATABASE_USER=${APP_NAME}",
		"APP_NAME=a",
		"APP_VERSION=1 # a comment is still a comment",
		"export CACHE_PREFIX=cost$$",
		"# DATABASE_PORT=1 is commented out",
		`HTTP_DOCS_TITLE="a \"quoted\" $T"`,
		"",
	}, "\n"))
	cfg, err := LoadFlags("", env)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"password":   {cfg.Database.Password, "pa$SWORD$HOME"},
		"host":       {cfg.Database.Host, "h$USER"},
		"name":       {cfg.Database.Name, "lit$X"},
		"user":       {cfg.Database.User, "${APP_NAME}"},
		"version":    {cfg.App.Version, "1"},
		"prefix":     {cfg.Cache.Prefix, "cost$$"},
		"docs title": {cfg.HTTP.Docs.Title, `a "quoted" $T`},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if cfg.Database.Port != 0 {
		t.Errorf("a commented key must not be read: port %d", cfg.Database.Port)
	}
	if _, err := LoadFlags("", writeEnv(t, "APP_NAME=a\x00b\n")); err == nil {
		t.Error("a NUL byte must be rejected")
	}
}

func TestHandlerTimeoutAndBodyLimitSettings(t *testing.T) {
	cfg, err := LoadFlags(writeConfig(t, baseYAML), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Server.HandlerTimeout != 30*time.Second || cfg.HTTP.Server.MaxBodyBytes != 0 {
		t.Fatalf("defaults: %+v", cfg.HTTP.Server)
	}
	cfg, err = LoadFlags(writeConfig(t, baseYAML+"http: {server: {handler_timeout: 5s, max_body_bytes: 2mb}}\n"), "")
	if err != nil || cfg.HTTP.Server.HandlerTimeout != 5*time.Second || cfg.HTTP.Server.MaxBodyBytes != 2*MB {
		t.Fatalf("configured: %v %+v", err, cfg.HTTP.Server)
	}
	for _, bad := range []string{"handler_timeout: -1s", "max_body_bytes: 2gb"} {
		if _, err := LoadFlags(writeConfig(t, baseYAML+"http: {server: {"+bad+"}}\n"), ""); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	// reachable from the env file like every other field
	cfg, err = LoadFlags(writeConfig(t, baseYAML), writeEnv(t, "HTTP_SERVER_HANDLER_TIMEOUT=7s\nDATABASE_CONNECTION_STATEMENT_TIMEOUT=45s\n"))
	if err != nil || cfg.HTTP.Server.HandlerTimeout != 7*time.Second || cfg.Database.Connection.StatementTimeout != 45*time.Second {
		t.Fatalf("env file: %v %+v", err, cfg.HTTP.Server)
	}
}

func TestTracingSampleRatioAndTrust(t *testing.T) {
	cfg, err := LoadFlags(writeConfig(t, baseYAML), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracing.Otel.Ratio() != 1 || cfg.Tracing.Otel.TrustsIncoming() {
		t.Fatalf("defaults must keep every request and distrust the caller: %+v", cfg.Tracing.Otel)
	}
	cfg, err = LoadFlags(writeConfig(t, baseYAML+"tracing: {otel: {sample_ratio: 0.1, trust_incoming: true}}\n"), "")
	if err != nil || cfg.Tracing.Otel.Ratio() != 0.1 || !cfg.Tracing.Otel.TrustsIncoming() {
		t.Fatalf("configured: %v %+v", err, cfg.Tracing.Otel)
	}
	if cfg, err = LoadFlags(writeConfig(t, baseYAML+"tracing: {otel: {sample_ratio: 0}}\n"), ""); err != nil || cfg.Tracing.Otel.Ratio() != 0 {
		t.Fatalf("an explicit 0 must be kept: %v", err)
	}
	for _, bad := range []string{"-0.1", "1.5", ".nan"} {
		if _, err := LoadFlags(writeConfig(t, baseYAML+"tracing: {otel: {sample_ratio: "+bad+"}}\n"), ""); err == nil {
			t.Errorf("sample_ratio %s must be refused", bad)
		}
	}
	if cfg, err = LoadFlags(writeConfig(t, baseYAML), writeEnv(t, "TRACING_OTEL_SAMPLE_RATIO=0.5\nTRACING_OTEL_TRUST_INCOMING=true\n")); err != nil ||
		cfg.Tracing.Otel.Ratio() != 0.5 || !cfg.Tracing.Otel.TrustsIncoming() {
		t.Fatalf("env file: %v %+v", err, cfg.Tracing.Otel)
	}
}

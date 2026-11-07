package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const baseYAML = "app: {name: a, version: '1'}\n"

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeEnv(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// server is a module schema, as a module would define it.
type server struct {
	Port    int           `yaml:"port"`
	Host    string        `yaml:"host"`
	Timeout time.Duration `yaml:"timeout"`
	Origins []string      `yaml:"origins"`
	Enabled *bool         `yaml:"enabled"`
	TLS     struct {
		KeyFile string `yaml:"key_file"`
	} `yaml:"tls"`
	Keys []struct {
		Kid string `yaml:"kid"`
	} `yaml:"keys"`
	Labels map[string]string `yaml:"labels"`
}

type store struct {
	Password string `yaml:"password"`
	Host     string `yaml:"host"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Port     int    `yaml:"port"`
}

func load(t *testing.T, yaml, env string) (*Document, error) {
	t.Helper()
	envPath := ""
	if env != "" {
		envPath = writeEnv(t, env)
	}
	return LoadFlags(writeConfig(t, yaml), envPath)
}

func decode[T any](t *testing.T, b *Binding, section string) T {
	t.Helper()
	var v T
	if err := b.Decode(section, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReservedSections(t *testing.T) {
	d, err := load(t, "env: Dev\napp: {name: svc, version: 1.2.3, internal_use: true}\nprd_exceptions: {redis_plaintext: true}\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Env() != "dev" || d.App() != (AppConfig{Name: "svc", Version: "1.2.3", InternalUse: true}) {
		t.Fatalf("env %q app %+v", d.Env(), d.App())
	}
	if !d.PrdExceptions().Allows("redis_plaintext") || d.PrdExceptions().Allows("database_plaintext") || d.PrdExceptions().Allows("nope") {
		t.Fatalf("exceptions = %+v", d.PrdExceptions())
	}
	if time.Since(d.OnlineSince()) > time.Minute {
		t.Fatal("online since")
	}
	if len(d.Sections()) != 0 {
		t.Fatalf("reserved sections are not module sections: %v", d.Sections())
	}
	for _, bad := range []string{"app: {name: a}\n", "app: {version: '1'}\n"} {
		if _, err := load(t, bad, ""); err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if _, err := load(t, baseYAML+"prd_exceptions: {redis_plain: true}\n", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown exception: err = %v", err)
	}
	b := d.Bind()
	if err := b.Decode("app", &AppConfig{}); err == nil {
		t.Error("a module cannot own a reserved section")
	}
}

// A typo in any section must fail: http.cors.origns used to leave origins
// empty, which means "any origin".
func TestUnknownKeysAreRejected(t *testing.T) {
	cases := map[string]string{
		"nested key": baseYAML + "server: {tls: {key_fil: x}}\n",
		"leaf key":   baseYAML + "server: {origns: [a]}\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := load(t, text, "")
			if err != nil {
				t.Fatal(err)
			}
			err = d.Bind().Decode("server", &server{})
			if err == nil || !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "server") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := load(t, baseYAML+"app: {name: b, version: '2'}\n", ""); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate section: err = %v", err)
	}
	if _, err := load(t, "- a\n- b\n", ""); err == nil {
		t.Fatal("a sequence is not a configuration")
	}
}

// YAML cannot activate a module: a section nobody owns is an error.
func TestSectionsOfUnregisteredModulesAreRejected(t *testing.T) {
	d, err := load(t, baseYAML+"server: {port: 1}\ndatabse: {host: x}\n", "")
	if err != nil {
		t.Fatal(err)
	}
	b := d.Bind()
	_ = decode[server](t, b, "server")
	err = b.Finish()
	if err == nil || !strings.Contains(err.Error(), `section "databse" belongs to no registered module`) {
		t.Fatalf("err = %v", err)
	}
	b = d.Bind()
	b.Own("server")
	b.Own("databse")
	if err := b.Finish(); err != nil {
		t.Fatalf("owned sections: %v", err)
	}
}

func TestExtensionKeysAreIgnored(t *testing.T) {
	d, err := load(t, baseYAML+"x-feature-flags:\n  beta: true\nx-team: payments\n", "")
	if err != nil {
		t.Fatalf("top-level x- keys are for the application: %v", err)
	}
	if err := d.Bind().Finish(); err != nil {
		t.Fatal(err)
	}
	// only at the top level
	d, err = load(t, baseYAML+"server: {x-extra: 1}\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Bind().Decode("server", &server{}); err == nil {
		t.Fatal("x- keys are allowed only at the top level")
	}
}

// Precedence, later wins: base YAML, env file, overlay.
func TestLayersApplyInOrder(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	base := writeConfig(t, "env: stg\n"+baseYAML+"server: {port: 1, host: base, timeout: 1s, labels: {a: '1'}}\nstore: {name: base}\n")
	if err := os.WriteFile("config.stg.yaml", []byte("server: {host: overlay, labels: {b: '2'}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadFlags(base, writeEnv(t, "SERVER_PORT=2\nSERVER_HOST=env\nSERVER_TIMEOUT=5s\nSTORE_PORT=7\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := d.Bind()
	s := decode[server](t, b, "server")
	if s.Port != 2 || s.Host != "overlay" || s.Timeout != 5*time.Second || s.Labels["a"] != "1" || s.Labels["b"] != "2" {
		t.Fatalf("server = %+v", s)
	}
	// a section only the env file sets is decoded too
	if st := decode[store](t, b, "store"); st.Port != 7 || st.Name != "base" {
		t.Fatalf("store = %+v", st)
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	// defaults survive an absent section
	b = d.Bind()
	other := store{Host: "default"}
	if err := b.Decode("absent", &other); err != nil || other.Host != "default" || b.Present("absent") || !b.Present("server") {
		t.Fatalf("absent section: %v %+v", err, other)
	}
}

func TestUnknownEnvFileKeysAreRejected(t *testing.T) {
	for name, text := range map[string]string{
		"typo":       "SERVER_ORIGNS=https://a.example\n",
		"lower case": "server_port=9090\n",
		"stale":      "SOME_OTHER_TOOL_VAR=1\n",
		"mixed":      "SERVER_PORT=9090\nSERVER_PROT=1\n",
	} {
		t.Run(name, func(t *testing.T) {
			d, err := load(t, baseYAML, text)
			if err != nil {
				t.Fatal(err)
			}
			b := d.Bind()
			_ = decode[server](t, b, "server")
			if err := b.Finish(); err == nil || !strings.Contains(err.Error(), "unknown keys") {
				t.Fatalf("want an unknown keys error, got %v", err)
			}
		})
	}
	d, err := load(t, baseYAML, "SERVER_PORT=9090\nSERVER_ORIGINS=a, b\nSERVER_ENABLED=false\nAPP_NAME=renamed\nENV=dev\n")
	if err != nil {
		t.Fatal(err)
	}
	b := d.Bind()
	s := decode[server](t, b, "server")
	if s.Port != 9090 || len(s.Origins) != 2 || s.Origins[1] != "b" || s.Enabled == nil || *s.Enabled || d.App().Name != "renamed" || d.Env() != "dev" {
		t.Fatalf("env values not applied: %+v %+v", s, d.App())
	}
	if err := b.Finish(); err != nil {
		t.Fatalf("valid keys: %v", err)
	}
}

// Maps and sequences of structs are YAML only; an env key naming one is an
// explicit error, not ignored.
func TestEnvFileRejectsStructuredFields(t *testing.T) {
	for _, key := range []string{"SERVER_KEYS=x", "SERVER_LABELS=a=b"} {
		d, err := load(t, baseYAML, key+"\n")
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Bind().Decode("server", &server{}); err == nil || !strings.Contains(err.Error(), "cannot be set from the env file") {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	d, err := load(t, baseYAML, "SERVER_PORT=99999999999999999999\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Bind().Decode("server", &server{}); err == nil {
		t.Error("an overflowing value must be rejected")
	}
}

// env decides the overlay and the log level, so a misspelling must not pass.
func TestEnvIsValidatedAndNormalized(t *testing.T) {
	for _, bad := range []string{"production", "prod", "development", "test", "stage"} {
		_, err := load(t, "env: "+`"`+bad+`"`+"\n"+baseYAML, "")
		if err == nil || !strings.Contains(err.Error(), "env must be one of") {
			t.Errorf("env %q must be rejected, got %v", bad, err)
		}
	}
	for in, want := range map[string]string{"PRD": "prd", "Dev": "dev", "stg": "stg", "local": "local"} {
		d, err := load(t, "env: "+in+"\n"+baseYAML, "")
		if err != nil {
			t.Fatalf("env %q: %v", in, err)
		}
		if d.Env() != want {
			t.Errorf("env %q normalized to %q, want %q", in, d.Env(), want)
		}
	}
	if _, err := load(t, baseYAML, "ENV=production\n"); err == nil {
		t.Error("env from the env file must be validated")
	}
}

// A missing overlay is skipped; an unreadable or invalid one is an error, and
// the overlay may change env.
func TestOverlayErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	yaml := writeConfig(t, "env: prd\n"+baseYAML)
	if _, err := LoadFlags(yaml, ""); err != nil {
		t.Fatalf("no overlay file is fine: %v", err)
	}
	if err := os.WriteFile("config.prd.yaml", []byte("server: {port: 3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadFlags(yaml, "")
	if err != nil {
		t.Fatal(err)
	}
	if s := decode[server](t, d.Bind(), "server"); s.Port != 3 {
		t.Fatalf("overlay must apply: %+v", s)
	}
	if err := os.WriteFile("config.prd.yaml", []byte("server: {prot: 3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err = LoadFlags(yaml, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Bind().Decode("server", &server{}); err == nil || !strings.Contains(err.Error(), "overlay") {
		t.Fatalf("a typo in the overlay must fail too: %v", err)
	}
	if err := os.WriteFile("config.prd.yaml", []byte("env: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFlags(yaml, ""); err == nil {
		t.Fatal("env set by the overlay must be validated")
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
// ./config.yaml may be absent.
func TestExplicitConfigMustExist(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	missing := filepath.Join(dir, "nope.yaml")
	if _, err := LoadFlags(missing, ""); err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Fatalf("a missing -config file must fail with its name: %v", err)
	}
	if _, err := LoadFlags("", writeEnv(t, "APP_NAME=a\nAPP_VERSION=1\n")); err != nil {
		t.Fatalf("absent default config.yaml is allowed: %v", err)
	}
}

func TestConfigFileSizeIsBounded(t *testing.T) {
	big := baseYAML + "x-blob: '" + strings.Repeat("a", maxConfigFileBytes) + "'\n"
	if _, err := load(t, big, ""); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
	if _, err := LoadFlags(writeConfig(t, baseYAML), writeEnv(t, "X="+strings.Repeat("a", maxConfigFileBytes)+"\n")); err == nil {
		t.Fatal("an oversized env file must be refused")
	}
}

// An unset ${VAR} is an error naming every missing variable, unless it carries
// a default; expansion keeps the YAML types.
func TestUnsetEnvVariablesAreAnError(t *testing.T) {
	t.Setenv("NF_TEST_SET", "x")
	t.Setenv("NF_TEST_EMPTY", "")
	t.Setenv("NF_TEST_PORT", "8081")
	text := baseYAML + "server: {host: '${NF_TEST_SET}-${NF_TEST_UNSET_A}', tls: {key_file: '${NF_TEST_UNSET_B}${NF_TEST_UNSET_A}'}}\n"
	_, err := load(t, text, "")
	if err == nil || !strings.Contains(err.Error(), "NF_TEST_UNSET_A, NF_TEST_UNSET_B") || strings.Contains(err.Error(), "NF_TEST_SET") {
		t.Fatalf("err = %v", err)
	}
	text = baseYAML + "server:\n  port: ${NF_TEST_PORT}\n  host: '${NF_TEST_SET:-d}-${NF_TEST_UNSET_A:-fallback}-${NF_TEST_UNSET_B:-}'\n  tls: {key_file: '${NF_TEST_EMPTY}${NF_TEST_EMPTY:-v}'}\n"
	d, err := load(t, text, "")
	if err != nil {
		t.Fatal(err)
	}
	s := decode[server](t, d.Bind(), "server")
	if s.Host != "x-fallback-" || s.TLS.KeyFile != "v" || s.Port != 8081 {
		t.Errorf("server = %+v", s)
	}
}

// "$" is an ordinary character in env-file values: godotenv used to expand
// $NAME from earlier keys of the file (an unknown name became empty), so
// "pa$SWORD" lost a piece of the password.
func TestEnvFileValuesAreLiteral(t *testing.T) {
	env := strings.Join([]string{
		"STORE_PASSWORD=pa$SWORD$HOME",
		`STORE_HOST="h$USER"`,
		"STORE_NAME='lit$X'",
		"STORE_USER=${APP_NAME}",
		"APP_NAME=a",
		"APP_VERSION=1 # a comment is still a comment",
		"# STORE_PORT=1 is commented out",
		"",
	}, "\n")
	d, err := LoadFlags("", writeEnv(t, env))
	if err != nil {
		t.Fatal(err)
	}
	st := decode[store](t, d.Bind(), "store")
	checks := map[string][2]string{
		"password": {st.Password, "pa$SWORD$HOME"},
		"host":     {st.Host, "h$USER"},
		"name":     {st.Name, "lit$X"},
		"user":     {st.User, "${APP_NAME}"},
		"version":  {d.App().Version, "1"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if st.Port != 0 {
		t.Errorf("a commented key must not be read: port %d", st.Port)
	}
	if _, err := LoadFlags("", writeEnv(t, "APP_NAME=a\x00b\n")); err == nil {
		t.Error("a NUL byte must be rejected")
	}
}

// One document, several applications: each binding has its own bookkeeping.
func TestBindingsAreIndependent(t *testing.T) {
	d, err := load(t, baseYAML+"server: {port: 1}\n", "SERVER_PORT=2\n")
	if err != nil {
		t.Fatal(err)
	}
	a, b := d.Bind(), d.Bind()
	if s := decode[server](t, a, "server"); s.Port != 2 {
		t.Fatalf("port = %d", s.Port)
	}
	if err := a.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(); err == nil {
		t.Fatal("the second application did not register the server module")
	}
}

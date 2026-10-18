package jose

import (
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"time"
)

// Config is the standalone JOSE configuration (the `jose` YAML section).
// It holds no key material: keys are read from their sources by New.
type Config struct {
	Enabled  bool            `yaml:"enabled"`  // default: false; when false nothing is loaded and deps.JOSE is nil
	Limits   LimitsConfig    `yaml:"limits"`   // resource bounds; see LimitsConfig for the defaults
	Keys     []KeyConfig     `yaml:"keys"`     // at least one when enabled; kids are globally unique
	Profiles []ProfileConfig `yaml:"profiles"` // at least one when enabled; names are unique
	JWKS     JWKSConfig      `yaml:"jwks"`     // public key set endpoint
}

// LimitsConfig bounds every untrusted input. Byte limits are plain integers
// (jose does not depend on config.ByteSize).
type LimitsConfig struct {
	MaxTokenBytes   int64 `yaml:"max_token_bytes"`    // default: 16384; at most 1 MiB
	MaxHeaderBytes  int64 `yaml:"max_header_bytes"`   // decoded protected header; default: 2048; at most 16 KiB
	MaxPayloadBytes int64 `yaml:"max_payload_bytes"`  // decoded payload/plaintext; default: 8192; at most max_token_bytes
	MaxJSONDepth    int   `yaml:"max_json_depth"`     // default: 16; at most 64
	MaxKeyFileBytes int64 `yaml:"max_key_file_bytes"` // default: 65536; at most 1 MiB
	MaxProfiles     int   `yaml:"max_profiles"`       // default: 32; at most 1024
	MaxKeys         int   `yaml:"max_keys"`           // default: 64; at most 1024
}

// KeyConfig binds one key to exactly one algorithm and explicit operations.
type KeyConfig struct {
	KID        string          `yaml:"kid"`        // required, [A-Za-z0-9_-]{1,64}
	Algorithm  string          `yaml:"algorithm"`  // HS256 (default) | EdDSA | ES256 | PS256 | RS256 | RSA-OAEP-256 | dir
	Operations []string        `yaml:"operations"` // sign, verify (signing algs) | encrypt, decrypt (RSA-OAEP-256, dir); default: all of the algorithm
	Secret     string          `yaml:"secret"`     // inline symmetric secret for HS256 (>= 32 bytes) or dir (exactly 32 bytes), usually ${ENV}; excludes source
	Source     KeySourceConfig `yaml:"source"`     // key file; required unless secret is set
	Publish    bool            `yaml:"publish"`    // export the public part in the JWKS; default: false; never for symmetric keys
}

// KeySourceConfig points to the file holding the key. The path is trusted
// configuration, resolved from the working directory.
type KeySourceConfig struct {
	Format string `yaml:"format"` // required: pem | jwk | oct
	File   string `yaml:"file"`   // required; private/secret files must have no group/other permission bits
}

// ProfileConfig is a named, explicitly typed policy. Producer fields
// (signing_*, encryption_*) and consumer fields (verification_*,
// decryption_*) are independent: a profile may be producer-only,
// consumer-only or both.
type ProfileConfig struct {
	Name                   string    `yaml:"name"`                    // required, [A-Za-z0-9_-]{1,64}
	Kind                   string    `yaml:"kind"`                    // required: jws | jwe | jwt_signed | jwt_nested
	Typ                    string    `yaml:"typ"`                     // required, case-sensitive; unique across JWT profiles
	SigningAlgorithm       string    `yaml:"signing_algorithm"`       // with signing_key: enables Sign / IssueJWT
	SigningKey             string    `yaml:"signing_key"`             // kid with the sign operation
	VerificationAlgorithms []string  `yaml:"verification_algorithms"` // with verification_keys: enables Verify / ValidateJWT
	VerificationKeys       []string  `yaml:"verification_keys"`       // candidate kids with the verify operation
	EncryptionAlgorithm    string    `yaml:"encryption_algorithm"`    // with encryption_key: enables Encrypt / nested IssueJWT
	EncryptionKey          string    `yaml:"encryption_key"`          // kid with the encrypt operation
	DecryptionAlgorithms   []string  `yaml:"decryption_algorithms"`   // with decryption_keys: enables Decrypt / nested ValidateJWT
	DecryptionKeys         []string  `yaml:"decryption_keys"`         // candidate kids with the decrypt operation
	ContentEncryption      string    `yaml:"content_encryption"`      // jwe and jwt_nested only; default: A256GCM
	JWT                    JWTConfig `yaml:"jwt"`                     // jwt_signed and jwt_nested only
}

// JWTConfig is the claims policy of a JWT profile.
type JWTConfig struct {
	Issuer    string         `yaml:"issuer"`     // required
	Audiences []string       `yaml:"audiences"`  // required, nonempty, unique
	TTL       time.Duration  `yaml:"ttl"`        // default lifetime of issued tokens; default: 15m (capped by max_ttl when only max_ttl is set)
	MaxTTL    time.Duration  `yaml:"max_ttl"`    // maximum accepted lifetime; default: 15m, or ttl when only ttl is set
	ClockSkew *time.Duration `yaml:"clock_skew"` // default: 30s; explicit 0s is kept; at most 2m
}

// JWKSConfig configures the public key set endpoint served by nhttp.
type JWKSConfig struct {
	Enabled     bool           `yaml:"enabled"`       // default: false
	Path        string         `yaml:"path"`          // default: /.well-known/jwks.json
	CacheMaxAge *time.Duration `yaml:"cache_max_age"` // Cache-Control max-age; default: 60s; whole seconds in [0, 1h]
}

const (
	defaultMaxTokenBytes   = 16384
	defaultMaxHeaderBytes  = 2048
	defaultMaxPayloadBytes = 8192
	defaultMaxJSONDepth    = 16
	defaultMaxKeyFileBytes = 65536
	defaultMaxProfiles     = 32
	defaultMaxKeys         = 64
	defaultTTL             = 15 * time.Minute
	defaultClockSkew       = 30 * time.Second
	maxClockSkew           = 2 * time.Minute
	defaultJWKSPath        = "/.well-known/jwks.json"
	defaultJWKSMaxAge      = 60 * time.Second
	maxJWKSMaxAge          = time.Hour
	maxTypBytes            = 128
)

var identRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ApplyDefaults fills the unset fields with their defaults. It is a no-op
// when the configuration is disabled.
func ApplyDefaults(cfg *Config) {
	if !cfg.Enabled {
		return
	}
	l := &cfg.Limits
	setDefault(&l.MaxTokenBytes, defaultMaxTokenBytes)
	setDefault(&l.MaxHeaderBytes, defaultMaxHeaderBytes)
	setDefault(&l.MaxPayloadBytes, defaultMaxPayloadBytes)
	setDefault(&l.MaxJSONDepth, defaultMaxJSONDepth)
	setDefault(&l.MaxKeyFileBytes, defaultMaxKeyFileBytes)
	setDefault(&l.MaxProfiles, defaultMaxProfiles)
	setDefault(&l.MaxKeys, defaultMaxKeys)

	// Keys default to HS256 with every operation of their algorithm, and
	// profiles inherit their algorithms from the keys they reference. The
	// allowlists still come only from configuration, never from tokens.
	algs := map[string]string{}
	for i := range cfg.Keys {
		k := &cfg.Keys[i]
		if k.Algorithm == "" {
			k.Algorithm = HS256
		}
		if len(k.Operations) == 0 {
			k.Operations = opsFor(k.Algorithm)
		}
		algs[k.KID] = k.Algorithm
	}
	derive := func(kids []string) []string {
		var out []string
		for _, kid := range kids {
			if alg := algs[kid]; alg != "" && !slices.Contains(out, alg) {
				out = append(out, alg)
			}
		}
		return out
	}

	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if p.SigningAlgorithm == "" && p.SigningKey != "" {
			p.SigningAlgorithm = algs[p.SigningKey]
		}
		if p.EncryptionAlgorithm == "" && p.EncryptionKey != "" {
			p.EncryptionAlgorithm = algs[p.EncryptionKey]
		}
		if len(p.VerificationAlgorithms) == 0 {
			p.VerificationAlgorithms = derive(p.VerificationKeys)
		}
		if len(p.DecryptionAlgorithms) == 0 {
			p.DecryptionAlgorithms = derive(p.DecryptionKeys)
		}
		if encrypts(p.Kind) && p.ContentEncryption == "" {
			p.ContentEncryption = A256GCM
		}
		if !isJWTKind(p.Kind) {
			continue
		}
		j := &p.JWT
		switch {
		case j.TTL == 0 && j.MaxTTL == 0:
			j.TTL, j.MaxTTL = defaultTTL, defaultTTL
		case j.MaxTTL == 0:
			j.MaxTTL = j.TTL
		case j.TTL == 0:
			j.TTL = min(defaultTTL, j.MaxTTL)
		}
		if j.ClockSkew == nil {
			skew := defaultClockSkew
			j.ClockSkew = &skew
		}
	}

	if cfg.JWKS.Path == "" {
		cfg.JWKS.Path = defaultJWKSPath
	}
	if cfg.JWKS.CacheMaxAge == nil {
		age := defaultJWKSMaxAge
		cfg.JWKS.CacheMaxAge = &age
	}
}

func setDefault[T int | int64](v *T, def T) {
	if *v == 0 {
		*v = def
	}
}

// ValidateConfig checks the structure of an enabled configuration (after
// ApplyDefaults). It does not read key sources: New validates the actual keys.
func ValidateConfig(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	if err := validateLimits(cfg.Limits); err != nil {
		return err
	}
	if len(cfg.Keys) == 0 {
		return configError("jose.keys", "at least one key is required")
	}
	if len(cfg.Profiles) == 0 {
		return configError("jose.profiles", "at least one profile is required")
	}
	if len(cfg.Keys) > cfg.Limits.MaxKeys {
		return configError("jose.keys", "more keys than limits.max_keys")
	}
	if len(cfg.Profiles) > cfg.Limits.MaxProfiles {
		return configError("jose.profiles", "more profiles than limits.max_profiles")
	}

	keys := make(map[string]*KeyConfig, len(cfg.Keys))
	for i := range cfg.Keys {
		k := &cfg.Keys[i]
		field := fmt.Sprintf("jose.keys[%d]", i)
		if err := validateKeyConfig(field, k); err != nil {
			return err
		}
		if keys[k.KID] != nil {
			return configError(field+".kid", "duplicate kid")
		}
		keys[k.KID] = k
	}

	names := map[string]bool{}
	typs := map[string]bool{}
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		field := fmt.Sprintf("jose.profiles[%d]", i)
		if err := validateProfileConfig(field, p, keys); err != nil {
			return err
		}
		if names[p.Name] {
			return configError(field+".name", "duplicate profile name")
		}
		names[p.Name] = true
		if isJWTKind(p.Kind) {
			if typs[p.Typ] {
				return configError(field+".typ", "jwt profiles must have distinct typ values")
			}
			typs[p.Typ] = true
		}
	}

	if cfg.JWKS.Enabled {
		if len(cfg.JWKS.Path) < 2 || cfg.JWKS.Path[0] != '/' {
			return configError("jose.jwks.path", "must be an absolute path")
		}
	}
	if age := cfg.JWKS.CacheMaxAge; age != nil {
		if *age < 0 || *age > maxJWKSMaxAge || *age%time.Second != 0 {
			return configError("jose.jwks.cache_max_age", "must be whole seconds in [0, 1h]")
		}
	}
	return nil
}

func validateLimits(l LimitsConfig) error {
	const mib = 1 << 20
	checks := []struct {
		field string
		ok    bool
	}{
		{"max_token_bytes", l.MaxTokenBytes > 0 && l.MaxTokenBytes <= mib},
		{"max_header_bytes", l.MaxHeaderBytes > 0 && l.MaxHeaderBytes <= 16<<10},
		{"max_payload_bytes", l.MaxPayloadBytes > 0 && l.MaxPayloadBytes <= l.MaxTokenBytes},
		{"max_json_depth", l.MaxJSONDepth > 0 && l.MaxJSONDepth <= 64},
		{"max_key_file_bytes", l.MaxKeyFileBytes > 0 && l.MaxKeyFileBytes <= mib},
		{"max_profiles", l.MaxProfiles > 0 && l.MaxProfiles <= 1024},
		{"max_keys", l.MaxKeys > 0 && l.MaxKeys <= 1024},
	}
	for _, c := range checks {
		if !c.ok {
			return configError("jose.limits."+c.field, "out of range")
		}
	}
	return nil
}

func validateKeyConfig(field string, k *KeyConfig) error {
	if !identRe.MatchString(k.KID) {
		return configError(field+".kid", "must match [A-Za-z0-9_-]{1,64}")
	}
	if !isSigningAlg(k.Algorithm) && !isKeyMgmtAlg(k.Algorithm) {
		return configError(field+".algorithm", "unsupported algorithm")
	}
	if len(k.Operations) == 0 {
		return configError(field+".operations", "at least one operation is required")
	}
	allowed := opsFor(k.Algorithm)
	seen := map[string]bool{}
	for _, op := range k.Operations {
		if !slices.Contains(allowed, op) {
			return configError(field+".operations", "operation not compatible with the algorithm")
		}
		if seen[op] {
			return configError(field+".operations", "duplicate operation")
		}
		seen[op] = true
	}
	if k.Secret != "" {
		if k.Source != (KeySourceConfig{}) {
			return configError(field+".secret", "secret and source are mutually exclusive")
		}
		if !isSymmetricAlg(k.Algorithm) {
			return configError(field+".secret", "inline secrets are only for HS256 and dir")
		}
		if k.Algorithm == HS256 && len(k.Secret) < hmacMinLen {
			return configError(field+".secret", "HS256 secrets must have at least 32 bytes")
		}
		if k.Algorithm == Dir && len(k.Secret) != dirKeyLen {
			return configError(field+".secret", "dir secrets must have exactly 32 bytes")
		}
		if k.Publish {
			return configError(field+".publish", "symmetric keys are never published")
		}
		return nil
	}
	if k.Source == (KeySourceConfig{}) {
		return configError(field, "secret or source is required (is the ${VAR} of the secret set?)")
	}
	switch k.Source.Format {
	case "pem", "jwk", "oct":
	default:
		return configError(field+".source.format", "must be pem, jwk or oct")
	}
	if k.Source.File == "" {
		return configError(field+".source.file", "required")
	}
	if isSymmetricAlg(k.Algorithm) {
		if k.Source.Format == "pem" {
			return configError(field+".source.format", "symmetric keys must use the oct or jwk format")
		}
		if k.Publish {
			return configError(field+".publish", "symmetric keys are never published")
		}
	} else if k.Source.Format == "oct" {
		return configError(field+".source.format", "asymmetric keys must use the pem or jwk format")
	}
	return nil
}

func validateProfileConfig(field string, p *ProfileConfig, keys map[string]*KeyConfig) error {
	if !identRe.MatchString(p.Name) {
		return configError(field+".name", "must match [A-Za-z0-9_-]{1,64}")
	}
	switch p.Kind {
	case KindJWS, KindJWE, KindJWTSigned, KindJWTNested:
	default:
		return configError(field+".kind", "must be jws, jwe, jwt_signed or jwt_nested")
	}
	if !validTyp(p.Typ) {
		return configError(field+".typ", "required printable ASCII without spaces, at most 128 bytes")
	}

	hasSign := p.SigningAlgorithm != "" || p.SigningKey != ""
	hasVerify := len(p.VerificationAlgorithms) > 0 || len(p.VerificationKeys) > 0
	hasEnc := p.EncryptionAlgorithm != "" || p.EncryptionKey != ""
	hasDec := len(p.DecryptionAlgorithms) > 0 || len(p.DecryptionKeys) > 0

	if !signs(p.Kind) && (hasSign || hasVerify) {
		return configError(field, "signing fields are not allowed on jwe profiles")
	}
	if !encrypts(p.Kind) && (hasEnc || hasDec || p.ContentEncryption != "") {
		return configError(field, "encryption fields are only allowed on jwe and jwt_nested profiles")
	}
	if !isJWTKind(p.Kind) && !jwtConfigEmpty(p.JWT) {
		return configError(field+".jwt", "jwt fields are only allowed on jwt profiles")
	}
	if encrypts(p.Kind) && p.ContentEncryption != A256GCM {
		return configError(field+".content_encryption", "only A256GCM is supported")
	}

	// producer: an active key paired with its output algorithm
	if hasSign {
		if err := checkProducer(field+".signing", p.SigningAlgorithm, p.SigningKey, OpSign, isSigningAlg, keys); err != nil {
			return err
		}
	}
	if hasEnc {
		if err := checkProducer(field+".encryption", p.EncryptionAlgorithm, p.EncryptionKey, OpEncrypt, isKeyMgmtAlg, keys); err != nil {
			return err
		}
	}
	// consumer: explicit algorithm allowlist and candidate kids
	if hasVerify {
		if err := checkConsumer(field+".verification", p.VerificationAlgorithms, p.VerificationKeys, OpVerify, isSigningAlg, keys); err != nil {
			return err
		}
		hs := slices.Contains(p.VerificationAlgorithms, HS256)
		if hs && len(p.VerificationAlgorithms) > 1 {
			return configError(field+".verification_algorithms", "HS256 cannot be mixed with asymmetric algorithms")
		}
	}
	if hasDec {
		if err := checkConsumer(field+".decryption", p.DecryptionAlgorithms, p.DecryptionKeys, OpDecrypt, isKeyMgmtAlg, keys); err != nil {
			return err
		}
	}

	producer := hasSign || hasEnc
	consumer := hasVerify || hasDec
	switch p.Kind {
	case KindJWS:
		if !hasSign && !hasVerify {
			return configError(field, "a jws profile needs signing and/or verification")
		}
	case KindJWE:
		if !hasEnc && !hasDec {
			return configError(field, "a jwe profile needs encryption and/or decryption")
		}
	case KindJWTSigned:
		if !producer && !consumer {
			return configError(field, "a jwt_signed profile needs signing and/or verification")
		}
	case KindJWTNested:
		if hasSign != hasEnc {
			return configError(field, "a nested producer needs both signing and encryption")
		}
		if hasVerify != hasDec {
			return configError(field, "a nested consumer needs both verification and decryption")
		}
		if !producer && !consumer {
			return configError(field, "a jwt_nested profile needs a producer and/or consumer capability")
		}
	}
	if isJWTKind(p.Kind) {
		return validateJWTConfig(field+".jwt", p.JWT)
	}
	return nil
}

func checkProducer(field, alg, kid, op string, algOK func(string) bool, keys map[string]*KeyConfig) error {
	if alg == "" || kid == "" {
		return configError(field, "key and algorithm must be set together")
	}
	if !algOK(alg) {
		return configError(field+"_algorithm", "unsupported algorithm for this operation")
	}
	k := keys[kid]
	if k == nil {
		return configError(field+"_key", "unknown kid")
	}
	if k.Algorithm != alg {
		return configError(field+"_key", "key algorithm does not match")
	}
	if !slices.Contains(k.Operations, op) {
		return configError(field+"_key", "key lacks the "+op+" operation")
	}
	return nil
}

func checkConsumer(field string, algs, kids []string, op string, algOK func(string) bool, keys map[string]*KeyConfig) error {
	if len(algs) == 0 || len(kids) == 0 {
		return configError(field, "keys and algorithms must be set together")
	}
	for _, a := range algs {
		if !algOK(a) {
			return configError(field+"_algorithms", "unsupported algorithm for this operation")
		}
	}
	if hasDuplicates(algs) {
		return configError(field+"_algorithms", "duplicate algorithm")
	}
	if hasDuplicates(kids) {
		return configError(field+"_keys", "duplicate kid")
	}
	for _, kid := range kids {
		k := keys[kid]
		if k == nil {
			return configError(field+"_keys", "unknown kid")
		}
		if !slices.Contains(algs, k.Algorithm) {
			return configError(field+"_keys", "key algorithm is not in the allowlist")
		}
		if !slices.Contains(k.Operations, op) {
			return configError(field+"_keys", "key lacks the "+op+" operation")
		}
	}
	return nil
}

func validateJWTConfig(field string, j JWTConfig) error {
	if j.Issuer == "" || hasControl(j.Issuer) {
		return configError(field+".issuer", "required, without control characters")
	}
	if len(j.Audiences) == 0 {
		return configError(field+".audiences", "at least one audience is required")
	}
	for _, a := range j.Audiences {
		if a == "" || hasControl(a) {
			return configError(field+".audiences", "audiences must be nonempty, without control characters")
		}
	}
	if hasDuplicates(j.Audiences) {
		return configError(field+".audiences", "duplicate audience")
	}
	if j.TTL <= 0 || j.MaxTTL <= 0 {
		return configError(field+".ttl", "ttl and max_ttl must be positive")
	}
	if j.TTL%time.Second != 0 || j.MaxTTL%time.Second != 0 {
		return configError(field+".ttl", "ttl and max_ttl must be whole seconds")
	}
	if j.TTL > j.MaxTTL {
		return configError(field+".ttl", "ttl exceeds max_ttl")
	}
	if j.ClockSkew == nil || *j.ClockSkew < 0 || *j.ClockSkew > maxClockSkew {
		return configError(field+".clock_skew", "must be in [0, 2m]")
	}
	return nil
}

func jwtConfigEmpty(j JWTConfig) bool {
	return j.Issuer == "" && len(j.Audiences) == 0 && j.TTL == 0 && j.MaxTTL == 0 && j.ClockSkew == nil
}

func validTyp(typ string) bool {
	if typ == "" || len(typ) > maxTypBytes {
		return false
	}
	for i := 0; i < len(typ); i++ {
		if typ[i] <= ' ' || typ[i] >= 0x7f || typ[i] == '"' || typ[i] == '\\' {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return true
		}
	}
	return false
}

func hasDuplicates(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}

// Clone returns a deep copy, so the service never shares slices or pointers
// with the caller.
func (c Config) Clone() Config {
	out := c
	out.Keys = make([]KeyConfig, len(c.Keys))
	for i, k := range c.Keys {
		k.Operations = slices.Clone(k.Operations)
		out.Keys[i] = k
	}
	out.Profiles = make([]ProfileConfig, len(c.Profiles))
	for i, p := range c.Profiles {
		p.VerificationAlgorithms = slices.Clone(p.VerificationAlgorithms)
		p.VerificationKeys = slices.Clone(p.VerificationKeys)
		p.DecryptionAlgorithms = slices.Clone(p.DecryptionAlgorithms)
		p.DecryptionKeys = slices.Clone(p.DecryptionKeys)
		p.JWT.Audiences = slices.Clone(p.JWT.Audiences)
		if p.JWT.ClockSkew != nil {
			skew := *p.JWT.ClockSkew
			p.JWT.ClockSkew = &skew
		}
		out.Profiles[i] = p
	}
	if c.JWKS.CacheMaxAge != nil {
		age := *c.JWKS.CacheMaxAge
		out.JWKS.CacheMaxAge = &age
	}
	return out
}

// LogValue logs only non-sensitive policy data: no key paths or material.
func (c Config) LogValue() slog.Value {
	names := make([]string, len(c.Profiles))
	for i, p := range c.Profiles {
		names[i] = p.Name
	}
	return slog.GroupValue(
		slog.Bool("enabled", c.Enabled),
		slog.Int("keys", len(c.Keys)),
		slog.Any("profiles", names),
		slog.Bool("jwks", c.JWKS.Enabled),
	)
}

// String keeps fmt from printing key source paths.
func (c Config) String() string {
	return fmt.Sprintf("jose.Config{enabled: %t, keys: %d, profiles: %d}", c.Enabled, len(c.Keys), len(c.Profiles))
}

// LogValue omits the source path.
func (k KeyConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("kid", k.KID), slog.String("algorithm", k.Algorithm), slog.Any("operations", k.Operations))
}

// String keeps fmt from printing the source path.
func (k KeyConfig) String() string {
	return fmt.Sprintf("jose.KeyConfig{kid: %s, algorithm: %s}", k.KID, k.Algorithm)
}

// LogValue omits the path.
func (s KeySourceConfig) LogValue() slog.Value { return slog.StringValue(s.Format) }

// String keeps fmt from printing the path.
func (s KeySourceConfig) String() string { return "jose.KeySource{format: " + s.Format + "}" }

// GoString keeps %#v from printing key source paths.
func (c Config) GoString() string { return c.String() }

// GoString keeps %#v from printing the source path.
func (k KeyConfig) GoString() string { return k.String() }

// GoString keeps %#v from printing the path.
func (s KeySourceConfig) GoString() string { return s.String() }

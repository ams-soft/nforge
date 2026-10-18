package jose

import (
	"context"
	"crypto/sha256"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Service signs, verifies, encrypts and decrypts with named profiles. It is
// safe for concurrent use. Each operation runs on one immutable snapshot of
// the loaded keys; Reload swaps the snapshot atomically and Shutdown stops
// admitting new operations.
type Service struct {
	cfg        Config
	clock      func() time.Time
	validators map[string]ClaimsValidator

	state    atomic.Pointer[snapshot]
	reloadMu sync.Mutex

	mu        sync.Mutex
	closed    bool
	active    int
	drained   chan struct{}
	drainOnce sync.Once
	wipeOnce  sync.Once
}

// snapshot is the immutable runtime state built from the configuration and
// the key sources.
type snapshot struct {
	keys        map[string]*key
	profiles    map[string]*profile
	jwks        []byte
	fingerprint [32]byte
}

// profile is the resolved runtime policy of a ProfileConfig.
type profile struct {
	name string
	kind string
	typ  string

	signAlg    string
	signKey    *key
	verifyAlgs []string
	verifyKeys map[string]*key

	encAlg     string
	encKey     *key
	decAlgs    []string
	decKeys    map[string]*key
	contentEnc string

	jwt       *jwtPolicy
	validator ClaimsValidator
}

type jwtPolicy struct {
	issuer    string
	audiences []string
	ttl       time.Duration
	maxTTL    time.Duration
	skew      time.Duration
}

func (p *profile) canSign() bool    { return p.signKey != nil }
func (p *profile) canVerify() bool  { return len(p.verifyKeys) > 0 }
func (p *profile) canEncrypt() bool { return p.encKey != nil }
func (p *profile) canDecrypt() bool { return len(p.decKeys) > 0 }

// canProduce reports the producer capability of the profile kind.
func (p *profile) canProduce() bool {
	switch p.kind {
	case KindJWS, KindJWTSigned:
		return p.canSign()
	case KindJWE:
		return p.canEncrypt()
	case KindJWTNested:
		return p.canSign() && p.canEncrypt()
	}
	return false
}

// canConsume reports the consumer capability of the profile kind.
func (p *profile) canConsume() bool {
	switch p.kind {
	case KindJWS, KindJWTSigned:
		return p.canVerify()
	case KindJWE:
		return p.canDecrypt()
	case KindJWTNested:
		return p.canVerify() && p.canDecrypt()
	}
	return false
}

// ProfileInfo describes a configured profile without exposing keys.
type ProfileInfo struct {
	Name string
	Kind string
	Type string
	// CanProduce reports Sign / Encrypt / IssueJWT availability.
	CanProduce bool
	// CanConsume reports Verify / Decrypt / ValidateJWT availability.
	CanConsume bool
}

// New validates the configuration, loads every key source and returns a
// ready service. It returns ErrDisabled when cfg.Enabled is false. New never
// panics, exits or touches global state; multiple services may coexist.
func New(ctx context.Context, cfg Config, opts ...Option) (*Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxError(err, "new", "")
	}
	c := cfg.Clone()
	if !c.Enabled {
		return nil, newError(ErrDisabled, "new", "", "", "jose.enabled is false")
	}
	ApplyDefaults(&c)
	if err := ValidateConfig(c); err != nil {
		return nil, err
	}

	o := &options{clock: time.Now, validators: map[string]ClaimsValidator{}}
	for _, opt := range opts {
		if opt == nil {
			return nil, newError(ErrInvalidConfig, "new", "", "option", "nil option")
		}
		if err := opt(o); err != nil {
			return nil, err
		}
	}
	for name := range o.validators {
		i := slices.IndexFunc(c.Profiles, func(p ProfileConfig) bool { return p.Name == name })
		if i < 0 {
			return nil, newError(ErrUnknownProfile, "new", "", "validator", "validator for an unknown profile")
		}
		if !isJWTKind(c.Profiles[i].Kind) {
			return nil, newError(ErrProfileKind, "new", name, "validator", "validators require a jwt profile")
		}
	}

	s := &Service{cfg: c, clock: o.clock, validators: o.validators, drained: make(chan struct{})}
	snap, err := s.buildSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	s.state.Store(snap)
	return s, nil
}

// buildSnapshot reads every key source and resolves the profiles.
func (s *Service) buildSnapshot(ctx context.Context) (*snapshot, error) {
	snap := &snapshot{keys: make(map[string]*key, len(s.cfg.Keys)), profiles: make(map[string]*profile, len(s.cfg.Profiles))}
	ok := false
	defer func() {
		if !ok {
			for _, k := range snap.keys {
				k.wipe()
			}
		}
	}()

	materials := map[[32]byte]bool{}
	fp := sha256.New()
	for _, kc := range s.cfg.Keys {
		if err := ctx.Err(); err != nil {
			return nil, ctxError(err, "load key", "")
		}
		k, err := loadKey(kc, s.cfg.Limits)
		if err != nil {
			return nil, err
		}
		if materials[k.material] {
			k.wipe()
			return nil, keyErr(kc.KID, "the same key material is configured more than once")
		}
		materials[k.material] = true
		snap.keys[k.kid] = k
		fp.Write([]byte(k.kid))
		fp.Write(k.material[:])
	}
	copy(snap.fingerprint[:], fp.Sum(nil))

	for _, pc := range s.cfg.Profiles {
		p := &profile{
			name: pc.Name, kind: pc.Kind, typ: pc.Typ,
			signAlg: pc.SigningAlgorithm, verifyAlgs: slices.Clone(pc.VerificationAlgorithms),
			encAlg: pc.EncryptionAlgorithm, decAlgs: slices.Clone(pc.DecryptionAlgorithms),
			contentEnc: pc.ContentEncryption,
			validator:  s.validators[pc.Name],
		}
		if pc.SigningKey != "" {
			p.signKey = snap.keys[pc.SigningKey]
		}
		if pc.EncryptionKey != "" {
			p.encKey = snap.keys[pc.EncryptionKey]
		}
		if len(pc.VerificationKeys) > 0 {
			p.verifyKeys = map[string]*key{}
			for _, kid := range pc.VerificationKeys {
				p.verifyKeys[kid] = snap.keys[kid]
			}
		}
		if len(pc.DecryptionKeys) > 0 {
			p.decKeys = map[string]*key{}
			for _, kid := range pc.DecryptionKeys {
				p.decKeys[kid] = snap.keys[kid]
			}
		}
		if isJWTKind(pc.Kind) {
			p.jwt = &jwtPolicy{
				issuer: pc.JWT.Issuer, audiences: slices.Clone(pc.JWT.Audiences),
				ttl: pc.JWT.TTL, maxTTL: pc.JWT.MaxTTL, skew: *pc.JWT.ClockSkew,
			}
		}
		snap.profiles[p.name] = p
	}

	jwks, err := buildJWKS(snap.keys)
	if err != nil {
		return nil, err
	}
	snap.jwks = jwks
	ok = true
	return snap, nil
}

// acquire admits one operation and returns the snapshot it must use for
// all of its steps.
func (s *Service) acquire(ctx context.Context, op, profile string) (*snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxError(err, op, profile)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, newError(ErrClosed, op, profile, "", "")
	}
	s.active++
	return s.state.Load(), nil
}

func (s *Service) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.closed && s.active == 0 {
		s.drainOnce.Do(func() { close(s.drained) })
	}
}

// resolve finds the profile and checks its kind.
func (snap *snapshot) resolve(op, name string, kinds ...string) (*profile, error) {
	p := snap.profiles[name]
	if p == nil {
		return nil, newError(ErrUnknownProfile, op, "", "", "")
	}
	if !slices.Contains(kinds, p.kind) {
		return nil, newError(ErrProfileKind, op, name, "", "operation not allowed for a "+p.kind+" profile")
	}
	return p, nil
}

// Profile describes a configured profile. It is safe to call after Shutdown.
func (s *Service) Profile(name string) (ProfileInfo, bool) {
	p := s.state.Load().profiles[name]
	if p == nil {
		return ProfileInfo{}, false
	}
	return ProfileInfo{Name: p.name, Kind: p.kind, Type: p.typ, CanProduce: p.canProduce(), CanConsume: p.canConsume()}, true
}

// MaxTokenBytes is the configured limit on compact token size, so transport
// adapters can reject oversized credentials before any processing.
func (s *Service) MaxTokenBytes() int64 { return s.cfg.Limits.MaxTokenBytes }

// Check reports whether the service admits operations and every profile
// still has its configured capabilities. It does no I/O and no cryptography,
// so it is cheap enough for readiness probes.
func (s *Service) Check(ctx context.Context) error {
	snap, err := s.acquire(ctx, "check", "")
	if err != nil {
		return err
	}
	defer s.release()
	for _, p := range snap.profiles {
		if !p.canProduce() && !p.canConsume() {
			return newError(ErrOperationUnavailable, "check", p.name, "", "profile has no usable capability")
		}
	}
	return nil
}

// PublicJWKS returns the JSON Web Key Set of the keys configured with
// publish: true, sorted by kid, with public members only. The result is a
// fresh copy owned by the caller.
func (s *Service) PublicJWKS(ctx context.Context) ([]byte, error) {
	snap, err := s.acquire(ctx, "jwks", "")
	if err != nil {
		return nil, err
	}
	defer s.release()
	return slices.Clone(snap.jwks), nil
}

// Reload rereads the configured key sources and atomically replaces the
// key snapshot when every key validates. It does not reload the YAML nor
// change profile policy. On failure the previous snapshot stays active.
// In-flight operations finish on the snapshot they started with.
func (s *Service) Reload(ctx context.Context) error {
	if _, err := s.acquire(ctx, "reload", ""); err != nil {
		return err
	}
	defer s.release()
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	next, err := s.buildSnapshot(ctx)
	if err != nil {
		return withOp(err, "reload", "")
	}
	if next.fingerprint == s.state.Load().fingerprint {
		for _, k := range next.keys {
			k.wipe()
		}
		return nil // identical material: keep the current snapshot
	}
	s.state.Store(next)
	return nil
}

// Shutdown stops admitting operations (they get ErrClosed) and waits for
// the admitted ones up to ctx. It is idempotent: after a timeout, admission
// stays closed and a later call keeps waiting. Once drained, owned secret
// buffers are cleared on a best-effort basis (no guarantee in a garbage
// collected runtime).
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.active == 0 {
			s.drainOnce.Do(func() { close(s.drained) })
		}
	}
	s.mu.Unlock()

	select {
	case <-s.drained:
		s.wipeOnce.Do(func() {
			for _, k := range s.state.Load().keys {
				k.wipe()
			}
		})
		return nil
	case <-ctx.Done():
		return ctxError(ctx.Err(), "shutdown", "")
	}
}

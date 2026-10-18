package jose

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/ams-soft/nforge/jose/internal/strictjson"
)

// ValidatedJWT is returned only after every cryptographic, registered-claim,
// custom-decoding and application check succeeded.
type ValidatedJWT struct {
	Registered RegisteredClaims
	Signing    Header
	Encryption *Header // Non-nil only for jwt_nested.
}

// IssueJWT issues a token with a jwt_signed or jwt_nested profile. Missing
// iss, aud, iat, nbf, exp and jti are filled from the profile and the clock;
// supplied values must agree with the profile policy. private holds the
// custom claims: nil, a struct (or pointer to one) or a string-keyed map that
// encodes as a JSON object without registered claim names.
func (s *Service) IssueJWT(ctx context.Context, profile string, registered RegisteredClaims, private any) (string, error) {
	const op = "issue"
	snap, err := s.acquire(ctx, op, profile)
	if err != nil {
		return "", err
	}
	defer s.release()
	p, err := snap.resolve(op, profile, KindJWTSigned, KindJWTNested)
	if err != nil {
		return "", err
	}
	if !p.canProduce() {
		return "", newError(ErrOperationUnavailable, op, profile, "", "profile cannot issue tokens")
	}
	out, err := s.issue(ctx, p, registered.clone(), private)
	if err != nil {
		return "", withOp(err, op, profile)
	}
	return out, nil
}

func (s *Service) issue(ctx context.Context, p *profile, c RegisteredClaims, private any) (string, error) {
	pol := p.jwt
	now := s.clock().Truncate(time.Second)

	if c.Issuer == "" {
		c.Issuer = pol.issuer
	} else if c.Issuer != pol.issuer {
		return "", newError(ErrInvalidIssuer, "", "", "iss", "issuer differs from the profile")
	}
	if len(c.Audience) == 0 {
		c.Audience = slices.Clone(Audience(pol.audiences))
	} else {
		if c.Audience.check() != nil {
			return "", newError(ErrInvalidAudience, "", "", "aud", "invalid audience")
		}
		for _, a := range c.Audience {
			if !slices.Contains(pol.audiences, a) {
				return "", newError(ErrInvalidAudience, "", "", "aud", "audience not configured in the profile")
			}
		}
	}
	if c.IssuedAt == nil {
		c.IssuedAt = NewNumericDate(now)
	}
	if c.NotBefore == nil {
		c.NotBefore = NewNumericDate(now)
	}
	if c.ExpiresAt == nil {
		exp, ok := addSeconds(*c.IssuedAt, int64(pol.ttl/time.Second))
		if !ok {
			return "", claimErr("exp", "expiration overflows")
		}
		c.ExpiresAt = &exp
	}
	if c.ID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", newError(ErrInternal, "", "", "jti", "entropy source failed")
		}
		c.ID = base64.RawURLEncoding.EncodeToString(b[:])
	}
	if c.Subject == "" {
		return "", claimErr("sub", "required")
	}
	if err := checkIdentity(c); err != nil {
		return "", err
	}
	iat, nbf, exp := *c.IssuedAt, *c.NotBefore, *c.ExpiresAt
	for name, d := range map[string]NumericDate{"iat": iat, "nbf": nbf, "exp": exp} {
		if !d.valid() {
			return "", claimErr(name, "date out of range")
		}
	}
	if iat > nbf || nbf >= exp {
		return "", claimErr("nbf", "dates must satisfy iat <= nbf < exp")
	}
	lifetime := int64(exp) - int64(iat)
	if lifetime <= 0 || lifetime > int64(pol.maxTTL/time.Second) {
		return "", claimErr("exp", "lifetime exceeds max_ttl")
	}
	nowSec := NumericDate(now.Unix())
	if iat > nowSec {
		return "", claimErr("iat", "issued in the future")
	}
	if exp <= nowSec {
		return "", newError(ErrExpired, "", "", "exp", "token would already be expired")
	}

	custom, err := s.privateMembers(private)
	if err != nil {
		return "", err
	}
	if p.validator != nil {
		normalized, err := normalize(custom)
		if err != nil {
			return "", err
		}
		if err := callValidator(ctx, p.validator, c.clone(), normalized); err != nil {
			return "", err
		}
	}

	members := map[string]json.RawMessage{}
	for name, raw := range custom {
		members[name] = raw
	}
	regJSON, err := json.Marshal(c)
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot encode claims")
	}
	var reg map[string]json.RawMessage
	if err := json.Unmarshal(regJSON, &reg); err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot encode claims")
	}
	for name, raw := range reg {
		members[name] = raw
	}
	payload, err := json.Marshal(members)
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot encode claims")
	}
	if err := ctx.Err(); err != nil {
		return "", ctxError(err, "", "")
	}

	jws, err := s.signCompact(p, payload)
	if err != nil {
		return "", err
	}
	if p.kind == KindJWTSigned {
		return jws, nil
	}
	return s.encryptCompact(p, []byte(jws), "JWT")
}

// privateMembers encodes the custom claims and checks they form a JSON
// object without registered names or duplicates.
func (s *Service) privateMembers(private any) (map[string]json.RawMessage, error) {
	if private == nil {
		return map[string]json.RawMessage{}, nil
	}
	v := reflect.ValueOf(private)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return map[string]json.RawMessage{}, nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, claimErr("private", "maps must have string keys")
		}
	default:
		return nil, claimErr("private", "custom claims must be a struct or a string-keyed map")
	}
	data, err := json.Marshal(private)
	if err != nil {
		return nil, claimErr("private", "custom claims cannot be encoded")
	}
	members, err := strictjson.Object(data, s.cfg.Limits.MaxJSONDepth)
	if err != nil {
		return nil, claimErr("private", "custom claims must encode as a strict json object")
	}
	for name := range members {
		if registeredNames[name] {
			return nil, claimErr(name, "custom claims cannot use registered claim names")
		}
	}
	return members, nil
}

// normalize decodes custom members into an isolated map with json.Number.
func normalize(members map[string]json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(members))
	for name, raw := range members {
		var v any
		if err := strictjson.DecodeNumbers(raw, &v); err != nil {
			return nil, claimErr("private", "custom claims cannot be decoded")
		}
		out[name] = v
	}
	return out, nil
}

// callValidator maps application errors onto the package categories; it
// never lets an unknown error count as success.
func callValidator(ctx context.Context, fn ClaimsValidator, c RegisteredClaims, custom any) error {
	err := fn(ctx, c, custom)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return ctxError(err, "", "")
	case errors.Is(err, ErrClaimsRejected):
		return newError(ErrClaimsRejected, "", "", "", "rejected by the application")
	case errors.Is(err, ErrValidationUnavailable):
		return newError(ErrValidationUnavailable, "", "", "", "application validation unavailable")
	}
	return newError(ErrInternal, "", "", "", "application validator failed")
}

// ValidateJWT verifies (and for jwt_nested, first decrypts) a token with the
// caller-chosen profile and validates every claim. privateTarget is nil or a
// non-nil pointer to a struct or string-keyed map receiving the custom
// claims; it is assigned only on complete success.
func (s *Service) ValidateJWT(ctx context.Context, profile, compact string, privateTarget any) (*ValidatedJWT, error) {
	const op = "validate"
	targetType, err := checkTarget(privateTarget)
	if err != nil {
		return nil, withOp(err, op, profile)
	}
	snap, err := s.acquire(ctx, op, profile)
	if err != nil {
		return nil, err
	}
	defer s.release()
	p, err := snap.resolve(op, profile, KindJWTSigned, KindJWTNested)
	if err != nil {
		return nil, err
	}
	if !p.canConsume() {
		return nil, newError(ErrOperationUnavailable, op, profile, "", "profile cannot validate tokens")
	}
	out, temp, err := s.validate(ctx, p, compact, targetType)
	if err != nil {
		return nil, withOp(err, op, profile)
	}
	if temp.IsValid() {
		reflect.ValueOf(privateTarget).Elem().Set(temp.Elem())
	}
	return out, nil
}

func checkTarget(target any) (reflect.Type, error) {
	if target == nil {
		return nil, nil
	}
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, newError(ErrInvalidTarget, "", "", "", "target must be a non-nil pointer")
	}
	t := v.Type().Elem()
	switch {
	case t.Kind() == reflect.Struct:
	case t.Kind() == reflect.Map && t.Key().Kind() == reflect.String:
	default:
		return nil, newError(ErrInvalidTarget, "", "", "", "target must point to a struct or string-keyed map")
	}
	return t, nil
}

func (s *Service) validate(ctx context.Context, p *profile, input string, targetType reflect.Type) (*ValidatedJWT, reflect.Value, error) {
	out := &ValidatedJWT{}
	jws := input
	if p.kind == KindJWTNested {
		inner, header, err := s.decryptCompact(ctx, p, input, "JWT")
		if err != nil {
			return nil, reflect.Value{}, err
		}
		out.Encryption = &header
		jws = string(inner)
	}
	payload, header, err := s.verifyCompact(ctx, p, jws)
	if err != nil {
		return nil, reflect.Value{}, err
	}
	out.Signing = header
	if len(payload) == 0 {
		return nil, reflect.Value{}, claimErr("", "empty claims")
	}
	members, err := strictjson.Object(payload, s.cfg.Limits.MaxJSONDepth)
	if err != nil {
		return nil, reflect.Value{}, claimErr("", "claims must be a strict json object")
	}
	c, err := decodeRegistered(members)
	if err != nil {
		return nil, reflect.Value{}, err
	}
	if err := s.checkClaims(p.jwt, c); err != nil {
		return nil, reflect.Value{}, err
	}

	custom := map[string]json.RawMessage{}
	for name, raw := range members {
		if !registeredNames[name] {
			custom[name] = raw
		}
	}
	var temp reflect.Value
	var validatorInput any
	if targetType != nil {
		data, err := json.Marshal(custom)
		if err != nil {
			return nil, reflect.Value{}, claimErr("", "custom claims cannot be decoded")
		}
		temp = reflect.New(targetType)
		if err := strictjson.DecodeNumbers(data, temp.Interface()); err != nil {
			return nil, reflect.Value{}, claimErr("", "custom claims do not match the target")
		}
		validatorInput = temp.Interface()
	}
	if p.validator != nil {
		if validatorInput == nil {
			normalized, err := normalize(custom)
			if err != nil {
				return nil, reflect.Value{}, err
			}
			validatorInput = normalized
		}
		if err := callValidator(ctx, p.validator, c.clone(), validatorInput); err != nil {
			return nil, reflect.Value{}, err
		}
	}
	out.Registered = c
	return out, temp, nil
}

// checkClaims applies the profile policy with one clock snapshot.
func (s *Service) checkClaims(pol *jwtPolicy, c RegisteredClaims) error {
	switch {
	case c.Issuer == "":
		return claimErr("iss", "required")
	case c.Subject == "":
		return claimErr("sub", "required")
	case len(c.Audience) == 0:
		return claimErr("aud", "required")
	case c.ExpiresAt == nil:
		return claimErr("exp", "required")
	case c.IssuedAt == nil:
		return claimErr("iat", "required")
	case c.ID == "":
		return claimErr("jti", "required")
	}
	if err := checkIdentity(c); err != nil {
		return err
	}
	if c.Issuer != pol.issuer {
		return newError(ErrInvalidIssuer, "", "", "iss", "unexpected issuer")
	}
	if !slices.ContainsFunc(c.Audience, func(a string) bool { return slices.Contains(pol.audiences, a) }) {
		return newError(ErrInvalidAudience, "", "", "aud", "no accepted audience")
	}

	now := s.clock().Unix()
	skew := int64(pol.skew / time.Second)
	exp, iat := int64(*c.ExpiresAt), int64(*c.IssuedAt)
	// all dates are within years 0001-9999, so these sums cannot overflow
	if now >= exp+skew {
		return newError(ErrExpired, "", "", "exp", "token expired")
	}
	if c.NotBefore != nil {
		nbf := int64(*c.NotBefore)
		if now+skew < nbf {
			return newError(ErrNotYetValid, "", "", "nbf", "token not yet valid")
		}
		if iat > nbf || nbf >= exp {
			return claimErr("nbf", "dates must satisfy iat <= nbf < exp")
		}
	}
	if iat > now+skew {
		return claimErr("iat", "issued in the future")
	}
	if lifetime := exp - iat; lifetime <= 0 || lifetime > int64(pol.maxTTL/time.Second) {
		return claimErr("exp", "lifetime exceeds max_ttl")
	}
	return nil
}

// checkIdentity rejects control characters in identity strings.
func checkIdentity(c RegisteredClaims) error {
	for name, v := range map[string]string{"iss": c.Issuer, "sub": c.Subject, "jti": c.ID} {
		if hasControl(v) {
			return claimErr(name, "control characters are not allowed")
		}
	}
	for _, a := range c.Audience {
		if a == "" || hasControl(a) {
			return claimErr("aud", "invalid audience value")
		}
	}
	return nil
}

func addSeconds(d NumericDate, secs int64) (NumericDate, bool) {
	sum := int64(d) + secs
	if (secs > 0 && sum < int64(d)) || !NumericDate(sum).valid() {
		return 0, false
	}
	return NumericDate(sum), true
}

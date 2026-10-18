package jose

import (
	"context"
	"slices"

	gojose "github.com/go-jose/go-jose/v4"
)

// VerifiedMessage is returned only after the signature was verified with a
// key and algorithm allowed by the profile.
type VerifiedMessage struct {
	Payload []byte
	Header  Header
}

// Sign produces a compact JWS of payload with a jws profile. The payload is
// arbitrary bytes (possibly empty) and stays readable: JWS gives integrity,
// not confidentiality.
func (s *Service) Sign(ctx context.Context, profile string, payload []byte) (string, error) {
	const op = "sign"
	snap, err := s.acquire(ctx, op, profile)
	if err != nil {
		return "", err
	}
	defer s.release()
	p, err := snap.resolve(op, profile, KindJWS)
	if err != nil {
		return "", err
	}
	if !p.canSign() {
		return "", newError(ErrOperationUnavailable, op, profile, "", "profile has no signing key")
	}
	out, err := s.signCompact(p, payload)
	if err != nil {
		return "", withOp(err, op, profile)
	}
	return out, nil
}

// Verify checks a compact JWS with a jws profile and returns its payload.
func (s *Service) Verify(ctx context.Context, profile, compact string) (*VerifiedMessage, error) {
	const op = "verify"
	snap, err := s.acquire(ctx, op, profile)
	if err != nil {
		return nil, err
	}
	defer s.release()
	p, err := snap.resolve(op, profile, KindJWS)
	if err != nil {
		return nil, err
	}
	if !p.canVerify() {
		return nil, newError(ErrOperationUnavailable, op, profile, "", "profile has no verification keys")
	}
	payload, header, err := s.verifyCompact(ctx, p, compact)
	if err != nil {
		return nil, withOp(err, op, profile)
	}
	return &VerifiedMessage{Payload: payload, Header: header}, nil
}

// signCompact signs payload with the profile's key, emitting the protected
// alg, kid and typ headers.
func (s *Service) signCompact(p *profile, payload []byte) (string, error) {
	if int64(len(payload)) > s.cfg.Limits.MaxPayloadBytes {
		return "", newError(ErrPayloadTooLarge, "", "", "", "payload exceeds limits.max_payload_bytes")
	}
	opts := (&gojose.SignerOptions{}).WithType(gojose.ContentType(p.typ)).WithHeader(gojose.HeaderKey("kid"), p.signKey.kid)
	signer, err := gojose.NewSigner(gojose.SigningKey{Algorithm: gojose.SignatureAlgorithm(p.signAlg), Key: p.signKey.signingKey()}, opts)
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot create signer")
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "signing failed")
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot serialize jws")
	}
	if int64(len(out)) > s.cfg.Limits.MaxTokenBytes {
		return "", newError(ErrPayloadTooLarge, "", "", "", "token would exceed limits.max_token_bytes")
	}
	return out, nil
}

// verifyCompact runs the defensive parsing pipeline and then the signature
// check. The returned payload is a fresh buffer.
func (s *Service) verifyCompact(ctx context.Context, p *profile, input string) ([]byte, Header, error) {
	c, err := parseCompact(input, headerRules{segments: jwsSegments}, s.cfg.Limits)
	if err != nil {
		return nil, Header{}, err
	}
	h := c.header
	if h.Type != p.typ {
		return nil, Header{}, newError(ErrInvalidType, "", "", "header.typ", "unexpected type")
	}
	if !slices.Contains(p.verifyAlgs, h.Algorithm) {
		return nil, Header{}, newError(ErrUnsupportedAlgorithm, "", "", "header.alg", "algorithm not allowed by the profile")
	}
	k := p.verifyKeys[h.KeyID]
	if k == nil {
		return nil, Header{}, newError(ErrUnknownKey, "", "", "header.kid", "key not accepted by the profile")
	}
	if k.alg != h.Algorithm {
		return nil, Header{}, newError(ErrUnsupportedAlgorithm, "", "", "header.alg", "algorithm does not match the key")
	}

	// cheap structural prechecks before any cryptography
	if int64(len(c.segments[1])) > int64(b64Len(s.cfg.Limits.MaxPayloadBytes)) {
		return nil, Header{}, newError(ErrPayloadTooLarge, "", "", "", "payload exceeds limits.max_payload_bytes")
	}
	sig, err := c.segment(2)
	if err != nil {
		return nil, Header{}, err
	}
	if want := signatureLen(k); len(sig) != want {
		return nil, Header{}, newError(ErrInvalidSignature, "", "", "", "unexpected signature length")
	}
	if _, err := c.segment(1); err != nil {
		return nil, Header{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, Header{}, ctxError(err, "", "")
	}

	obj, err := gojose.ParseSignedCompact(input, []gojose.SignatureAlgorithm{gojose.SignatureAlgorithm(k.alg)})
	if err != nil || len(obj.Signatures) != 1 {
		return nil, Header{}, malformed("", "unparsable jws")
	}
	got := obj.Signatures[0].Protected
	if got.Algorithm != h.Algorithm || got.KeyID != h.KeyID {
		return nil, Header{}, malformed("header", "header mismatch")
	}
	payload, err := obj.Verify(k.verificationKey())
	if err != nil {
		return nil, Header{}, newError(ErrInvalidSignature, "", "", "", "signature verification failed")
	}
	if int64(len(payload)) > s.cfg.Limits.MaxPayloadBytes {
		return nil, Header{}, newError(ErrPayloadTooLarge, "", "", "", "payload exceeds limits.max_payload_bytes")
	}
	return slices.Clone(payload), h, nil
}

// signatureLen is the exact signature size of the key's algorithm.
func signatureLen(k *key) int {
	switch k.alg {
	case EdDSA, ES256:
		return 64
	case HS256:
		return 32
	default:
		return k.rsaSize()
	}
}

// b64Len is the unpadded base64url length of n bytes.
func b64Len(n int64) int64 { return (n*8 + 5) / 6 }

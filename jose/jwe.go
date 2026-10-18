package jose

import (
	"context"
	"slices"

	gojose "github.com/go-jose/go-jose/v4"
)

// DecryptedMessage is returned only after authenticated decryption
// succeeded. With public-key encryption anyone holding the recipient public
// key can produce a valid JWE, so decryption alone does not establish a
// trusted sender.
type DecryptedMessage struct {
	Plaintext []byte
	Header    Header
}

// Encrypt produces a compact JWE of plaintext with a jwe profile. IVs and
// content keys always come from crypto/rand inside the JOSE library; with
// dir, rotate keys before GCM nonce-collision limits matter.
func (s *Service) Encrypt(ctx context.Context, profile string, plaintext []byte) (string, error) {
	const op = "encrypt"
	snap, err := s.acquire(ctx, op, profile)
	if err != nil {
		return "", err
	}
	defer s.release()
	p, err := snap.resolve(op, profile, KindJWE)
	if err != nil {
		return "", err
	}
	if !p.canEncrypt() {
		return "", newError(ErrOperationUnavailable, op, profile, "", "profile has no encryption key")
	}
	out, err := s.encryptCompact(p, plaintext, "")
	if err != nil {
		return "", withOp(err, op, profile)
	}
	return out, nil
}

// Decrypt opens a compact JWE with a jwe profile.
func (s *Service) Decrypt(ctx context.Context, profile, compact string) (*DecryptedMessage, error) {
	const op = "decrypt"
	snap, err := s.acquire(ctx, op, profile)
	if err != nil {
		return nil, err
	}
	defer s.release()
	p, err := snap.resolve(op, profile, KindJWE)
	if err != nil {
		return nil, err
	}
	if !p.canDecrypt() {
		return nil, newError(ErrOperationUnavailable, op, profile, "", "profile has no decryption keys")
	}
	plaintext, header, err := s.decryptCompact(ctx, p, compact, "")
	if err != nil {
		return nil, withOp(err, op, profile)
	}
	return &DecryptedMessage{Plaintext: plaintext, Header: header}, nil
}

// encryptCompact encrypts with the profile's key, emitting the protected
// alg, enc, kid, typ and (for nested JWTs) cty headers.
func (s *Service) encryptCompact(p *profile, plaintext []byte, cty string) (string, error) {
	if int64(len(plaintext)) > s.cfg.Limits.MaxPayloadBytes {
		return "", newError(ErrPayloadTooLarge, "", "", "", "plaintext exceeds limits.max_payload_bytes")
	}
	opts := (&gojose.EncrypterOptions{}).WithType(gojose.ContentType(p.typ))
	if cty != "" {
		opts = opts.WithContentType(gojose.ContentType(cty))
	}
	rcpt := gojose.Recipient{Algorithm: gojose.KeyAlgorithm(p.encAlg), Key: p.encKey.verificationKey(), KeyID: p.encKey.kid}
	enc, err := gojose.NewEncrypter(gojose.ContentEncryption(p.contentEnc), rcpt, opts)
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot create encrypter")
	}
	obj, err := enc.Encrypt(plaintext)
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "encryption failed")
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		return "", newError(ErrInternal, "", "", "", "cannot serialize jwe")
	}
	if int64(len(out)) > s.cfg.Limits.MaxTokenBytes {
		return "", newError(ErrPayloadTooLarge, "", "", "", "token would exceed limits.max_token_bytes")
	}
	return out, nil
}

// decryptCompact runs the defensive parsing pipeline and then authenticated
// decryption. wantCty is "JWT" for the outer layer of a nested token and
// empty otherwise (cty is then rejected).
func (s *Service) decryptCompact(ctx context.Context, p *profile, input, wantCty string) ([]byte, Header, error) {
	c, err := parseCompact(input, headerRules{segments: jweSegments, allowCty: wantCty != "", needEnc: true}, s.cfg.Limits)
	if err != nil {
		return nil, Header{}, err
	}
	h := c.header
	if h.Type != p.typ {
		return nil, Header{}, newError(ErrInvalidType, "", "", "header.typ", "unexpected type")
	}
	if h.ContentType != wantCty {
		return nil, Header{}, newError(ErrInvalidType, "", "", "header.cty", "unexpected content type")
	}
	if !slices.Contains(p.decAlgs, h.Algorithm) {
		return nil, Header{}, newError(ErrUnsupportedAlgorithm, "", "", "header.alg", "algorithm not allowed by the profile")
	}
	if h.Encryption != p.contentEnc {
		return nil, Header{}, newError(ErrUnsupportedAlgorithm, "", "", "header.enc", "content encryption not allowed by the profile")
	}
	k := p.decKeys[h.KeyID]
	if k == nil {
		return nil, Header{}, newError(ErrUnknownKey, "", "", "header.kid", "key not accepted by the profile")
	}
	if k.alg != h.Algorithm {
		return nil, Header{}, newError(ErrUnsupportedAlgorithm, "", "", "header.alg", "algorithm does not match the key")
	}

	// structural prechecks before any cryptography
	encKey, err := c.segment(1)
	if c.segments[1] != "" && err != nil {
		return nil, Header{}, err
	}
	switch k.alg {
	case Dir:
		if c.segments[1] != "" {
			return nil, Header{}, malformed("encrypted_key", "must be empty for dir")
		}
	case RSAOAEP256:
		if len(encKey) != k.rsaSize() {
			return nil, Header{}, malformed("encrypted_key", "unexpected length")
		}
	}
	iv, err := c.segment(2)
	if err != nil || len(iv) != gcmIVLen {
		return nil, Header{}, malformed("iv", "unexpected length")
	}
	tag, err := c.segment(4)
	if err != nil || len(tag) != gcmTagLen {
		return nil, Header{}, malformed("tag", "unexpected length")
	}
	if int64(len(c.segments[3])) > b64Len(s.cfg.Limits.MaxPayloadBytes) {
		return nil, Header{}, newError(ErrPayloadTooLarge, "", "", "", "ciphertext exceeds limits.max_payload_bytes")
	}
	if _, err := c.segment(3); err != nil {
		return nil, Header{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, Header{}, ctxError(err, "", "")
	}

	obj, err := gojose.ParseEncryptedCompact(input,
		[]gojose.KeyAlgorithm{gojose.KeyAlgorithm(k.alg)},
		[]gojose.ContentEncryption{gojose.ContentEncryption(p.contentEnc)})
	if err != nil {
		return nil, Header{}, malformed("", "unparsable jwe")
	}
	if obj.Header.Algorithm != h.Algorithm || obj.Header.KeyID != h.KeyID {
		return nil, Header{}, malformed("header", "header mismatch")
	}
	plaintext, err := obj.Decrypt(k.signingKey())
	if err != nil {
		return nil, Header{}, newError(ErrDecryption, "", "", "", "authenticated decryption failed")
	}
	if int64(len(plaintext)) > s.cfg.Limits.MaxPayloadBytes {
		return nil, Header{}, newError(ErrPayloadTooLarge, "", "", "", "plaintext exceeds limits.max_payload_bytes")
	}
	return slices.Clone(plaintext), h, nil
}

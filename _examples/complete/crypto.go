package main

import (
	"encoding/base64"
	"log/slog"
	"os"

	ncrypto "github.com/ams-soft/nforge/crypto"
)

func runCrypto(log *slog.Logger) {
	key, err := ncrypto.Key()
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	var c ncrypto.Crypto
	c, err = ncrypto.NewChaCha20(key)
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	aad := []byte("tenant:123")
	encrypted, err := c.Encrypt([]byte("Olá, Nano Forge!"), aad)
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	plain, err := c.Decrypt(encrypted, aad)
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	log.Info("[CryptoModule Test] encrypted:", "result", base64.StdEncoding.EncodeToString(encrypted))
	log.Info("[CryptoModule Test] decrypted:", "result", string(plain))

	macKey, err := ncrypto.Key() // Separate key for HMAC.
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	mac, err := ncrypto.NewHMAC256(macKey)
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	signature := mac.Sign(plain)
	log.Info("[CryptoModule Test] HMAC valid:", "result", mac.Verify(plain, signature))

	h, err := ncrypto.NewHash(ncrypto.HashConfig{})
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	for _, fn := range []func(string) (string, error){h.Bcrypt, h.Argon2id} {
		encoded, err := fn("senha de demonstração")
		if err != nil {
			log.Error("err:", slog.String("err", err.Error()))
			os.Exit(1)
		}
		log.Info("[CryptoModule Test] password hash:", "result", encoded)
	}
	log.Info("[CryptoModule Test] BLAKE3:", "result", h.Blake3(plain))
	token, err := ncrypto.String(32)
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	id, err := ncrypto.ULID()
	if err != nil {
		log.Error("err:", slog.String("err", err.Error()))
		os.Exit(1)
	}
	log.Info("[CryptoModule Test] token:", "result", token)
	log.Info("[CryptoModule Test] ULID:", "result", id)
}

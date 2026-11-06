// Package gateway gives one CI run its own key on a LiteLLM proxy. The agent's
// job draws the key and seals it to the gateway's sealing key; a job that holds
// the proxy's admin key opens it and registers it with the run's budget; the
// agent runs on it; a last job deletes it. No key crosses a job in the clear.
package gateway

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	keyPrefix     = "sk-"
	sealingLabel  = "costroid gateway sealing key v1"
	requestFormat = "costroid gateway request v1"
)

var (
	kem   = hpke.DHKEM(ecdh.X25519())
	kdf   = hpke.HKDFSHA256()
	aead  = hpke.AES256GCM()
	coder = base64.RawURLEncoding
)

// NewKey draws a key for one run: 128 random bits after LiteLLM's prefix.
func NewKey() string {
	return keyPrefix + rand.Text()
}

// KeyID is the key's identifier: the SHA-256 of the key in hex, which is what
// LiteLLM stores in place of the key and writes on its spend logs.
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// sealingKey derives the gateway's sealing key pair from its admin key, so the
// pair needs no secret of its own. Only a holder of the admin key can open a
// request.
func sealingKey(adminKey string) (hpke.PrivateKey, error) {
	if adminKey == "" {
		return nil, errors.New("gateway: the admin key is empty")
	}
	mac := hmac.New(sha256.New, []byte(adminKey))
	mac.Write([]byte(sealingLabel))
	private, err := kem.DeriveKeyPair(mac.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("gateway: derive the sealing key: %w", err)
	}
	return private, nil
}

// PublicKey is the public half of the sealing key, as a repository variable
// holds it.
func PublicKey(adminKey string) (string, error) {
	private, err := sealingKey(adminKey)
	if err != nil {
		return "", err
	}
	return coder.EncodeToString(private.PublicKey().Bytes()), nil
}

func seal(publicKey string, binding Binding, key string) (string, error) {
	raw, err := coder.DecodeString(publicKey)
	if err != nil {
		return "", fmt.Errorf("gateway: the sealing key is not base64url: %w", err)
	}
	public, err := kem.NewPublicKey(raw)
	if err != nil {
		return "", fmt.Errorf("gateway: the sealing key is not an X25519 key: %w", err)
	}
	sealed, err := hpke.Seal(public, kdf, aead, binding.info(), []byte(key))
	if err != nil {
		return "", fmt.Errorf("gateway: seal the key: %w", err)
	}
	return coder.EncodeToString(sealed), nil
}

func open(adminKey string, binding Binding, sealed string) (string, error) {
	private, err := sealingKey(adminKey)
	if err != nil {
		return "", err
	}
	raw, err := coder.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("gateway: the sealed key is not base64url: %w", err)
	}
	key, err := hpke.Open(private, kdf, aead, binding.info(), raw)
	if err != nil {
		return "", errors.New("gateway: the request does not open with this admin key for this run: it was sealed to another key, or for another repository, run or attempt")
	}
	return string(key), nil
}

// Package gateway gives one CI run its own key on a LiteLLM proxy. The agent's
// job draws the key and seals it to the gateway's sealing key; a job that holds
// the sealing key's private half and the proxy's admin key opens it and
// registers it with the run's budget; the agent runs on it; a last job deletes
// it. No key crosses a job in the clear.
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
	"strings"
)

const (
	keyPrefix     = "sk-"
	requestFormat = "costroid gateway request v2"
	// SealingKeyPrefix starts the sealing key's private half, so it is never
	// taken for the public half.
	SealingKeyPrefix = "costroid-sealing-key-v1-"
	// legacyLabel is what earlier versions derived the sealing key from the
	// admin key with. It is kept only to recognise such a key and refuse it.
	legacyLabel = "costroid gateway sealing key v1"
)

// ErrDerivedSealingKey is a request sealed to a public key derived from the
// admin key, as versions before keygen made it. Anyone who reads that public
// key can test guesses at the admin key offline.
var ErrDerivedSealingKey = errors.New("gateway: the request is sealed to a public key derived from the admin key, which anyone who reads it can test guesses at the admin key against. " +
	"Run costroid gateway keygen, store the private half as the environment secret COSTROID_GATEWAY_SEALING_KEY and the public half as the variable COSTROID_GATEWAY_PUBLIC_KEY, and rotate the admin key")

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

// NewSealingKey draws a sealing key pair at random. It returns the private
// half, for an environment secret, and the public half, for a repository
// variable. Nothing about the pair depends on the admin key.
func NewSealingKey() (private, public string, err error) {
	pair, err := kem.GenerateKey()
	if err != nil {
		return "", "", fmt.Errorf("gateway: draw the sealing key: %w", err)
	}
	raw, err := pair.Bytes()
	if err != nil {
		return "", "", fmt.Errorf("gateway: encode the sealing key: %w", err)
	}
	return SealingKeyPrefix + coder.EncodeToString(raw), coder.EncodeToString(pair.PublicKey().Bytes()), nil
}

// PublicKey is the public half of a sealing key's private half.
func PublicKey(sealingKey string) (string, error) {
	private, err := readSealingKey(sealingKey)
	if err != nil {
		return "", err
	}
	return coder.EncodeToString(private.PublicKey().Bytes()), nil
}

func readSealingKey(sealingKey string) (hpke.PrivateKey, error) {
	encoded, ok := strings.CutPrefix(sealingKey, SealingKeyPrefix)
	if !ok {
		return nil, fmt.Errorf("gateway: the sealing key does not start with %s: it is the private half costroid gateway keygen wrote", SealingKeyPrefix)
	}
	raw, err := coder.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("gateway: the sealing key is not base64url after its prefix")
	}
	private, err := kem.NewPrivateKey(raw)
	if err != nil {
		return nil, errors.New("gateway: the sealing key is not an X25519 private key")
	}
	return private, nil
}

// derivedPublicKey is the public key an earlier version derived from the admin
// key. It is computed only to refuse a request sealed to it.
func derivedPublicKey(adminKey string) string {
	mac := hmac.New(sha256.New, []byte(adminKey))
	mac.Write([]byte(legacyLabel))
	private, err := kem.DeriveKeyPair(mac.Sum(nil))
	if err != nil {
		return ""
	}
	return coder.EncodeToString(private.PublicKey().Bytes())
}

func seal(publicKey string, binding Binding, key string) (string, error) {
	if strings.HasPrefix(publicKey, SealingKeyPrefix) {
		return "", errors.New("gateway: COSTROID_GATEWAY_PUBLIC_KEY holds the sealing key's private half, which is now readable by anyone who reads the variable. " +
			"Delete the variable, run costroid gateway keygen again, and store the new private half only as the environment secret COSTROID_GATEWAY_SEALING_KEY")
	}
	raw, err := coder.DecodeString(publicKey)
	if err != nil {
		return "", fmt.Errorf("gateway: the public key is not base64url: %w", err)
	}
	public, err := kem.NewPublicKey(raw)
	if err != nil {
		return "", fmt.Errorf("gateway: the public key is not an X25519 key: %w", err)
	}
	sealed, err := hpke.Seal(public, kdf, aead, binding.info(), []byte(key))
	if err != nil {
		return "", fmt.Errorf("gateway: seal the key: %w", err)
	}
	return coder.EncodeToString(sealed), nil
}

func open(sealingKey string, binding Binding, sealed string) (string, error) {
	private, err := readSealingKey(sealingKey)
	if err != nil {
		return "", err
	}
	raw, err := coder.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("gateway: the sealed key is not base64url: %w", err)
	}
	key, err := hpke.Open(private, kdf, aead, binding.info(), raw)
	if err != nil {
		return "", errors.New("gateway: the request does not open with this sealing key for this run: it was sealed to another key, or for another repository, run or attempt")
	}
	return string(key), nil
}

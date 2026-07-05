// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package credentials implements Costroid's encrypted credential store
// (decisions D17, D32): the AI-vendor slice is the first source that
// genuinely needs stored secrets (Admin API keys), which D24 deferred the
// store to exactly.
//
// # Mechanism (D32)
//
// A random 256-bit key lives in a key file OUTSIDE the data directory
// (default ~/.config/costroid/credentials.key), written 0600 and
// permission-checked on every use. Each secret is sealed with AES-256-GCM
// — Go standard-library crypto only — under a fresh random 96-bit nonce,
// with the credential NAME bound as the additional authenticated data, and
// the (nonce, ciphertext) pair is stored in the DuckDB store (migration
// 0005). The key never enters the database, so a database backup alone
// exposes nothing; and a ciphertext moved to a different slot name fails to
// decrypt, because the name is authenticated.
//
// # Secret handling
//
// Secrets enter via stdin only (never argv, never env), are never printed,
// logged, or listed, and are handed to connectors as a Secret value whose
// String/GoString/MarshalJSON all render "[redacted]" — only the explicit
// Reveal method yields the plaintext, called exactly once at header
// injection. The key file's path (never the key material) may be overridden
// by the COSTROID_CREDENTIALS_KEY_FILE environment variable or a flag.
package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Costroid/costroid/internal/storage"
)

// KeyEnvVar is the environment variable that overrides the key file PATH
// (never the key material — the key never travels through the environment).
const KeyEnvVar = "COSTROID_CREDENTIALS_KEY_FILE"

// keySize is the AES-256 key length in bytes. aes.NewCipher silently
// accepts 16- and 24-byte keys (AES-128/192), so the loader enforces
// exactly 32 to prevent a shorter key from downgrading the cipher.
const keySize = 32

// Secret is a credential plaintext that never reveals itself through fmt or
// json. It implements fmt.Formatter on the value receiver, so EVERY fmt verb
// — not just the Stringer-aware ones — renders "[redacted]"; String,
// GoString, and MarshalJSON do too, for the non-fmt call sites (and direct
// calls). Every method is on the value receiver, so a Secret leaks nothing
// even nested in a struct or slice; only Reveal returns the plaintext.
type Secret struct {
	value string
}

// NewSecret wraps a plaintext secret. Prefer obtaining secrets from a
// Vault; this exists for the header-injection and test call sites.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the plaintext. It is the ONLY method that does so; call it
// exactly once, at the point the secret is used (e.g. header injection).
func (s Secret) Reveal() string { return s.value }

// Format implements fmt.Formatter (value receiver). fmt calls it for EVERY
// verb — including the numeric/character verbs (%d %f %g %c %U %b %o %e) that
// bypass Stringer and would otherwise reflect over the unexported field and
// print the plaintext bytes. It ignores the verb and flags and always writes
// the redaction marker.
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "[redacted]")
}

// String implements fmt.Stringer (value receiver). With Format present, fmt
// no longer calls String, but it stays for direct String() calls and any
// non-fmt Stringer consumer.
func (s Secret) String() string { return "[redacted]" }

// GoString implements fmt.GoStringer (value receiver) so %#v does not leak.
func (s Secret) GoString() string { return "[redacted]" }

// MarshalJSON implements json.Marshaler (value receiver) so json.Marshal
// does not leak, including when a Secret is a struct field or slice element.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// Backend is the slice of storage a Vault seals secrets into and reads
// ciphertext back from (satisfied by *storage.DuckDB).
type Backend interface {
	PutCredential(ctx context.Context, cred storage.Credential) error
	GetCredential(ctx context.Context, name string) (storage.Credential, bool, error)
}

// DefaultKeyPath is the key file's default location: os.UserConfigDir()/
// costroid/credentials.key (~/.config/costroid/credentials.key), placed
// deliberately OUTSIDE the data directory so a database backup alone leaks
// nothing (decision D32).
func DefaultKeyPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the user config directory for the default key file: %w", err)
	}
	return filepath.Join(dir, "costroid", "credentials.key"), nil
}

// ResolveKeyPath applies the path override precedence (decision D32):
// the flag value wins over the COSTROID_CREDENTIALS_KEY_FILE environment
// variable (which carries the PATH, never key material), which wins over
// the default location.
func ResolveKeyPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv(KeyEnvVar); env != "" {
		return env, nil
	}
	return DefaultKeyPath()
}

// InitKeyFile generates a fresh 256-bit key and writes it base64-encoded on
// a single line to path, mode 0600, creating the parent directory 0700 if
// missing. It uses O_CREATE|O_EXCL|O_WRONLY so an existing key file is
// REFUSED rather than overwritten — overwriting would make every stored
// credential undecryptable — with no stat-then-write race.
func InitKeyFile(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating key file directory %s: %w", dir, err)
		}
	}
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("generating random key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("a credential key file already exists at %s — refusing to overwrite it, because a new "+
				"key would make every stored credential undecryptable; remove it deliberately to rotate", path)
		}
		return fmt.Errorf("creating key file %s: %w", path, err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	_, writeErr := f.WriteString(encoded + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("writing key file %s: %w", path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("writing key file %s: %w", path, closeErr)
	}
	return nil
}

// LoadKey reads and validates the key at path. It re-checks permissions on
// every load — refusing a group- or world-accessible key file with an
// actionable chmod message — and refuses any content that does not
// base64-decode to exactly 32 bytes (aes.NewCipher would silently accept a
// 16- or 24-byte key and downgrade AES-256). It never echoes file contents.
func LoadKey(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no credential key file at %s — run `costroid credentials init` to create one "+
				"(decision D32)", path)
		}
		return nil, fmt.Errorf("opening key file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stating key file %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("credential key file %s is group- or world-accessible (mode %04o) — it must be "+
			"readable only by you; run `chmod 600 %s`", path, perm, path)
	}

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading key file %s: %w", path, err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != keySize {
		// Never echo the (possibly partial) contents.
		return nil, fmt.Errorf("credential key file %s is corrupt or not a Costroid key file — it must contain "+
			"exactly 32 random bytes, base64-encoded", path)
	}
	return key, nil
}

// Vault seals and opens named secrets with the loaded key. Build it with
// Open; a Vault holds the key in memory only for the life of the command.
type Vault struct {
	gcm     cipher.AEAD
	backend Backend
}

// Open loads and validates the key at keyPath and returns a Vault backed by
// the given store.
func Open(keyPath string, backend Backend) (*Vault, error) {
	key, err := LoadKey(keyPath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initializing AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initializing AES-GCM: %w", err)
	}
	return &Vault{gcm: gcm, backend: backend}, nil
}

// Set seals secret under name (a fresh random nonce, the name bound as
// GCM additional authenticated data) and stores it, replacing any existing
// slot of that name.
func (v *Vault) Set(ctx context.Context, name, secret string) error {
	nonce := make([]byte, v.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}
	ciphertext := v.gcm.Seal(nil, nonce, []byte(secret), []byte(name))
	return v.backend.PutCredential(ctx, storage.Credential{
		Name:       name,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	})
}

// Get opens the named secret. A missing slot and an authentication failure
// both return actionable errors that never include any plaintext bytes: a
// missing slot names the slot and suggests `credentials set`, and a GCM
// authentication failure points at a key-file mismatch (the name is bound
// as AAD, so a ciphertext moved to another slot also fails here).
func (v *Vault) Get(ctx context.Context, name string) (Secret, error) {
	cred, found, err := v.backend.GetCredential(ctx, name)
	if err != nil {
		return Secret{}, err
	}
	if !found {
		return Secret{}, fmt.Errorf("no credential named %q is stored — add it with `costroid credentials set %s` "+
			"(the secret is read from stdin)", name, name)
	}
	// Guard the stored row's shape before Open: gcm.Open PANICS on a nonce
	// whose length is not gcm.NonceSize(), so a truncated/corrupt/tampered row
	// must fail with an actionable error, not a panic. A ciphertext shorter
	// than the GCM tag cannot be authentic either.
	if len(cred.Nonce) != v.gcm.NonceSize() || len(cred.Ciphertext) < v.gcm.Overhead() {
		return Secret{}, fmt.Errorf("credential %q is corrupt or was tampered with: its stored nonce/ciphertext have "+
			"the wrong length — restore the store from a good backup or re-run `costroid credentials set %s`", name, name)
	}
	plaintext, err := v.gcm.Open(nil, cred.Nonce, cred.Ciphertext, []byte(name))
	if err != nil {
		// Never dump the ciphertext or any partial plaintext.
		return Secret{}, fmt.Errorf("credential %q could not be decrypted: the key file does not match the one that "+
			"encrypted it (or the stored ciphertext was moved between slots) — check $%s points at the correct key "+
			"file", name, KeyEnvVar)
	}
	return Secret{value: string(plaintext)}, nil
}

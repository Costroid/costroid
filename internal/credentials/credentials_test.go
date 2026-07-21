// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package credentials_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/storage"
)

func openStore(t *testing.T) *storage.DuckDB {
	t.Helper()
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// initKey writes a fresh key file in a temp dir and returns its path.
func initKey(t *testing.T) string {
	t.Helper()
	// Nest under a not-yet-existing directory so InitKeyFile creates it
	// (and the 0700 mode assertion in TestKeyFileInitAndLoad is meaningful).
	path := filepath.Join(t.TempDir(), "config", "costroid", "credentials.key")
	if err := credentials.InitKeyFile(path); err != nil {
		t.Fatalf("InitKeyFile: %v", err)
	}
	return path
}

const secretValue = "SUPERSECRETTOKEN-sk-ant-admin01-leakcanary"

// TestSecretNeverLeaks proves a Secret renders "[redacted]" through every
// fmt verb and json.Marshal — including nested in a struct and a slice —
// and only Reveal yields the plaintext.
func TestSecretNeverLeaks(t *testing.T) {
	s := credentials.NewSecret(secretValue)
	if s.Reveal() != secretValue {
		t.Fatalf("Reveal = %q, want the plaintext", s.Reveal())
	}

	type holder struct {
		Name  string
		Token credentials.Secret
	}
	h := holder{Name: "anthropic-cost", Token: s}
	slice := []credentials.Secret{s, s}

	jsonH, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("json.Marshal(struct): %v", err)
	}
	jsonSlice, err := json.Marshal(slice)
	if err != nil {
		t.Fatalf("json.Marshal(slice): %v", err)
	}

	outputs := map[string]string{
		"%v":          fmt.Sprintf("%v", s),
		"%s":          fmt.Sprintf("%s", s), //nolint:staticcheck // deliberately exercises fmt's %s verb on the Secret to prove it does not leak
		"%+v":         fmt.Sprintf("%+v", s),
		"%#v":         fmt.Sprintf("%#v", s),
		"%x":          fmt.Sprintf("%x", s),
		"%q":          fmt.Sprintf("%q", s),
		"struct %v":   fmt.Sprintf("%v", h),
		"struct %+v":  fmt.Sprintf("%+v", h),
		"struct %#v":  fmt.Sprintf("%#v", h),
		"struct %x":   fmt.Sprintf("%x", h),
		"slice %v":    fmt.Sprintf("%v", slice),
		"slice %#v":   fmt.Sprintf("%#v", slice),
		"json struct": string(jsonH),
		"json slice":  string(jsonSlice),
		"pointer %v":  fmt.Sprintf("%v", &s),
		// stringer call exercises the String method directly (fmt uses Format).
		"stringer call": s.String(),
	}
	// Non-Stringer verbs: without fmt.Formatter these reflect over the
	// unexported field and print the plaintext bytes. Format must render the
	// redaction marker for every one of them — bare, struct-, and
	// slice-embedded. The verb is held in a variable so `go vet`'s printf
	// check (correctly) leaves these intentionally-mismatched verbs alone.
	targets := map[string]any{"bare": s, "struct": h, "slice": slice}
	for _, verb := range []string{"%d", "%f", "%g", "%c", "%U", "%b", "%o", "%e"} {
		for label, v := range targets {
			outputs[label+" "+verb] = fmt.Sprintf(verb, v)
		}
	}
	hexSecret := fmt.Sprintf("%x", secretValue)
	// The hex verbs honor Stringer, so they render the hex of "[redacted]"
	// rather than the literal — still no leak, just a different marker.
	redactedHex := fmt.Sprintf("%x", "[redacted]")
	for label, out := range outputs {
		if strings.Contains(out, secretValue) {
			t.Errorf("%s leaked the plaintext: %q", label, out)
		}
		if strings.Contains(out, hexSecret) {
			t.Errorf("%s leaked the plaintext as hex: %q", label, out)
		}
		if !strings.Contains(out, "[redacted]") && !strings.Contains(out, redactedHex) {
			t.Errorf("%s = %q, want it to render the redaction marker", label, out)
		}
	}
}

// TestKeyFileInitAndLoad proves init writes a 0600 file that loads to a
// 32-byte key and refuses to overwrite an existing key file.
func TestKeyFileInitAndLoad(t *testing.T) {
	path := initKey(t)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file mode = %04o, want 0600", perm)
		}
	}

	key, err := credentials.LoadKey(path)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("loaded key is %d bytes, want 32", len(key))
	}

	// O_EXCL refuses to overwrite.
	err = credentials.InitKeyFile(path)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("re-init = %v, want the refuse-to-overwrite error", err)
	}

	// The parent directory was created 0700.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat key dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("key dir mode = %04o, want 0700", perm)
		}
	}
}

// TestLoadKeyRefusesGroupReadable proves a group/world-accessible key file
// is refused with an actionable chmod message.
func TestLoadKeyRefusesGroupReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credentials.LoadKey skips the mode check on Windows (credentials.go GOOS gate)")
	}
	path := initKey(t)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := credentials.LoadKey(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("LoadKey(group-readable) = %v, want the chmod refusal", err)
	}
}

// TestLoadKeyRefusesShortKey proves a 16-byte key (which aes.NewCipher
// would silently accept as AES-128) is refused — AES-256 must not downgrade.
func TestLoadKeyRefusesShortKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.key")
	sixteen := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if err := os.WriteFile(path, []byte(sixteen+"\n"), 0o600); err != nil {
		t.Fatalf("writing short key: %v", err)
	}
	_, err := credentials.LoadKey(path)
	if err == nil || !strings.Contains(err.Error(), "exactly 32 random bytes") {
		t.Errorf("LoadKey(16-byte) = %v, want the wrong-size refusal", err)
	}
	// The refusal must not echo the file contents.
	if err != nil && strings.Contains(err.Error(), sixteen) {
		t.Errorf("refusal echoed the key contents: %v", err)
	}
}

// TestLoadKeyMissing proves the missing-key-file error is actionable.
func TestLoadKeyMissing(t *testing.T) {
	_, err := credentials.LoadKey(filepath.Join(t.TempDir(), "nope.key"))
	if err == nil || !strings.Contains(err.Error(), "credentials init") {
		t.Errorf("LoadKey(missing) = %v, want the run-init suggestion", err)
	}
}

// TestVaultRoundTrip proves Set then Get returns the exact secret.
func TestVaultRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	vault, err := credentials.Open(initKey(t), store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := vault.Set(ctx, "anthropic-cost", secretValue); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := vault.Get(ctx, "anthropic-cost")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Reveal() != secretValue {
		t.Errorf("Get.Reveal = %q, want the stored secret", got.Reveal())
	}

	// A replace round-trips the new value.
	if err := vault.Set(ctx, "anthropic-cost", "second-value"); err != nil {
		t.Fatalf("Set (replace): %v", err)
	}
	got, err = vault.Get(ctx, "anthropic-cost")
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	if got.Reveal() != "second-value" {
		t.Errorf("after replace Get.Reveal = %q, want second-value", got.Reveal())
	}
}

// TestVaultUnknownSlot proves a missing slot names it and suggests `set`.
func TestVaultUnknownSlot(t *testing.T) {
	vault, err := credentials.Open(initKey(t), openStore(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = vault.Get(context.Background(), "openai-cost")
	if err == nil || !strings.Contains(err.Error(), "credentials set openai-cost") {
		t.Errorf("Get(unknown) = %v, want the set suggestion naming the slot", err)
	}
}

// TestVaultAADSwapFailsDecryption proves the credential NAME is bound as
// GCM AAD (decision D32, acceptance criterion 5): a credentials row's
// ciphertext copied to a DIFFERENT name fails to decrypt.
func TestVaultAADSwapFailsDecryption(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	vault, err := credentials.Open(initKey(t), store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := vault.Set(ctx, "anthropic-cost", secretValue); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Copy the stored row's nonce+ciphertext to another slot name.
	row, found, err := store.GetCredential(ctx, "anthropic-cost")
	if err != nil || !found {
		t.Fatalf("GetCredential: found=%v err=%v", found, err)
	}
	if err := store.PutCredential(ctx, storage.Credential{
		Name:       "openai-cost",
		Nonce:      row.Nonce,
		Ciphertext: row.Ciphertext,
	}); err != nil {
		t.Fatalf("copying ciphertext to another name: %v", err)
	}

	_, err = vault.Get(ctx, "openai-cost")
	if err == nil {
		t.Fatal("decryption of a ciphertext moved to another name succeeded; AAD is not bound to the name")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Errorf("decryption-failure error leaked plaintext: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be decrypted") {
		t.Errorf("AAD-swap error = %v, want the decrypt-failure message", err)
	}
}

// TestVaultTamperedNonceDoesNotPanic proves a stored row whose nonce is the
// wrong length (a truncated/corrupt/tampered row) returns the mandated
// actionable error instead of panicking inside gcm.Open — and never leaks the
// plaintext.
func TestVaultTamperedNonceDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	vault, err := credentials.Open(initKey(t), store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := vault.Set(ctx, "anthropic-cost", secretValue); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Read the good row, then overwrite it with a truncated nonce — the shape
	// a corrupt or tampered store could hold.
	row, found, err := store.GetCredential(ctx, "anthropic-cost")
	if err != nil || !found {
		t.Fatalf("GetCredential: found=%v err=%v", found, err)
	}
	if err := store.PutCredential(ctx, storage.Credential{
		Name:       "anthropic-cost",
		Nonce:      row.Nonce[:4], // wrong length → gcm.Open would panic
		Ciphertext: row.Ciphertext,
	}); err != nil {
		t.Fatalf("writing truncated-nonce row: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Get panicked on a truncated nonce instead of returning an error: %v", r)
		}
	}()
	_, err = vault.Get(ctx, "anthropic-cost")
	if err == nil || !strings.Contains(err.Error(), "wrong length") {
		t.Errorf("Get(truncated nonce) = %v, want the corrupt/tampered-row error", err)
	}
	if err != nil && strings.Contains(err.Error(), secretValue) {
		t.Errorf("tampered-row error leaked plaintext: %v", err)
	}
}

// TestVaultWrongKeyFailsDecryption proves a key file that did not encrypt a
// slot cannot open it, with an actionable message and no byte dump.
func TestVaultWrongKeyFailsDecryption(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	writer, err := credentials.Open(initKey(t), store)
	if err != nil {
		t.Fatalf("Open (writer): %v", err)
	}
	if err := writer.Set(ctx, "anthropic-cost", secretValue); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A different key file over the same store.
	reader, err := credentials.Open(initKey(t), store)
	if err != nil {
		t.Fatalf("Open (reader): %v", err)
	}
	_, err = reader.Get(ctx, "anthropic-cost")
	if err == nil || !strings.Contains(err.Error(), credentials.KeyEnvVar) {
		t.Errorf("Get(wrong key) = %v, want the key-mismatch message naming %s", err, credentials.KeyEnvVar)
	}
}

// TestResolveKeyPathPrecedence proves flag > env > default.
func TestResolveKeyPathPrecedence(t *testing.T) {
	t.Setenv(credentials.KeyEnvVar, "/env/path/credentials.key")

	got, err := credentials.ResolveKeyPath("/flag/path/credentials.key")
	if err != nil {
		t.Fatalf("ResolveKeyPath(flag): %v", err)
	}
	if got != "/flag/path/credentials.key" {
		t.Errorf("flag path = %q, want the flag value to win", got)
	}

	got, err = credentials.ResolveKeyPath("")
	if err != nil {
		t.Fatalf("ResolveKeyPath(env): %v", err)
	}
	if got != "/env/path/credentials.key" {
		t.Errorf("env path = %q, want the env value", got)
	}
}

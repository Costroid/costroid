// Package kmstest stands in for AWS KMS in tests. It answers Sign,
// GetParametersForImport and ImportKeyMaterial as KMS's API reference
// describes them, on a loopback server, and never reaches the network. It
// checks the shape of each request's Signature Version 4 header, not its
// HMAC: the signer's own tests hold that to AWS's published test vector.
package kmstest

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	// Region is the region the stub's credentials sign for.
	Region = "eu-central-1"
	// AccessKeyID and SecretAccessKey are the stub's credentials.
	AccessKeyID     = "AKIAKMSTESTEXAMPLE"
	SecretAccessKey = "kmstest-secret-access-key-not-a-real-one"
	// SigningKey holds a key from the start. ImportKey holds none until an
	// import.
	SigningKey = "11111111-2222-3333-4444-555555555555"
	ImportKey  = "66666666-7777-8888-9999-000000000000"

	signingAlgorithm  = "RSASSA_PKCS1_V1_5_SHA_256"
	wrappingAlgorithm = "RSA_AES_KEY_WRAP_SHA_256"
	keyBits           = 2048
)

var (
	authorization = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=` + AccessKeyID + `/[0-9]{8}/` + Region +
		`/kms/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-target(;x-amz-security-token)?, Signature=[0-9a-f]{64}$`)

	keysOnce sync.Once
	keys     [2]*rsa.PrivateKey
	keysErr  error
)

// generated draws the signing key and the wrapping key once per test binary.
func generated(t testing.TB) (signing, wrapping *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		for i := range keys {
			keys[i], keysErr = rsa.GenerateKey(rand.Reader, keyBits)
			if keysErr != nil {
				return
			}
		}
	})
	if keysErr != nil {
		t.Fatal(keysErr)
	}
	return keys[0], keys[1]
}

// Call is one request the stub took.
type Call struct {
	Action string
	Header http.Header
	Body   []byte
}

// KMS is the stub. Fail maps an action to the error type it answers with
// 400 in place of its work.
type KMS struct {
	Server *httptest.Server
	Fail   map[string]string

	mu       sync.Mutex
	keys     map[string]*rsa.PrivateKey
	wrapping *rsa.PrivateKey
	token    []byte
	calls    []Call
}

// New starts a stub and stops it with the test.
func New(t testing.TB) *KMS {
	t.Helper()
	signing, wrapping := generated(t)
	k := &KMS{Fail: map[string]string{}, keys: map[string]*rsa.PrivateKey{SigningKey: signing}, wrapping: wrapping}
	k.Server = httptest.NewServer(k)
	t.Cleanup(k.Server.Close)
	return k
}

// Getenv answers the AWS variables that point a client at the stub, with no
// session token.
func (k *KMS) Getenv(name string) string {
	return map[string]string{
		"AWS_ACCESS_KEY_ID":     AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": SecretAccessKey,
		"AWS_REGION":            Region,
		"AWS_ENDPOINT_URL_KMS":  k.Server.URL,
	}[name]
}

// Key returns the private key a KMS key holds, or nil.
func (k *KMS) Key(keyID string) *rsa.PrivateKey {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys[keyID]
}

// Calls returns the requests taken so far.
func (k *KMS) Calls() []Call {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.calls)
}

// ServeHTTP answers one action.
func (k *KMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		refuse(w, "SerializationException")
		return
	}
	action, _ := strings.CutPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, Call{Action: action, Header: r.Header.Clone(), Body: body})
	switch {
	case r.Method != http.MethodPost || r.URL.Path != "/" || r.Header.Get("Content-Type") != "application/x-amz-json-1.1":
		refuse(w, "SerializationException")
	case !authorization.MatchString(r.Header.Get("Authorization")) || r.Header.Get("X-Amz-Date") == "":
		refuse(w, "IncompleteSignatureException")
	case k.Fail[action] != "":
		refuse(w, k.Fail[action])
	case action == "Sign":
		k.sign(w, body)
	case action == "GetParametersForImport":
		k.parameters(w, body)
	case action == "ImportKeyMaterial":
		k.importKey(w, body)
	default:
		refuse(w, "UnknownOperationException")
	}
}

func refuse(w http.ResponseWriter, kind string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": kind, "message": "refused by kmstest"})
}

func answer(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(value)
}

// sign signs as KMS does: DIGEST signs the message as a SHA-256 digest, and
// RAW or no type hashes the message first.
func (k *KMS) sign(w http.ResponseWriter, body []byte) {
	var request struct {
		KeyID            string `json:"KeyId"`
		Message          []byte `json:"Message"`
		MessageType      string `json:"MessageType"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	if json.Unmarshal(body, &request) != nil {
		refuse(w, "SerializationException")
		return
	}
	key := k.keys[request.KeyID]
	digest := request.Message
	if request.MessageType != "DIGEST" {
		sum := sha256.Sum256(request.Message)
		digest = sum[:]
	}
	switch {
	case key == nil:
		refuse(w, "NotFoundException")
		return
	case request.SigningAlgorithm != signingAlgorithm:
		refuse(w, "InvalidKeyUsageException")
		return
	case len(digest) != sha256.Size:
		refuse(w, "ValidationException")
		return
	}
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest)
	if err != nil {
		refuse(w, "KMSInternalException")
		return
	}
	answer(w, map[string]any{"KeyId": request.KeyID, "Signature": signature, "SigningAlgorithm": signingAlgorithm})
}

func (k *KMS) parameters(w http.ResponseWriter, body []byte) {
	var request struct {
		KeyID             string `json:"KeyId"`
		WrappingAlgorithm string `json:"WrappingAlgorithm"`
		WrappingKeySpec   string `json:"WrappingKeySpec"`
	}
	if json.Unmarshal(body, &request) != nil || request.WrappingKeySpec == "" {
		refuse(w, "SerializationException")
		return
	}
	if request.WrappingAlgorithm != wrappingAlgorithm {
		refuse(w, "UnsupportedOperationException")
		return
	}
	if request.KeyID != ImportKey || k.keys[request.KeyID] != nil {
		refuse(w, "KMSInvalidStateException")
		return
	}
	public, err := x509.MarshalPKIXPublicKey(&k.wrapping.PublicKey)
	if err != nil {
		refuse(w, "KMSInternalException")
		return
	}
	k.token = []byte("kmstest import token for " + request.KeyID)
	answer(w, map[string]any{"KeyId": request.KeyID, "ImportToken": k.token, "PublicKey": public, "ParametersValidTo": 1.8e9})
}

func (k *KMS) importKey(w http.ResponseWriter, body []byte) {
	var request struct {
		KeyID                string `json:"KeyId"`
		ImportToken          []byte `json:"ImportToken"`
		EncryptedKeyMaterial []byte `json:"EncryptedKeyMaterial"`
		ExpirationModel      string `json:"ExpirationModel"`
	}
	if json.Unmarshal(body, &request) != nil {
		refuse(w, "SerializationException")
		return
	}
	if k.token == nil || !bytes.Equal(request.ImportToken, k.token) {
		refuse(w, "InvalidImportTokenException")
		return
	}
	if request.ExpirationModel != "KEY_MATERIAL_DOES_NOT_EXPIRE" {
		refuse(w, "ValidationException")
		return
	}
	key, err := k.unwrap(request.EncryptedKeyMaterial)
	if err != nil {
		refuse(w, "InvalidCiphertextException")
		return
	}
	k.keys[request.KeyID] = key
	answer(w, map[string]string{"KeyId": request.KeyID})
}

// unwrap reverses RSA_AES_KEY_WRAP_SHA_256 and reads the key as PKCS #8.
func (k *KMS) unwrap(sealed []byte) (*rsa.PrivateKey, error) {
	size := k.wrapping.Size()
	if len(sealed) <= size {
		return nil, errors.New("too short")
	}
	aesKey, err := rsa.DecryptOAEP(sha256.New(), nil, k.wrapping, sealed[:size], nil)
	if err != nil {
		return nil, err
	}
	material, err := UnwrapPadded(aesKey, sealed[size:])
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(material)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return key, nil
}

// UnwrapPadded is AES key unwrap with padding, RFC 5649 section 4.2.
func UnwrapPadded(kek, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < 16 || len(ciphertext)%8 != 0 {
		return nil, errors.New("key unwrap: bad length")
	}
	blocks := len(ciphertext)/8 - 1
	var integrity [8]byte
	plain := make([]byte, blocks*8)
	var buffer [16]byte
	if blocks == 1 {
		block.Decrypt(buffer[:], ciphertext)
		copy(integrity[:], buffer[:8])
		copy(plain, buffer[8:])
	} else {
		copy(integrity[:], ciphertext[:8])
		copy(plain, ciphertext[8:])
		for step := 5; step >= 0; step-- {
			for i := blocks; i >= 1; i-- {
				binary.BigEndian.PutUint64(buffer[:8], binary.BigEndian.Uint64(integrity[:])^uint64(blocks*step+i))
				copy(buffer[8:], plain[(i-1)*8:])
				block.Decrypt(buffer[:], buffer[:])
				copy(integrity[:], buffer[:8])
				copy(plain[(i-1)*8:], buffer[8:])
			}
		}
	}
	length := int(binary.BigEndian.Uint32(integrity[4:]))
	if binary.BigEndian.Uint32(integrity[:4]) != 0xA65959A6 || length <= (blocks-1)*8 || length > blocks*8 {
		return nil, errors.New("key unwrap: integrity check failed")
	}
	if !bytes.Equal(plain[length:], make([]byte, blocks*8-length)) {
		return nil, errors.New("key unwrap: padding is not zero")
	}
	return plain[:length], nil
}

package scm

import (
	"bytes"
	"cmp"
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The AWS KMS calls the App's key needs, over KMS's JSON API, each signed with
// Signature Version 4. AWS documents the protocol at
// https://docs.aws.amazon.com/kms/latest/APIReference/ and the signature at
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html.
const (
	kmsService     = "kms"
	kmsTarget      = "TrentService."
	kmsContentType = "application/x-amz-json-1.1"
	maxKMSAnswer   = 64 << 10

	sigV4Algorithm = "AWS4-HMAC-SHA256"
	sigV4Scope     = "aws4_request"
	amzDate        = "20060102T150405Z"
	amzDay         = "20060102"

	signingAlgorithm  = "RSASSA_PKCS1_V1_5_SHA_256"
	messageDigest     = "DIGEST"
	wrappingAlgorithm = "RSA_AES_KEY_WRAP_SHA_256"
	wrappingKeySpec   = "RSA_4096"
	noExpiry          = "KEY_MATERIAL_DOES_NOT_EXPIRE"
	// minWrappingBits refuses a wrapping key shorter than KMS's shortest spec.
	minWrappingBits = 2048
	aesKeyBytes     = 32
)

// The variables KMSFromEnvironment reads, by the AWS SDKs' own names.
const (
	envAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envSessionToken    = "AWS_SESSION_TOKEN"
	envRegion          = "AWS_REGION"
	envDefaultRegion   = "AWS_DEFAULT_REGION"
	envKMSEndpoint     = "AWS_ENDPOINT_URL_KMS"
	envEndpoint        = "AWS_ENDPOINT_URL"
)

var (
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	// keyARN is arn:PARTITION:kms:REGION:ACCOUNT:key/ID or alias/NAME.
	keyARN = regexp.MustCompile(`^arn:[a-z-]+:kms:([a-z0-9-]+):[0-9]{12}:(key|alias)/`)
)

// KMS calls AWS KMS in one region with the credentials it was given. Its
// errors name the call and AWS's error type, never a key, a token or a
// signature.
type KMS struct {
	endpoint    *url.URL
	region      string
	accessKeyID string
	secretKey   string
	session     string
	client      *http.Client
	now         func() time.Time
}

// KMSFromEnvironment reads AWS's credentials, region and endpoint from the
// environment's variables. A key ARN names its region, which then needs no
// variable. The client is required, as GitHubOptions.Client is.
func KMSFromEnvironment(getenv func(string) string, keyID string, client *http.Client, now func() time.Time) (*KMS, error) {
	access, secret := getenv(envAccessKeyID), getenv(envSecretAccessKey)
	if access == "" || secret == "" {
		return nil, fmt.Errorf("scm: KMS needs %s and %s", envAccessKeyID, envSecretAccessKey)
	}
	region := cmp.Or(getenv(envRegion), getenv(envDefaultRegion))
	if match := keyARN.FindStringSubmatch(keyID); match != nil {
		region = match[1]
	}
	if !regionPattern.MatchString(region) {
		return nil, fmt.Errorf("scm: KMS needs a region: set %s, or name the key by its ARN", envRegion)
	}
	root := cmp.Or(getenv(envKMSEndpoint), getenv(envEndpoint), "https://kms."+region+".amazonaws.com")
	if err := checkRoot(root); err != nil {
		return nil, fmt.Errorf("scm: KMS endpoint: %w", err)
	}
	endpoint, err := url.Parse(strings.TrimRight(root, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("scm: KMS endpoint: %w", err)
	}
	if client == nil {
		return nil, errNoClient
	}
	return &KMS{
		endpoint: endpoint, region: region, accessKeyID: access, secretKey: secret, session: getenv(envSessionToken),
		client: bounded(client), now: now,
	}, nil
}

// Host is the host every call goes to, for an egress allowlist.
func (k *KMS) Host() string {
	return k.endpoint.Hostname()
}

// KMSError is an answer of KMS other than 200.
type KMSError struct {
	Action string
	Status int
	// Type is AWS's error type, such as AccessDeniedException.
	Type string
}

func (e *KMSError) Error() string {
	return fmt.Sprintf("scm: KMS %s answered %d %s", e.Action, e.Status, cmp.Or(e.Type, "with no error type"))
}

// call sends one action and decodes its answer into out.
func (k *KMS) call(ctx context.Context, action string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("scm: encode KMS %s: %w", action, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, k.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("scm: build KMS %s: %w", action, err)
	}
	at := k.now().UTC()
	headers := map[string]string{
		"content-type": kmsContentType,
		"host":         request.URL.Host,
		"x-amz-date":   at.Format(amzDate),
		"x-amz-target": kmsTarget + action,
	}
	if k.session != "" {
		headers["x-amz-security-token"] = k.session
	}
	for name, value := range headers {
		if name != "host" {
			request.Header.Set(name, value)
		}
	}
	request.Header.Set("Authorization", k.authorization(request.URL.EscapedPath(), headers, body, at))
	response, err := k.client.Do(request)
	if err != nil {
		return fmt.Errorf("scm: KMS %s: %w", action, unwrapURL(err))
	}
	defer response.Body.Close()
	answer := io.LimitReader(response.Body, maxKMSAnswer)
	if response.StatusCode != http.StatusOK {
		var refusal struct {
			Type string `json:"__type"`
		}
		_ = json.NewDecoder(answer).Decode(&refusal)
		_, kind, _ := strings.Cut(refusal.Type, "#")
		return &KMSError{Action: action, Status: response.StatusCode, Type: cmp.Or(kind, refusal.Type)}
	}
	if err := json.NewDecoder(answer).Decode(out); err != nil {
		return fmt.Errorf("scm: read KMS %s: %w", action, err)
	}
	return nil
}

// authorization is the Authorization header of a POST with no query.
func (k *KMS) authorization(path string, headers map[string]string, body []byte, at time.Time) string {
	signed, signature := signV4(k.secretKey, k.region, kmsService, http.MethodPost, cmp.Or(path, "/"), headers, body, at)
	return sigV4Algorithm + " Credential=" + k.accessKeyID + "/" + credentialScope(at, k.region, kmsService) +
		", SignedHeaders=" + signed + ", Signature=" + signature
}

// credentialScope is DAY/REGION/SERVICE/aws4_request.
func credentialScope(at time.Time, region, service string) string {
	return strings.Join([]string{at.Format(amzDay), region, service, sigV4Scope}, "/")
}

// signV4 returns the signed header names and the Signature Version 4
// signature of a request with no query string. headers are every header
// signed, by lowercase name, host included.
func signV4(secret, region, service, method, path string, headers map[string]string, body []byte, at time.Time) (string, string) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)
	var canonical strings.Builder
	canonical.WriteString(method + "\n" + path + "\n\n")
	for _, name := range names {
		canonical.WriteString(name + ":" + strings.Join(strings.Fields(headers[name]), " ") + "\n")
	}
	signed := strings.Join(names, ";")
	payload := sha256.Sum256(body)
	canonical.WriteString("\n" + signed + "\n" + hex.EncodeToString(payload[:]))
	request := sha256.Sum256([]byte(canonical.String()))
	toSign := strings.Join([]string{sigV4Algorithm, at.Format(amzDate), credentialScope(at, region, service), hex.EncodeToString(request[:])}, "\n")
	key := []byte("AWS4" + secret)
	for _, part := range []string{at.Format(amzDay), region, service, sigV4Scope} {
		key = hmacSHA256(key, part)
	}
	return signed, hex.EncodeToString(hmacSHA256(key, toSign))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// Signer returns the Signer for one asymmetric KMS key, whose key usage is
// SIGN_VERIFY. The private key stays in KMS.
func (k *KMS) Signer(keyID string) Signer {
	return kmsSigner{kms: k, keyID: keyID}
}

type kmsSigner struct {
	kms   *KMS
	keyID string
}

// Sign asks KMS to sign the digest with RSASSA-PKCS1-v1_5 and SHA-256. The
// message type DIGEST keeps KMS from hashing it again.
func (s kmsSigner) Sign(ctx context.Context, digest [sha256.Size]byte) ([]byte, error) {
	request := struct {
		KeyID            string `json:"KeyId"`
		Message          []byte `json:"Message"`
		MessageType      string `json:"MessageType"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}{s.keyID, digest[:], messageDigest, signingAlgorithm}
	var answer struct {
		Signature        []byte `json:"Signature"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	if err := s.kms.call(ctx, "Sign", request, &answer); err != nil {
		return nil, err
	}
	if answer.SigningAlgorithm != signingAlgorithm || len(answer.Signature) == 0 {
		return nil, fmt.Errorf("scm: KMS Sign answered no %s signature", signingAlgorithm)
	}
	return answer.Signature, nil
}

// KeyImport is one import into a KMS key created with origin EXTERNAL: its
// wrapping public key and import token, valid for 24 hours.
type KeyImport struct {
	kms      *KMS
	keyID    string
	wrapping *rsa.PublicKey
	token    []byte
}

// PrepareImport asks KMS for the wrapping key and import token of keyID, for
// the RSA_AES_KEY_WRAP_SHA_256 algorithm, the one KMS takes for an RSA
// private key.
func (k *KMS) PrepareImport(ctx context.Context, keyID string) (*KeyImport, error) {
	request := struct {
		KeyID             string `json:"KeyId"`
		WrappingAlgorithm string `json:"WrappingAlgorithm"`
		WrappingKeySpec   string `json:"WrappingKeySpec"`
	}{keyID, wrappingAlgorithm, wrappingKeySpec}
	var answer struct {
		ImportToken []byte `json:"ImportToken"`
		PublicKey   []byte `json:"PublicKey"`
	}
	if err := k.call(ctx, "GetParametersForImport", request, &answer); err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(answer.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("scm: KMS's wrapping key: %w", err)
	}
	wrapping, ok := parsed.(*rsa.PublicKey)
	if !ok || wrapping.N.BitLen() < minWrappingBits || len(answer.ImportToken) == 0 {
		return nil, errors.New("scm: KMS answered no RSA wrapping key and import token")
	}
	return &KeyImport{kms: k, keyID: keyID, wrapping: wrapping, token: answer.ImportToken}, nil
}

// Import wraps an RSA private key, PKCS #8 in DER, and imports it with no
// expiry. The AES key it draws is zeroed before it returns. The caller owns
// material and zeroes it.
func (i *KeyImport) Import(ctx context.Context, material []byte) error {
	wrapped, err := wrapKeyMaterial(i.wrapping, material)
	if err != nil {
		return err
	}
	request := struct {
		KeyID                string `json:"KeyId"`
		ImportToken          []byte `json:"ImportToken"`
		EncryptedKeyMaterial []byte `json:"EncryptedKeyMaterial"`
		ExpirationModel      string `json:"ExpirationModel"`
	}{i.keyID, i.token, wrapped, noExpiry}
	var answer struct {
		KeyID string `json:"KeyId"`
	}
	return i.kms.call(ctx, "ImportKeyMaterial", request, &answer)
}

// wrapKeyMaterial is RSA_AES_KEY_WRAP_SHA_256: a fresh 256-bit AES key wraps
// the material with AES key wrap with padding, RFC 5649, and RSAES-OAEP with
// SHA-256 encrypts the AES key. The encrypted AES key comes first.
func wrapKeyMaterial(wrapping *rsa.PublicKey, material []byte) ([]byte, error) {
	key := make([]byte, aesKeyBytes)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("scm: draw the AES key: %w", err)
	}
	sealedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, wrapping, key, nil)
	if err != nil {
		return nil, fmt.Errorf("scm: wrap the AES key: %w", err)
	}
	sealedMaterial, err := wrapPadded(key, material)
	if err != nil {
		return nil, err
	}
	return append(sealedKey, sealedMaterial...), nil
}

// wrapPadded is AES key wrap with padding, RFC 5649 section 4.1, under kek.
func wrapPadded(kek, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 || uint64(len(plaintext)) > 1<<32-1 {
		return nil, errors.New("scm: key wrap takes 1 to 2^32-1 bytes")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("scm: key wrap: %w", err)
	}
	blocks := (len(plaintext) + 7) / 8
	out := make([]byte, 8+blocks*8)
	copy(out[8:], plaintext)
	var integrity [8]byte
	binary.BigEndian.PutUint32(integrity[:4], 0xA65959A6)
	binary.BigEndian.PutUint32(integrity[4:], uint32(len(plaintext)))
	if blocks == 1 {
		copy(out, integrity[:])
		block.Encrypt(out, out)
		return out, nil
	}
	var buffer [16]byte
	defer clear(buffer[:])
	for step := range 6 {
		for i := 1; i <= blocks; i++ {
			copy(buffer[:8], integrity[:])
			copy(buffer[8:], out[i*8:])
			block.Encrypt(buffer[:], buffer[:])
			binary.BigEndian.PutUint64(integrity[:], binary.BigEndian.Uint64(buffer[:8])^uint64(blocks*step+i))
			copy(out[i*8:], buffer[8:])
		}
	}
	copy(out, integrity[:])
	return out, nil
}

package scm

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// jwtLifetime is the ten minutes SPEC.md D fixes.
	jwtLifetime = 10 * time.Minute
	// jwtBackdate covers a clock behind GitHub's.
	jwtBackdate = time.Minute
	// tokenMargin renews an installation token before it can lapse mid-request.
	tokenMargin = 5 * time.Minute
)

// Signer signs a SHA-256 digest with RSASSA-PKCS1-v1_5. The production signer
// is a KMS call, so the private key never reaches this process.
type Signer interface {
	Sign(ctx context.Context, digest [sha256.Size]byte) ([]byte, error)
}

// AppJWT mints the App's RS256 JSON Web Token.
func AppJWT(ctx context.Context, signer Signer, appID string, now time.Time) (string, error) {
	issued := now.Add(-jwtBackdate)
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{issued.Unix(), issued.Add(jwtLifetime).Unix(), appID})
	if err != nil {
		return "", fmt.Errorf("scm: encode claims: %w", err)
	}
	encoding := base64.RawURLEncoding
	input := encoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encoding.EncodeToString(claims)
	signature, err := signer.Sign(ctx, sha256.Sum256([]byte(input)))
	if err != nil {
		return "", fmt.Errorf("scm: sign the app token: %w", err)
	}
	return input + "." + encoding.EncodeToString(signature), nil
}

// AppTokens mints installation tokens with the App's minimum permissions and
// holds each until it nears its expiry.
type AppTokens struct {
	API          string
	AppID        string
	Installation int64
	Signer       Signer
	Now          func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Token returns a live installation token.
func (a *AppTokens) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.Now()
	if a.token != "" && now.Add(tokenMargin).Before(a.expires) {
		return a.token, nil
	}
	assertion, err := AppJWT(ctx, a.Signer, a.AppID, now)
	if err != nil {
		return "", err
	}
	request := struct {
		Permissions map[string]string `json:"permissions"`
	}{map[string]string{"checks": "write", "pull_requests": "write", "contents": "read", "metadata": "read"}}
	var answer struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(a.Installation, 10) + "/access_tokens"
	root := strings.TrimRight(cmp.Or(a.API, DefaultAPI), "/")
	if err := checkRoot(root); err != nil {
		return "", err
	}
	if err := send(ctx, newClient(), http.MethodPost, root, path, "Bearer "+assertion, request, &answer); err != nil {
		return "", err
	}
	a.token, a.expires = answer.Token, answer.ExpiresAt
	return a.token, nil
}

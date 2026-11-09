package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	envAppID  = "COSTROID_GITHUB_APP_ID"
	envAppKey = "COSTROID_GITHUB_APP_KMS_KEY"
	// appTimeout bounds one call to GitHub or to KMS.
	appTimeout = 30 * time.Second
)

var appIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

// appHosts reaches a repository as one installation of the GitHub App. Each
// installation's token is minted with a JWT the KMS key signs, and held until
// it nears its expiry. The App's private key never reaches this process.
type appHosts struct {
	api    string
	appID  string
	signer scm.Signer
	client *http.Client
	limits *scm.Limiter
	now    func() time.Time

	mu     sync.Mutex
	tokens map[int64]*scm.AppTokens
}

func newAppHosts(api, appID string, signer scm.Signer, client *http.Client, now func() time.Time) *appHosts {
	return &appHosts{
		api: api, appID: appID, signer: signer, client: client, limits: scm.NewLimiter(now), now: now,
		tokens: map[int64]*scm.AppTokens{},
	}
}

// Repository returns the adapter for owner/name as one installation.
func (h *appHosts) Repository(installation int64, repository string) (ApprovalHost, error) {
	return scm.NewGitHub(scm.GitHubOptions{
		API: h.api, Repository: repository, Tokens: h.tokensOf(installation), Client: h.client,
		Limits: h.limits, Installation: installation,
	})
}

func (h *appHosts) tokensOf(installation int64) *scm.AppTokens {
	h.mu.Lock()
	defer h.mu.Unlock()
	tokens, found := h.tokens[installation]
	if !found {
		tokens = &scm.AppTokens{API: h.api, AppID: h.appID, Installation: installation, Signer: h.signer, Now: h.now, Client: h.client}
		h.tokens[installation] = tokens
	}
	return tokens
}

// appKeyHosts reads the App's id and its KMS key from the environment, and
// answers the hosts that reach the API root api through that key. With
// neither set it answers nil: the service holds no App key, and an approve
// press posts nothing.
func appKeyHosts(getenv func(string) string, api string, gate *egress.Gate, logger *slog.Logger) (RepositoryHosts, error) {
	appID, keyID := getenv(envAppID), getenv(envAppKey)
	if appID == "" && keyID == "" {
		return nil, nil
	}
	if appID == "" || keyID == "" {
		return nil, errors.New(envAppID + " and " + envAppKey + " are set together, or neither is")
	}
	if !appIDPattern.MatchString(appID) {
		return nil, fmt.Errorf("%s is not a GitHub App's id, a number", envAppID)
	}
	client := gate.Client(appTimeout)
	kms, err := scm.KMSFromEnvironment(getenv, keyID, client, time.Now)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envAppKey, err)
	}
	root, err := url.Parse(api)
	if err != nil {
		return nil, fmt.Errorf("GitHub's API root: %w", err)
	}
	for _, host := range []string{root.Hostname(), kms.Host()} {
		if !gate.Allows(host) {
			logger.Warn("egress allowlist does not cover the GitHub App's calls", "host", host, "add", egress.Flag)
		}
	}
	return newAppHosts(api, appID, kms.Signer(keyID), client, time.Now), nil
}

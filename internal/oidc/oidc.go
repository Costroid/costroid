// Package oidc verifies CI OIDC ID tokens and reads a run's identity from them.
//
// Only RS256 is accepted, the one algorithm both issuers' discovery documents
// list. docs/serve.md cites the source and retrieval date of every claim name,
// issuer URL and key set path below.
package oidc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxTokenBytes = 16 << 10
	clockSkew     = 60 * time.Second
	algorithm     = "RS256"
)

// The refusals. A caller reports one answer to the sender and logs which.
var (
	ErrMalformed      = errors.New("oidc: malformed token")
	ErrAlgorithm      = errors.New("oidc: algorithm not accepted")
	ErrIssuer         = errors.New("oidc: unknown issuer")
	ErrKeyUnavailable = errors.New("oidc: signing key unavailable")
	ErrSignature      = errors.New("oidc: bad signature")
	ErrAudience       = errors.New("oidc: wrong audience")
	ErrExpired        = errors.New("oidc: token expired")
	ErrNotYetValid    = errors.New("oidc: token not yet valid")
	ErrClaims         = errors.New("oidc: run identity claims missing or invalid")
)

// Kind selects how an issuer's claims read.
type Kind int

// The CI systems with a claim mapping.
const (
	GitHubActions Kind = iota + 1
	GitLabCI
)

// Issuer is one trusted token issuer.
type Issuer struct {
	Kind    Kind
	URL     string
	JWKSURL string
	// Host is the SCM host its repositories live on.
	Host string
}

// GitHub is github.com's Actions issuer.
func GitHub() Issuer {
	return Issuer{
		Kind:    GitHubActions,
		URL:     "https://token.actions.githubusercontent.com",
		JWKSURL: "https://token.actions.githubusercontent.com/.well-known/jwks",
		Host:    "github.com",
	}
}

// GitLab is a GitLab instance's CI issuer, given its host name.
func GitLab(host string) Issuer {
	return Issuer{
		Kind:    GitLabCI,
		URL:     "https://" + host,
		JWKSURL: "https://" + host + "/oauth/discovery/keys",
		Host:    host,
	}
}

// Identity is a run as the token states it. Repository is lower case, host and
// path, the form of the costroid/repo tag. PullRequest is empty unless the
// token itself names one.
//
// CommitSHA is the commit the token verifies. It is the head of the ref only
// when CommitIsHead says so: on GitHub's pull_request event it is a merge
// commit, and on a GitLab merged results pipeline a temporary one.
type Identity struct {
	Host         string
	Owner        string
	Repository   string
	RunID        string
	RunAttempt   string
	CommitSHA    string
	CommitIsHead bool
	Actor        string
	Event        string
	PullRequest  string
}

// Verifier checks tokens against a set of issuers and one audience.
type Verifier struct {
	audience string
	now      func() time.Time
	issuers  map[string]*issuerKeys
}

// NewVerifier returns a Verifier. A nil client means http.DefaultClient, and a
// nil clock means time.Now.
func NewVerifier(audience string, client *http.Client, now func() time.Time, issuers ...Issuer) *Verifier {
	if client == nil {
		client = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	v := &Verifier{audience: audience, now: now, issuers: map[string]*issuerKeys{}}
	for _, issuer := range issuers {
		v.issuers[issuer.URL] = &issuerKeys{issuer: issuer, client: client, now: now, keys: map[string]*rsa.PublicKey{}}
	}
	return v
}

type header struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

// registered holds the claims every issuer shares.
type registered struct {
	Issuer    string       `json:"iss"`
	Audience  audience     `json:"aud"`
	Expires   *numericDate `json:"exp"`
	NotBefore *numericDate `json:"nbf"`
}

// numericDate is a JWT NumericDate in whole seconds. A fractional part is
// allowed by the JWT specification and cut.
type numericDate int64

// UnmarshalJSON reads the number's text, so no float is involved.
func (d *numericDate) UnmarshalJSON(data []byte) error {
	whole, _, _ := strings.Cut(string(data), ".")
	seconds, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return err
	}
	*d = numericDate(seconds)
	return nil
}

// audience reads aud as one string or a list.
type audience []string

// UnmarshalJSON accepts both forms the JWT specification allows.
func (a *audience) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// Verify checks a token and returns the run it names.
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	parts, err := split(token)
	if err != nil {
		return Identity{}, err
	}
	var head header
	if err := decodeSegment(parts[0], &head); err != nil {
		return Identity{}, err
	}
	if head.Algorithm != algorithm {
		return Identity{}, fmt.Errorf("%w: %q", ErrAlgorithm, head.Algorithm)
	}
	var claims registered
	if err := decodeSegment(parts[1], &claims); err != nil {
		return Identity{}, err
	}
	issuer, ok := v.issuers[claims.Issuer]
	if !ok {
		return Identity{}, fmt.Errorf("%w: %q", ErrIssuer, claims.Issuer)
	}
	key, err := issuer.key(ctx, head.KeyID)
	if err != nil {
		return Identity{}, err
	}
	if err := checkSignature(key, parts); err != nil {
		return Identity{}, err
	}
	if err := v.checkRegistered(claims); err != nil {
		return Identity{}, err
	}
	return readIdentity(issuer.issuer, parts[1])
}

func split(token string) ([]string, error) {
	if len(token) > maxTokenBytes {
		return nil, fmt.Errorf("%w: over %d bytes", ErrMalformed, maxTokenBytes)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: %d segments", ErrMalformed, len(parts))
	}
	return parts, nil
}

func decodeSegment(segment string, target any) error {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return nil
}

func checkSignature(key *rsa.PublicKey, parts []string) error {
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return fmt.Errorf("%w: %w", ErrSignature, err)
	}
	return nil
}

func (v *Verifier) checkRegistered(claims registered) error {
	found := false
	for _, value := range claims.Audience {
		found = found || value == v.audience
	}
	if !found {
		return ErrAudience
	}
	now := v.now()
	if claims.Expires == nil {
		return fmt.Errorf("%w: no exp", ErrExpired)
	}
	if !now.Add(-clockSkew).Before(time.Unix(int64(*claims.Expires), 0)) {
		return ErrExpired
	}
	if claims.NotBefore != nil && now.Add(clockSkew).Before(time.Unix(int64(*claims.NotBefore), 0)) {
		return ErrNotYetValid
	}
	return nil
}

// Value patterns for what the claims must look like. They match the tag
// convention, version 0.1, so a claim is always a legal tag value.
var (
	repoPath   = regexp.MustCompile(`^[A-Za-z0-9._~%-]+(/[A-Za-z0-9._~%-]+){1,7}$`)
	hostName   = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	commitSHA  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	runID      = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	githubPull = regexp.MustCompile(`^refs/pull/([1-9][0-9]{0,19})/merge$`)
)

// eventPush is both systems' name for a run a push started, the one event
// whose token states the head of its ref.
const eventPush = "push"

type githubClaims struct {
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	SHA        string `json:"sha"`
	Actor      string `json:"actor"`
	EventName  string `json:"event_name"`
	Ref        string `json:"ref"`
}

type gitlabClaims struct {
	ProjectPath    string `json:"project_path"`
	PipelineID     string `json:"pipeline_id"`
	SHA            string `json:"sha"`
	UserLogin      string `json:"user_login"`
	PipelineSource string `json:"pipeline_source"`
}

func readIdentity(issuer Issuer, payload string) (Identity, error) {
	identity := Identity{Host: strings.ToLower(issuer.Host)}
	var path string
	switch issuer.Kind {
	case GitHubActions:
		var c githubClaims
		if err := decodeSegment(payload, &c); err != nil {
			return Identity{}, err
		}
		path = c.Repository
		identity.RunID, identity.RunAttempt = c.RunID, c.RunAttempt
		identity.CommitSHA, identity.Actor, identity.Event = c.SHA, c.Actor, c.EventName
		if match := githubPull.FindStringSubmatch(c.Ref); match != nil {
			identity.PullRequest = match[1]
		}
	case GitLabCI:
		var c gitlabClaims
		if err := decodeSegment(payload, &c); err != nil {
			return Identity{}, err
		}
		path = c.ProjectPath
		identity.RunID = c.PipelineID
		identity.CommitSHA, identity.Actor, identity.Event = c.SHA, c.UserLogin, c.PipelineSource
	default:
		return Identity{}, fmt.Errorf("%w: issuer kind %d", ErrIssuer, issuer.Kind)
	}
	path = strings.ToLower(path)
	if !repoPath.MatchString(path) || !hostName.MatchString(identity.Host) {
		return Identity{}, fmt.Errorf("%w: repository", ErrClaims)
	}
	identity.Repository = identity.Host + "/" + path
	identity.CommitIsHead = identity.Event == eventPush
	identity.Owner, _, _ = strings.Cut(path, "/")
	if !runID.MatchString(identity.RunID) {
		return Identity{}, fmt.Errorf("%w: run id", ErrClaims)
	}
	if !commitSHA.MatchString(identity.CommitSHA) {
		return Identity{}, fmt.Errorf("%w: commit", ErrClaims)
	}
	if identity.RunAttempt != "" {
		if _, err := strconv.ParseUint(identity.RunAttempt, 10, 32); err != nil {
			return Identity{}, fmt.Errorf("%w: run attempt", ErrClaims)
		}
	}
	return identity, nil
}

package oidc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/oidc/oidctest"
)

const (
	testAudience = "https://costroid.test"
	testSHA      = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
)

var testNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type stubIssuer struct {
	*oidctest.Issuer
}

func newStubIssuer(t *testing.T) *stubIssuer {
	t.Helper()
	return &stubIssuer{oidctest.New(t)}
}

func (s *stubIssuer) issuer(kind Kind) Issuer {
	return Issuer{Kind: kind, URL: s.URL(), JWKSURL: s.URL() + "/keys", Host: "github.com"}
}

func (s *stubIssuer) verifier(kind Kind) *Verifier {
	return NewVerifier(testAudience, s.Server.Client(), func() time.Time { return testNow }, s.issuer(kind))
}

func (s *stubIssuer) claims() map[string]any {
	return map[string]any{
		"iss":              s.URL(),
		"aud":              testAudience,
		"exp":              testNow.Add(5 * time.Minute).Unix(),
		"nbf":              testNow.Add(-time.Minute).Unix(),
		"repository":       "Acme/Widgets",
		"repository_owner": "Acme",
		"run_id":           "16873409921",
		"run_attempt":      "2",
		"sha":              testSHA,
		"actor":            "octo-dev",
		"event_name":       "pull_request",
		"ref":              "refs/pull/1423/merge",
	}
}

func TestVerifyReadsAGitHubRunFromTheToken(t *testing.T) {
	s := newStubIssuer(t)
	got, err := s.verifier(GitHubActions).Verify(context.Background(), s.Token(s.claims()))
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{
		Host: "github.com", Owner: "acme", Repository: "github.com/acme/widgets", RunID: "16873409921",
		RunAttempt: "2", CommitSHA: testSHA, Actor: "octo-dev", Event: "pull_request", PullRequest: "1423",
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestVerifyReadsAGitLabRunFromTheToken(t *testing.T) {
	s := newStubIssuer(t)
	claims := s.claims()
	for _, key := range []string{"repository", "run_id", "run_attempt", "actor", "event_name", "ref"} {
		delete(claims, key)
	}
	claims["project_path"] = "acme/platform/widgets"
	claims["pipeline_id"] = "991"
	claims["user_login"] = "gl-dev"
	claims["pipeline_source"] = "merge_request_event"
	claims["ref_path"] = "refs/merge-requests/77/head"
	got, err := s.verifier(GitLabCI).Verify(context.Background(), s.Token(claims))
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{
		Host: "github.com", Owner: "acme", Repository: "github.com/acme/platform/widgets", RunID: "991",
		CommitSHA: testSHA, Actor: "gl-dev", Event: "merge_request_event", PullRequest: "77",
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAPushTokenNamesNoPullRequest(t *testing.T) {
	s := newStubIssuer(t)
	claims := s.claims()
	claims["ref"] = "refs/heads/main"
	got, err := s.verifier(GitHubActions).Verify(context.Background(), s.Token(claims))
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequest != "" {
		t.Fatalf("pull request %q from a branch ref", got.PullRequest)
	}
}

func TestVerifyRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{"wrong audience", func(c map[string]any) { c["aud"] = "https://elsewhere.test" }, ErrAudience},
		{"wrong audience in a list", func(c map[string]any) { c["aud"] = []string{"a", "b"} }, ErrAudience},
		{"no audience", func(c map[string]any) { delete(c, "aud") }, ErrAudience},
		{"expired", func(c map[string]any) { c["exp"] = testNow.Add(-2 * time.Minute).Unix() }, ErrExpired},
		{"no expiry", func(c map[string]any) { delete(c, "exp") }, ErrExpired},
		{"not yet valid", func(c map[string]any) { c["nbf"] = testNow.Add(2 * time.Minute).Unix() }, ErrNotYetValid},
		{"unknown issuer", func(c map[string]any) { c["iss"] = "https://evil.test" }, ErrIssuer},
		{"no repository", func(c map[string]any) { delete(c, "repository") }, ErrClaims},
		{"repository without an owner", func(c map[string]any) { c["repository"] = "widgets" }, ErrClaims},
		{"no run", func(c map[string]any) { delete(c, "run_id") }, ErrClaims},
		{"short commit", func(c map[string]any) { c["sha"] = "abc123" }, ErrClaims},
		{"non-numeric attempt", func(c map[string]any) { c["run_attempt"] = "first" }, ErrClaims},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubIssuer(t)
			claims := s.claims()
			tc.mutate(claims)
			_, err := s.verifier(GitHubActions).Verify(context.Background(), s.Token(claims))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyAcceptsAnAudienceInAList(t *testing.T) {
	s := newStubIssuer(t)
	claims := s.claims()
	claims["aud"] = []string{"other", testAudience}
	if _, err := s.verifier(GitHubActions).Verify(context.Background(), s.Token(claims)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRefusesASignatureFromAnotherKey(t *testing.T) {
	s := newStubIssuer(t)
	token := oidctest.Sign(t, oidctest.NewKey(t), map[string]any{"alg": "RS256", "kid": oidctest.KeyID}, s.claims())
	if _, err := s.verifier(GitHubActions).Verify(context.Background(), token); !errors.Is(err, ErrSignature) {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyRefusesTamperedClaims(t *testing.T) {
	s := newStubIssuer(t)
	claims := s.claims()
	token := s.Token(claims)
	claims["repository"] = "acme/other"
	forged := s.Token(claims)
	spliced := splice(token, forged)
	if _, err := s.verifier(GitHubActions).Verify(context.Background(), spliced); !errors.Is(err, ErrSignature) {
		t.Fatalf("got %v", err)
	}
}

// splice puts one token's payload under another token's signature.
func splice(signed, payload string) string {
	a, _ := split(signed)
	b, _ := split(payload)
	return a[0] + "." + b[1] + "." + a[2]
}

func TestVerifyRefusesOtherAlgorithms(t *testing.T) {
	s := newStubIssuer(t)
	for _, alg := range []string{"none", "HS256", "RS512", "ES256", ""} {
		t.Run(alg, func(t *testing.T) {
			token := oidctest.Sign(t, s.Key, map[string]any{"alg": alg, "kid": oidctest.KeyID}, s.claims())
			if _, err := s.verifier(GitHubActions).Verify(context.Background(), token); !errors.Is(err, ErrAlgorithm) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestVerifyRefusesAnUnknownKeyID(t *testing.T) {
	s := newStubIssuer(t)
	token := oidctest.Sign(t, s.Key, map[string]any{"alg": "RS256", "kid": "forged"}, s.claims())
	if _, err := s.verifier(GitHubActions).Verify(context.Background(), token); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownKeyIDsFetchTheKeySetOncePerInterval(t *testing.T) {
	s := newStubIssuer(t)
	v := s.verifier(GitHubActions)
	for i := range 5 {
		token := oidctest.Sign(t, s.Key, map[string]any{"alg": "RS256", "kid": fmt.Sprintf("forged-%d", i)}, s.claims())
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, ErrKeyUnavailable) {
			t.Fatalf("got %v", err)
		}
	}
	if got := s.Hits(); got != 1 {
		t.Fatalf("key set fetched %d times", got)
	}
}

func TestAKnownKeySurvivesAnIssuerOutage(t *testing.T) {
	s := newStubIssuer(t)
	clock := testNow
	v := NewVerifier(testAudience, s.Server.Client(), func() time.Time { return clock }, s.issuer(GitHubActions))
	if _, err := v.Verify(context.Background(), s.Token(s.claims())); err != nil {
		t.Fatal(err)
	}
	s.Server.Close()
	clock = clock.Add(keySetLifetime + time.Minute)
	claims := s.claims()
	claims["exp"] = clock.Add(time.Minute).Unix()
	if _, err := v.Verify(context.Background(), s.Token(claims)); err != nil {
		t.Fatalf("a cached key should still verify: %v", err)
	}
}

func TestVerifyRefusesMalformedTokens(t *testing.T) {
	s := newStubIssuer(t)
	v := s.verifier(GitHubActions)
	for _, token := range []string{"", "a", "a.b", "a.b.c", "a.b.c.d", "!!.??.%%"} {
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: got %v", token, err)
		}
	}
}

func TestAWeakKeyInTheKeySetIsRefused(t *testing.T) {
	if _, err := rsaKey(base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xff}), "AQAB"); err == nil {
		t.Fatal("a 16-bit modulus was accepted")
	}
}

func FuzzVerify(f *testing.F) {
	f.Add("eyJhbGciOiJSUzI1NiJ9.e30.c2ln")
	f.Add("a.b.c")
	f.Add(`e30.e30.e30`)
	v := NewVerifier(testAudience, nil, func() time.Time { return testNow })
	f.Fuzz(func(t *testing.T, token string) {
		if _, err := v.Verify(context.Background(), token); err == nil {
			t.Fatalf("a verifier with no issuers accepted %q", token)
		}
	})
}

func FuzzReadIdentity(f *testing.F) {
	f.Add(`{"repository":"a/b","run_id":"1","sha":"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d","ref":"refs/pull/1/merge"}`)
	f.Add(`{"project_path":"a/b/c","pipeline_id":"1","ref_path":"refs/merge-requests/3/head"}`)
	f.Fuzz(func(t *testing.T, payload string) {
		segment := base64.RawURLEncoding.EncodeToString([]byte(payload))
		for _, issuer := range []Issuer{GitHub(), GitLab("gitlab.com")} {
			identity, err := readIdentity(issuer, segment)
			if err != nil {
				continue
			}
			if !commitSHA.MatchString(identity.CommitSHA) || !runID.MatchString(identity.RunID) {
				t.Fatalf("an invalid identity passed: %+v", identity)
			}
		}
	})
}

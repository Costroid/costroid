package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testOwner       = "7204411"
	testNamespace   = "4410287"
	testGitLabProof = "eyJhbGciOiJSUzI1NiJ9.eyJuYW1lc3BhY2VfaWQiOiI0NDEwMjg3In0.Z2l0bGFiLXNpZ25hdHVyZQ"
	testAudience    = "https://app.costroid.com/tenants/" + testTenant
)

// connectNow is the clock connect reads in these tests.
var connectNow = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// connectStub answers as Costroid Platform and as GitHub's OIDC token
// endpoint, and keeps what each was sent.
type connectStub struct {
	server *httptest.Server
	// installations is the identity answer's list, as JSON.
	installations string
	// status and body are the answer to a proof. A 200 without a body echoes
	// an active binding of the path's installation.
	status int
	body   string

	mu        sync.Mutex
	identity  int
	audiences []string
	proofs    []sentProof
	urls      []string
}

type sentProof struct {
	path, authorization, contentType string
	body                             []byte
}

func newConnectStub(t *testing.T) *connectStub {
	t.Helper()
	stub := &connectStub{installations: "[]", status: http.StatusOK}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *connectStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.urls = append(s.urls, r.URL.String())
	installationsPath := integrationPath + "/installations/"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/oidc":
		if r.Header.Get("Authorization") != "Bearer "+testRequestToken {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		s.audiences = append(s.audiences, r.URL.Query().Get("audience"))
		w.Write([]byte(`{"value":"` + testRunToken + `"}`))
	case r.Method == http.MethodGet && r.URL.Path == integrationPath+"/identity":
		s.identity++
		if r.Header.Get("Authorization") != "Bearer "+testIntegrationToken {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"AUTH_TOKEN_INVALID","message":"no"}}`))
			return
		}
		w.Write([]byte(`{"contract":{"version":"0.2","tag_set":"tags/v0.2","focus_row":"focus-row/v0.3"},` +
			`"tenant":{"id":"` + testTenant + `","name":"Example Inc","region":"eu"},` +
			`"account":{"id":"acct_01JEXAMPLE0001","role":"admin"},` +
			`"token":{"id":"tok_01JEXAMPLE0001","scopes":["identity:read","installations:write","rows:write"],"created_at":"2026-10-01T08:00:00Z","expires_at":"2027-01-01T08:00:00Z","last_used_at":null},` +
			`"installations":` + s.installations + `}`))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, installationsPath):
		body, _ := io.ReadAll(r.Body)
		installation := strings.TrimPrefix(r.URL.Path, installationsPath)
		s.proofs = append(s.proofs, sentProof{path: installation, authorization: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: body})
		w.WriteHeader(s.status)
		if s.status == http.StatusOK && s.body == "" {
			owner := installation[strings.Index(installation, ":")+1:]
			w.Write([]byte(`{"id":"` + installation + `","tenant_id":"` + testTenant + `","owner_id":"` + owner + `","status":"active","proof_kind":"ci_oidc","bound_at":"2026-11-08T08:59:58Z","proven_at":"2026-11-08T08:59:30Z","ended_at":null}`))
			return
		}
		w.Write([]byte(s.body))
	default:
		http.Error(w, "", http.StatusNotFound)
	}
}

// githubEnv is a scheduled GitHub Actions job with id-token: write, under the
// owner 7204411.
func (s *connectStub) githubEnv() map[string]string {
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_EVENT_NAME":              "schedule",
		"GITHUB_REPOSITORY_OWNER_ID":     testOwner,
		"ACTIONS_ID_TOKEN_REQUEST_URL":   s.server.URL + "/oidc?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": testRequestToken,
		platformTokenVariable:            testIntegrationToken,
	}
}

// gitlabEnv is a GitLab.com job in namespace 4410287 whose id_tokens set the
// proof.
func gitlabEnv() map[string]string {
	return map[string]string{
		"GITLAB_CI":               "true",
		"CI_PROJECT_NAMESPACE_ID": testNamespace,
		proofVariable:             testGitLabProof,
		platformTokenVariable:     testIntegrationToken,
	}
}

func (s *connectStub) connect(t *testing.T, env map[string]string, extra ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--platform-url", s.server.URL}, extra...)
	code := connect(context.Background(), args, s.server.Client(), func(name string) string { return env[name] }, func() time.Time { return connectNow }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// activeInstallation is an identity list holding one active binding proven at
// the time given.
func activeInstallation(id string, provenAt time.Time) string {
	return `[{"id":"` + id + `","tenant_id":"` + testTenant + `","owner_id":"` + id[strings.Index(id, ":")+1:] +
		`","status":"active","proof_kind":"ci_oidc","bound_at":"2026-10-01T08:02:11Z","proven_at":"` + provenAt.Format(time.RFC3339) + `","ended_at":null}]`
}

func TestConnectSendsOneProof(t *testing.T) {
	cases := []struct {
		name, installations, installation, proof string
		gitlab                                   bool
		audiences                                []string
	}{
		{name: "a GitHub owner with no binding", installations: "[]", installation: "github-owner:" + testOwner, proof: testRunToken, audiences: []string{testAudience}},
		{name: "a GitHub binding proven 20 hours ago", installations: activeInstallation("github-owner:"+testOwner, connectNow.Add(-20*time.Hour)), installation: "github-owner:" + testOwner, proof: testRunToken, audiences: []string{testAudience}},
		{name: "a displaced GitHub binding", installations: `[{"id":"github-owner:` + testOwner + `","tenant_id":"` + testTenant + `","owner_id":"` + testOwner + `","status":"displaced","proof_kind":"ci_oidc","bound_at":"2026-10-01T08:02:11Z","proven_at":"2026-11-08T08:00:00Z","ended_at":"2026-11-08T08:30:00Z"}]`, installation: "github-owner:" + testOwner, proof: testRunToken, audiences: []string{testAudience}},
		{name: "a GitLab namespace", installations: "[]", installation: "gitlab-namespace:" + testNamespace, proof: testGitLabProof, gitlab: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newConnectStub(t)
			stub.installations = tc.installations
			env := stub.githubEnv()
			if tc.gitlab {
				env = gitlabEnv()
			}
			code, stdout, stderr := stub.connect(t, env)
			if code != 0 || len(stub.proofs) != 1 {
				t.Fatalf("exit %d, %d proofs: %s", code, len(stub.proofs), stderr)
			}
			sent := stub.proofs[0]
			if sent.path != tc.installation {
				t.Errorf("the proof went to /installations/%s, want %s", sent.path, tc.installation)
			}
			if sent.authorization != "Bearer "+testIntegrationToken || sent.contentType != "application/json" {
				t.Errorf("Authorization %q, Content-Type %q", sent.authorization, sent.contentType)
			}
			if want := `{"proof":"` + tc.proof + `"}`; string(sent.body) != want {
				t.Errorf("body %s, want %s", sent.body, want)
			}
			if strings.Join(stub.audiences, ",") != strings.Join(tc.audiences, ",") {
				t.Errorf("the OIDC token was asked for audiences %q, want %q", stub.audiences, tc.audiences)
			}
			want := "costroid: " + tc.installation + " is bound to tenant " + testTenant + ", proven at 2026-11-08T08:59:30Z\n"
			if stdout != want || stderr != "" {
				t.Errorf("stdout %q, stderr %q, want stdout %q", stdout, stderr, want)
			}
		})
	}
}

func TestConnectSkipsWhenBound(t *testing.T) {
	provenAt := connectNow.Add(-20*time.Hour + time.Second)
	noIDToken := func(*connectStub) map[string]string {
		return map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY_OWNER_ID": testOwner, platformTokenVariable: testIntegrationToken}
	}
	gitlab := func(*connectStub) map[string]string { return gitlabEnv() }
	outsideCI := func(*connectStub) map[string]string {
		return map[string]string{platformTokenVariable: testIntegrationToken}
	}
	cases := []struct {
		name         string
		env          func(*connectStub) map[string]string
		installation string
		args         []string
	}{
		{name: "a GitHub job", env: (*connectStub).githubEnv, installation: "github-owner:" + testOwner},
		{name: "a GitHub job with no id-token permission", env: noIDToken, installation: "github-owner:" + testOwner},
		{name: "a GitLab job", env: gitlab, installation: "gitlab-namespace:" + testNamespace},
		{name: "outside CI", env: outsideCI, installation: "github-owner:" + testOwner, args: []string{"--installation-id", "github-owner:" + testOwner}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newConnectStub(t)
			stub.installations = activeInstallation(tc.installation, provenAt)
			code, stdout, stderr := stub.connect(t, tc.env(stub), tc.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if len(stub.proofs) != 0 || len(stub.audiences) != 0 || stub.identity != 1 {
				t.Errorf("%d proofs, %d OIDC requests and %d identity requests, want 0, 0 and 1", len(stub.proofs), len(stub.audiences), stub.identity)
			}
			want := "costroid: " + tc.installation + " is bound to tenant " + testTenant + ", proven at " + provenAt.Format(time.RFC3339) + ", so no proof was sent\n"
			if stdout != want {
				t.Errorf("stdout %q, want %q", stdout, want)
			}
		})
	}
}

// refuseProof is the platform's refusal of a proof, with a message that the
// command must not print.
func refuseProof(t *testing.T, status int, code string) *connectStub {
	t.Helper()
	stub := newConnectStub(t)
	stub.status = status
	stub.body = `{"error":{"code":"` + code + `","message":"the platform's own words"}}`
	return stub
}

func TestConnectNamesUnprotectedRun(t *testing.T) {
	for name, env := range map[string]func(*connectStub) map[string]string{
		"GitHub": (*connectStub).githubEnv,
		"GitLab": func(*connectStub) map[string]string { return gitlabEnv() },
	} {
		t.Run(name, func(t *testing.T) {
			stub := refuseProof(t, http.StatusForbidden, "INSTALLATION_PROOF_UNPROTECTED_RUN")
			code, stdout, stderr := stub.connect(t, env(stub))
			installation := "github-owner:" + testOwner
			if name == "GitLab" {
				installation = "gitlab-namespace:" + testNamespace
			}
			want := "costroid: Costroid Platform refused the proof for " + installation + ": 403 INSTALLATION_PROOF_UNPROTECTED_RUN. " +
				"Run costroid connect from a scheduled job, or protect the default branch and run it on a push there.\n"
			if code != exitRejected || stdout != "" || stderr != want {
				t.Errorf("exit %d, stdout %q, stderr %q, want stderr %q", code, stdout, stderr, want)
			}
		})
	}
}

func TestConnectNamesBoundElsewhere(t *testing.T) {
	stub := refuseProof(t, http.StatusConflict, "INSTALLATION_BOUND_ELSEWHERE")
	code, stdout, stderr := stub.connect(t, stub.githubEnv())
	want := "costroid: Costroid Platform refused the proof for github-owner:" + testOwner + ": 409 INSTALLATION_BOUND_ELSEWHERE. " +
		"Another tenant holds this owner, and this proof does not outrank its binding, as docs/connect.md explains.\n"
	if code != exitRejected || stdout != "" || stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q, want stderr %q", code, stdout, stderr, want)
	}
}

// TestConnectNamesEveryRefusal gives each code of the contract its own
// sentence, so no refusal falls through to the generic line.
func TestConnectNamesEveryRefusal(t *testing.T) {
	generic := refusalSentence("", binding{})
	seen := map[string]string{}
	for _, code := range []string{
		"INSTALLATION_PROOF_MISSING", "INSTALLATION_PROOF_ISSUER_UNTRUSTED", "INSTALLATION_PROOF_SIGNATURE_INVALID",
		"INSTALLATION_PROOF_AUDIENCE_MISMATCH", "INSTALLATION_PROOF_EXPIRED", "INSTALLATION_PROOF_CLAIMS_INVALID",
		"INSTALLATION_PROOF_UNPROTECTED_RUN", "INSTALLATION_PROOF_SUBJECT_MISMATCH", "INSTALLATION_BOUND_ELSEWHERE",
		"INSTALLATION_PROOF_KEYS_UNAVAILABLE", "AUTH_TOKEN_INVALID", "AUTH_TOKEN_SCOPE",
	} {
		sentence := refusalSentence(code, binding{installation: "github-owner:" + testOwner, tenant: testTenant})
		if sentence == generic {
			t.Errorf("%s gets the generic sentence", code)
		}
		if strings.Contains(sentence, ". ") || strings.Contains(sentence, ": ") || !strings.HasSuffix(sentence, ".") {
			t.Errorf("%s: %q is not one sentence", code, sentence)
		}
		seen[code] = sentence
	}
	if want := "set the ID token's aud to " + testAudience + "."; !strings.HasSuffix(seen["INSTALLATION_PROOF_AUDIENCE_MISMATCH"], want) {
		t.Errorf("the audience refusal says %q, want it to end %q", seen["INSTALLATION_PROOF_AUDIENCE_MISMATCH"], want)
	}
}

func TestConnectRefusesAnInstallationTheJobCannotProve(t *testing.T) {
	stub := newConnectStub(t)
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"another owner on GitHub", stub.githubEnv(), []string{"--installation-id", "github-owner:1"}, "costroid: --installation-id is github-owner:1, and this job runs under github-owner:" + testOwner + ": a job proves only its own owner\n"},
		{"an App installation", stub.githubEnv(), []string{"--installation-id", "github:58120394"}, "costroid: --installation-id must be github-owner:<id> or gitlab-namespace:<id>, the ids a CI job proves\n"},
		{"no id outside CI", map[string]string{platformTokenVariable: testIntegrationToken}, nil, "costroid: outside GitHub Actions and GitLab CI, connect needs --installation-id, and only reads whether it is bound\n"},
		{"a name for the owner", map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY_OWNER_ID": "example"}, nil, "costroid: GITHUB_REPOSITORY_OWNER_ID is \"example\", not an owner's number\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := stub.connect(t, tc.env, tc.args...)
			if code != exitFailure || stdout != "" || stderr != tc.want {
				t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, tc.want)
			}
		})
	}
	if stub.identity != 0 || len(stub.proofs) != 0 {
		t.Errorf("%d identity requests and %d proofs, want none", stub.identity, len(stub.proofs))
	}
}

func TestConnectOutsideCINamesTheJobThatCanProve(t *testing.T) {
	stub := newConnectStub(t)
	code, stdout, stderr := stub.connect(t, map[string]string{platformTokenVariable: testIntegrationToken}, "--installation-id", "github-owner:"+testOwner)
	want := "costroid: github-owner:" + testOwner + " has no binding to tenant " + testTenant + " proven in the last 20 hours, and outside GitHub Actions and GitLab CI no proof exists: run connect in a scheduled job\n"
	if code != exitFailure || stdout != "" || stderr != want || len(stub.proofs) != 0 {
		t.Errorf("exit %d, %d proofs, stdout %q, stderr %q", code, len(stub.proofs), stdout, stderr)
	}
}

func TestConnectGitLabNamesTheAudienceItNeeds(t *testing.T) {
	stub := newConnectStub(t)
	env := gitlabEnv()
	delete(env, proofVariable)
	code, _, stderr := stub.connect(t, env)
	want := "costroid: COSTROID_PLATFORM_PROOF is empty, so this job has no proof: give the job an ID token of that name with aud " + testAudience + "\n"
	if code != exitFailure || stderr != want || len(stub.proofs) != 0 {
		t.Errorf("exit %d, %d proofs, stderr %q, want %q", code, len(stub.proofs), stderr, want)
	}
}

// secretsIn are the values no line of connect may hold: the integration
// token, GitHub's request token, and both proofs.
var secretsIn = []string{testIntegrationToken, testRequestToken, testRunToken, testGitLabProof, "the platform's own words"}

func TestConnectNeverPrintsTheToken(t *testing.T) {
	type scenario struct {
		name   string
		gitlab bool
		setup  func(*connectStub, map[string]string)
	}
	scenarios := []scenario{
		{name: "bound by this proof"},
		{name: "bound already", setup: func(s *connectStub, _ map[string]string) {
			s.installations = activeInstallation("github-owner:"+testOwner, connectNow.Add(-time.Hour))
		}},
		{name: "a GitLab binding", gitlab: true},
		{name: "a GitLab proof that is not an ID token", gitlab: true, setup: func(_ *connectStub, env map[string]string) {
			env[proofVariable] = strings.ReplaceAll(testGitLabProof, ".", "")
		}},
		{name: "an answer that echoes the proof", setup: func(s *connectStub, _ map[string]string) {
			s.body = `{"id":"` + testRunToken + `","proof":"` + testRunToken + `"}`
		}},
		{name: "a refusal that echoes the proof", setup: func(s *connectStub, _ map[string]string) {
			s.status, s.body = http.StatusForbidden, `{"error":{"code":"`+testRunToken+`","message":"`+testRunToken+`"}}`
		}},
		{name: "an unknown integration token", setup: func(_ *connectStub, env map[string]string) {
			env[platformTokenVariable] = "costroid_it_" + strings.Repeat("Z", 43)
		}},
		{name: "a token of another kind", setup: func(_ *connectStub, env map[string]string) {
			env[platformTokenVariable] = "sk-ant-" + testIntegrationToken
		}},
		{name: "GitHub refusing the OIDC request", setup: func(_ *connectStub, env map[string]string) {
			env["ACTIONS_ID_TOKEN_REQUEST_TOKEN"] = testRequestToken + "-wrong"
		}},
	}
	for _, code := range []string{"INSTALLATION_PROOF_UNPROTECTED_RUN", "INSTALLATION_BOUND_ELSEWHERE", "INSTALLATION_PROOF_AUDIENCE_MISMATCH", "INSTALLATION_PROOF_KEYS_UNAVAILABLE", "QUOTA_SOMETHING_NEW"} {
		scenarios = append(scenarios, scenario{name: code, setup: func(s *connectStub, _ map[string]string) {
			s.status, s.body = http.StatusForbidden, `{"error":{"code":"`+code+`","message":"the platform's own words"}}`
		}})
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			stub := newConnectStub(t)
			env := stub.githubEnv()
			if tc.gitlab {
				env = gitlabEnv()
			}
			if tc.setup != nil {
				tc.setup(stub, env)
			}
			_, stdout, stderr := stub.connect(t, env)
			if stdout+stderr == "" {
				t.Fatal("connect printed nothing")
			}
			for _, secret := range append(secretsIn, env[platformTokenVariable], env[proofVariable]) {
				if secret != "" && strings.Contains(stdout+stderr, secret) {
					t.Errorf("the output holds %q:\n%s%s", secret, stdout, stderr)
				}
			}
			for _, address := range stub.urls {
				for _, secret := range []string{testIntegrationToken, testRunToken, testGitLabProof} {
					if strings.Contains(address, secret) {
						t.Errorf("a request's URL holds a token: %s", address)
					}
				}
			}
		})
	}
}

// TestConnectDocsMatchTheCommand holds docs/connect.md's commands to the
// flags connect parses, and its GitLab job to the variable and the audience
// connect reads.
func TestConnectDocsMatchTheCommand(t *testing.T) {
	page := readFile(t, "../../docs/connect.md")
	commands := regexp.MustCompile(`(?m)^[\s-]*costroid connect( [^\n]*)?$`).FindAllStringSubmatch(page, -1)
	if len(commands) == 0 {
		t.Fatal("docs/connect.md runs no costroid connect")
	}
	for _, command := range commands {
		if _, err := parseConnect(strings.Fields(command[1]), io.Discard); err != nil {
			t.Errorf("docs/connect.md runs %q, which connect refuses: %v", strings.TrimSpace(command[0]), err)
		}
	}
	for _, want := range []string{
		"    " + proofVariable + ":\n      aud: " + proofAudience,
		"GITHUB_REPOSITORY_OWNER_ID", "CI_PROJECT_NAMESPACE_ID", "id-token: write", "identity:read", "installations:write", "rows:write",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("docs/connect.md does not hold %q", want)
		}
	}
}

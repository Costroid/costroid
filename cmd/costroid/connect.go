package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/Costroid/costroid/internal/gateway"
)

const (
	// proofVariable names the GitLab job's ID token, which the job's id_tokens
	// keyword sets with the tenant's audience.
	proofVariable = "COSTROID_PLATFORM_PROOF"
	// renewAfter is the age of a binding's last proof at which connect sends a
	// new one. A daily schedule then renews it once a day, and a push on the
	// same day sends nothing.
	renewAfter = 20 * time.Hour
	// proofAudience and the tenant's id are the audience of a proof, which
	// nothing else in either product uses.
	proofAudience       = "https://app.costroid.com/tenants/"
	githubOwnerKind     = "github-owner:"
	gitlabNamespaceKind = "gitlab-namespace:"
)

var (
	// proofInstallationPattern is the ids a CI job proves: a GitHub owner that
	// runs only the action, and a GitLab.com namespace.
	proofInstallationPattern = regexp.MustCompile(`^(github-owner|gitlab-namespace):[1-9][0-9]{0,19}$`)
	// compactJWSPattern is integration/v0.2's CompactJws.
	compactJWSPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

const connectHelp = `Usage:
  costroid connect --platform-url URL [--installation-id ID]

connect binds the job's owner to the Costroid Platform tenant of the
integration token in COSTROID_PLATFORM_TOKEN, so that costroid export can send
rows under it. The token needs identity:read and installations:write.
--platform-url is the platform's https origin.

On GitHub Actions the installation is github-owner: and
GITHUB_REPOSITORY_OWNER_ID. connect reads the tenant from GET /identity, and
asks GitHub for the job's OIDC token with the tenant's audience, which needs
id-token: write. On GitLab CI it is gitlab-namespace: and
CI_PROJECT_NAMESPACE_ID, and the proof is the job's ID token in
COSTROID_PLATFORM_PROOF, which id_tokens sets with the tenant's audience.
--installation-id states the same id, and connect refuses any other. Outside
both, --installation-id is needed, and connect only reads whether it is bound.

The platform takes a proof from a scheduled run or a push to a protected
branch. A binding stays current for 7 days after its last proof, so a daily
schedule keeps it. When the installation was proven in the last 20 hours,
connect sends nothing. The token and the proof are never printed.
docs/connect.md gives the job.

Exit codes of connect:
  0                 the installation is bound to the token's tenant
  1                 the platform refused, and the line names its code
  125               costroid could not read its input, get a proof or reach the platform
`

type connectFlags struct {
	base, installation string
}

// ciJob is where connect runs: the installation its proof binds, and how it
// gets the proof.
type ciJob struct {
	installation string
	// proof returns the CI system's ID token for the audience. It is nil
	// outside CI, where no proof exists.
	proof func(ctx context.Context, client *http.Client, audience string) (string, error)
}

// binding is an installation the platform lists as active for the tenant. On
// a refusal it holds the tenant once GET /identity named it.
type binding struct {
	installation, tenant string
	provenAt             time.Time
	// sent is false when the binding was current and no proof was sent.
	sent bool
}

func (b binding) print(stdout io.Writer) {
	proven := b.provenAt.UTC().Format(time.RFC3339)
	if b.sent {
		fmt.Fprintf(stdout, "costroid: %s is bound to tenant %s, proven at %s\n", b.installation, b.tenant, proven)
		return
	}
	fmt.Fprintf(stdout, "costroid: %s is bound to tenant %s, proven at %s, so no proof was sent\n", b.installation, b.tenant, proven)
}

// connectCommand sends with a client that follows no redirect, so the token
// and the proof reach only the origin named.
func connectCommand(ctx context.Context, args []string, std stdio) int {
	client := &http.Client{
		Timeout:       platformTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return connect(ctx, args, client, os.Getenv, time.Now, std.stdout, std.stderr)
}

func connect(ctx context.Context, args []string, client *http.Client, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	flags, err := parseConnect(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	job, err := connectJob(flags.installation, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	target := platform{client: client, base: flags.base, token: getenv(platformTokenVariable)}
	bound, err := bind(ctx, target, job, now)
	var refused *refusal
	switch {
	case err == nil:
		bound.print(stdout)
		return 0
	case errors.As(err, &refused):
		code := refused.code
		if code == "" {
			code = "with no refusal code"
		}
		fmt.Fprintf(stderr, "costroid: Costroid Platform refused %s: %d %s. %s\n", refused.what, refused.status, code, refusalSentence(refused.code, bound))
		return exitRejected
	default:
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
}

func parseConnect(args []string, stderr io.Writer) (connectFlags, error) {
	set := flag.NewFlagSet("connect", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	address := set.String("platform-url", "", "Costroid Platform's https origin")
	installation := set.String("installation-id", "", "the installation to bind: github-owner:<id> or gitlab-namespace:<id>")
	if err := set.Parse(args); err != nil {
		return connectFlags{}, err
	}
	if set.NArg() > 0 {
		return connectFlags{}, errors.New("connect takes no argument")
	}
	if *address == "" {
		return connectFlags{}, errors.New("connect needs --platform-url")
	}
	if *installation != "" && !proofInstallationPattern.MatchString(*installation) {
		return connectFlags{}, errors.New("--installation-id must be github-owner:<id> or gitlab-namespace:<id>, the ids a CI job proves")
	}
	base, err := integrationBase(*address)
	if err != nil {
		return connectFlags{}, err
	}
	return connectFlags{base: base, installation: *installation}, nil
}

// connectJob reads the installation from the CI system's own variables. A
// given id must be the job's own, because a proof binds only the owner it was
// issued under.
func connectJob(given string, getenv func(string) string) (ciJob, error) {
	var job ciJob
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		owner := getenv("GITHUB_REPOSITORY_OWNER_ID")
		if !installationNumber.MatchString(owner) {
			return ciJob{}, fmt.Errorf("GITHUB_REPOSITORY_OWNER_ID is %q, not an owner's number", owner)
		}
		job = ciJob{installation: githubOwnerKind + owner, proof: githubProof(getenv)}
	case getenv("GITLAB_CI") == "true":
		namespace := getenv("CI_PROJECT_NAMESPACE_ID")
		if !installationNumber.MatchString(namespace) {
			return ciJob{}, fmt.Errorf("CI_PROJECT_NAMESPACE_ID is %q, not a namespace's number", namespace)
		}
		job = ciJob{installation: gitlabNamespaceKind + namespace, proof: gitlabProof(getenv)}
	case given == "":
		return ciJob{}, errors.New("outside GitHub Actions and GitLab CI, connect needs --installation-id, and only reads whether it is bound")
	default:
		return ciJob{installation: given}, nil
	}
	if given != "" && given != job.installation {
		return ciJob{}, fmt.Errorf("--installation-id is %s, and this job runs under %s: a job proves only its own owner", given, job.installation)
	}
	return job, nil
}

// githubProof asks GitHub Actions for the job's OIDC token.
func githubProof(getenv func(string) string) func(context.Context, *http.Client, string) (string, error) {
	return func(ctx context.Context, client *http.Client, audience string) (string, error) {
		if getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" || getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "" {
			return "", errors.New("this job has no id-token permission, so it cannot prove its owner: give the job id-token: write")
		}
		token, err := gateway.ActionsToken(ctx, client, getenv, audience)
		if err != nil {
			return "", fmt.Errorf("no proof: %w", err)
		}
		return token, nil
	}
}

// gitlabProof reads the ID token the job's id_tokens keyword set. GitLab
// fixes its audience in the pipeline file, so connect cannot choose it.
func gitlabProof(getenv func(string) string) func(context.Context, *http.Client, string) (string, error) {
	return func(_ context.Context, _ *http.Client, audience string) (string, error) {
		token := getenv(proofVariable)
		if token == "" {
			return "", fmt.Errorf("COSTROID_PLATFORM_PROOF is empty, so this job has no proof: give the job an ID token of that name with aud %s", audience)
		}
		if !compactJWSPattern.MatchString(token) {
			return "", errors.New("COSTROID_PLATFORM_PROOF does not hold an ID token: set it only through the job's id_tokens")
		}
		return token, nil
	}
}

// bind leaves a binding proven in the last 20 hours as it is, and sends one
// proof otherwise.
func bind(ctx context.Context, target platform, job ciJob, now func() time.Time) (binding, error) {
	if !integrationTokenPattern.MatchString(target.token) {
		return binding{}, errors.New("COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform")
	}
	identity, err := target.identity(ctx)
	if err != nil {
		return binding{}, err
	}
	if held, ok := identity.current(job.installation, now()); ok {
		return held, nil
	}
	named := binding{installation: job.installation, tenant: identity.Tenant.ID}
	if job.proof == nil {
		return named, fmt.Errorf("%s has no binding to tenant %s proven in the last 20 hours, and outside GitHub Actions and GitLab CI no proof exists: run connect in a scheduled job", job.installation, identity.Tenant.ID)
	}
	proof, err := job.proof(ctx, target.client, proofAudience+identity.Tenant.ID)
	if err != nil {
		return named, err
	}
	sent, err := target.sendProof(ctx, job.installation, identity.Tenant.ID, proof)
	if err != nil {
		return named, err
	}
	return sent, nil
}

// identityAnswer is the part of GET /identity that connect reads.
type identityAnswer struct {
	Tenant struct {
		ID string `json:"id"`
	} `json:"tenant"`
	Installations []installationAnswer `json:"installations"`
}

type installationAnswer struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ProvenAt string `json:"proven_at"`
}

func (p platform) identity(ctx context.Context) (identityAnswer, error) {
	request, err := p.request(ctx, http.MethodGet, "/identity", nil)
	if err != nil {
		return identityAnswer{}, err
	}
	_, data, err := p.do(request, "the identity request")
	if err != nil {
		return identityAnswer{}, err
	}
	var identity identityAnswer
	if err := json.Unmarshal(data, &identity); err != nil || !tenantIDPattern.MatchString(identity.Tenant.ID) {
		return identityAnswer{}, errors.New("the identity answer of Costroid Platform names no tenant id")
	}
	return identity, nil
}

// current finds the installation active and proven less than 20 hours ago.
func (a identityAnswer) current(installation string, now time.Time) (binding, bool) {
	for _, held := range a.Installations {
		if held.ID != installation || held.Status != "active" {
			continue
		}
		proven, err := time.Parse(time.RFC3339, held.ProvenAt)
		if err == nil && now.Sub(proven) < renewAfter {
			return binding{installation: installation, tenant: a.Tenant.ID, provenAt: proven}, true
		}
	}
	return binding{}, false
}

// sendProof puts the proof in the body, never in the path or a header the
// platform might log.
func (p platform) sendProof(ctx context.Context, installation, tenant, proof string) (binding, error) {
	body, err := json.Marshal(struct {
		Proof string `json:"proof"`
	}{proof})
	if err != nil {
		return binding{}, fmt.Errorf("encode the proof: %w", err)
	}
	request, err := p.request(ctx, http.MethodPut, "/installations/"+installation, bytes.NewReader(body))
	if err != nil {
		return binding{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	_, data, err := p.do(request, "the proof for "+installation)
	if err != nil {
		return binding{}, err
	}
	var answer installationAnswer
	if err := json.Unmarshal(data, &answer); err != nil || answer.ID != installation || answer.Status != "active" {
		return binding{}, fmt.Errorf("the answer of Costroid Platform to the proof for %s shows no active binding", installation)
	}
	proven, err := time.Parse(time.RFC3339, answer.ProvenAt)
	if err != nil {
		return binding{}, fmt.Errorf("the answer of Costroid Platform to the proof for %s names no time it was proven", installation)
	}
	return binding{installation: installation, tenant: tenant, provenAt: proven, sent: true}, nil
}

// refusalSentence says what to do about a refusal of connect's two requests.
// A code the contract does not name gets the page that lists them.
func refusalSentence(code string, named binding) string {
	switch code {
	case "INSTALLATION_PROOF_UNPROTECTED_RUN":
		return "Run costroid connect from a scheduled job, or protect the default branch and run it on a push there."
	case "INSTALLATION_BOUND_ELSEWHERE":
		return "Another tenant holds this owner, and this proof does not outrank its binding, as docs/connect.md explains."
	case "INSTALLATION_PROOF_AUDIENCE_MISMATCH":
		return fmt.Sprintf("The proof names another tenant than the integration token, so set the ID token's aud to %s%s.", proofAudience, named.tenant)
	case "INSTALLATION_PROOF_EXPIRED":
		return "The proof is past its window or older than the binding's last change, so run the job again for a new one."
	case "INSTALLATION_PROOF_SUBJECT_MISMATCH":
		return fmt.Sprintf("The proof names another owner than %s, so run costroid connect in a job of that owner.", named.installation)
	case "INSTALLATION_PROOF_CLAIMS_INVALID", "INSTALLATION_PROOF_SIGNATURE_INVALID", "INSTALLATION_PROOF_MISSING":
		return "The proof is not the ID token the CI system issued, so pass the job's own token unchanged."
	case "INSTALLATION_PROOF_ISSUER_UNTRUSTED":
		return "The platform trusts proofs from GitHub Actions and GitLab.com only, so a self-managed GitLab or GitHub Enterprise Server cannot bind."
	case "INSTALLATION_PROOF_KEYS_UNAVAILABLE":
		return "The platform could not read the CI system's keys, so run the job again."
	case "AUTH_TOKEN_INVALID":
		return "The integration token is unknown, expired or revoked, so create one on Costroid Platform."
	case "AUTH_TOKEN_SCOPE":
		return "The integration token needs identity:read and installations:write."
	default:
		return "The contract's refusals in docs/connect.md say what each code means."
	}
}

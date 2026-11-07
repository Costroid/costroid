package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
)

const (
	platformTokenVariable = "COSTROID_PLATFORM_TOKEN"
	// integrationPath is where Costroid Platform serves integration/v0.2.
	integrationPath = "/api/integration/v0.2"
	// rowsAudience opens a run token's audience, and the tenant's id and
	// /rows close it.
	rowsAudience       = "https://app.costroid.com/tenants/"
	exportFocusVersion = "1.4"
	maxPlatformAnswer  = 64 << 10
	platformTimeout    = 2 * time.Minute
)

// The patterns are integration/v0.2's.
var (
	integrationTokenPattern = regexp.MustCompile(`^costroid_it_[A-Za-z0-9_-]{43}$`)
	installationIDPattern   = regexp.MustCompile(`^(github|github-owner|gitlab-namespace):[1-9][0-9]{0,19}$`)
	batchIDPattern          = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	tenantIDPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	refusalCodePattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	runNumberPattern        = regexp.MustCompile(`^[0-9]{1,20}$`)
)

const exportHelp = `Usage:
  costroid export --rows rows.jsonl --platform-url URL --installation-id ID [--batch-id ID]

export sends a rows file to Costroid Platform as one batch of CSV, under the
integration token in COSTROID_PLATFORM_TOKEN. --platform-url is the platform's
https origin. --installation-id names the installation the rows came through:
github:<id>, github-owner:<id> or gitlab-namespace:<id>.

On GitHub Actions the batch id is the repository id, the run id and the run
attempt, joined by hyphens, so sending a run's rows again restates its batch.
The job's OIDC token rides with the batch and names the run. That needs
id-token: write on the job, and identity:read beside rows:write on the token.
Elsewhere --batch-id names the batch.

On 402 or 429 the platform took nothing for now and nothing in the rows is
wrong. export warns, keeps the rows file and exits 0, so it can be sent later.
docs/export.md lists every answer.

Exit codes of export:
  0                 the platform took the batch, or answered 402 or 429
  1                 the platform refused, and the line names its code
  125               costroid could not read the rows or reach the platform
`

type exportFlags struct {
	rows, base, installation, batch string
}

// refusal is an answer of the platform that took nothing.
type refusal struct {
	what       string
	status     int
	code       string
	retryAfter string
}

func (r *refusal) Error() string {
	code := r.code
	if code == "" {
		code = "with no refusal code"
	}
	return fmt.Sprintf("Costroid Platform refused %s: %d %s", r.what, r.status, code)
}

// later reports whether the same rows can be sent again: the tenant's plan or
// a quota refused them, not anything in them.
func (r *refusal) later() bool {
	return r.status == http.StatusPaymentRequired || r.status == http.StatusTooManyRequests
}

// platform is Costroid Platform's integration API under one token.
type platform struct {
	client *http.Client
	base   string
	token  string
}

// exportCommand sends with a client that follows no redirect, so the token
// and the rows reach only the origin named.
func exportCommand(ctx context.Context, args []string, std stdio) int {
	client := &http.Client{
		Timeout:       platformTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return export(ctx, args, client, os.Getenv, std.stdout, std.stderr)
}

func export(ctx context.Context, args []string, client *http.Client, getenv func(string) string, stdout, stderr io.Writer) int {
	flags, err := parseExport(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	sent, err := sendBatch(ctx, flags, platform{client: client, base: flags.base, token: getenv(platformTokenVariable)}, getenv)
	var refused *refusal
	switch {
	case err == nil:
		sent.print(flags.rows, stdout, stderr)
		return 0
	case errors.As(err, &refused) && refused.later():
		retry := ""
		if runNumberPattern.MatchString(refused.retryAfter) {
			retry = ", retry after " + refused.retryAfter + " s"
		}
		fmt.Fprintf(stderr, "costroid: warning: %v%s. Nothing in the rows is wrong, and %s is kept to send again\n", err, retry, flags.rows)
		return 0
	case errors.As(err, &refused):
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitRejected
	default:
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
}

func parseExport(args []string, stderr io.Writer) (exportFlags, error) {
	set := flag.NewFlagSet("export", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	rows := set.String("rows", "", "the rows file costroid run wrote")
	address := set.String("platform-url", "", "Costroid Platform's https origin")
	installation := set.String("installation-id", "", "the installation the rows came through")
	batch := set.String("batch-id", "", "the batch id; GitHub Actions derives one from the run")
	if err := set.Parse(args); err != nil {
		return exportFlags{}, err
	}
	if set.NArg() > 0 {
		return exportFlags{}, errors.New("export takes no argument")
	}
	if *rows == "" || *address == "" || *installation == "" {
		return exportFlags{}, errors.New("export needs --rows, --platform-url and --installation-id")
	}
	if !installationIDPattern.MatchString(*installation) {
		return exportFlags{}, errors.New("--installation-id must be github:<id>, github-owner:<id> or gitlab-namespace:<id>, with the number the platform bound")
	}
	if *batch != "" && !batchIDPattern.MatchString(*batch) {
		return exportFlags{}, errors.New("--batch-id must be 1 to 128 letters, digits, dots, hyphens or underscores")
	}
	base, err := integrationBase(*address)
	if err != nil {
		return exportFlags{}, err
	}
	return exportFlags{rows: *rows, base: base, installation: *installation, batch: *batch}, nil
}

// integrationBase takes an origin only, so the contract's version in the path
// is this binary's to choose.
func integrationBase(address string) (string, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("--platform-url must be an https origin, such as https://app.costroid.com")
	}
	return "https://" + parsed.Host + integrationPath, nil
}

// sentBatch is what the platform took.
type sentBatch struct {
	batch  string
	rows   int
	digest [sha256.Size]byte
	status int
	quota  string
}

func (s sentBatch) print(path string, stdout, stderr io.Writer) {
	if s.rows == 0 {
		fmt.Fprintf(stdout, "costroid: %s holds no rows, so nothing was sent\n", path)
		return
	}
	outcome := "accepted for ingest"
	if s.status == http.StatusOK {
		outcome = "already held, so nothing changed"
	}
	fmt.Fprintf(stdout, "costroid: batch %s, %d rows, sha256 %x: %s\n", s.batch, s.rows, s.digest, outcome)
	if s.quota == "approaching" || s.quota == "exhausted" {
		fmt.Fprintf(stderr, "costroid: warning: Costroid Platform's export quota is %s\n", s.quota)
	}
}

func sendBatch(ctx context.Context, flags exportFlags, target platform, getenv func(string) string) (sentBatch, error) {
	if !integrationTokenPattern.MatchString(target.token) {
		return sentBatch{}, errors.New("COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform")
	}
	body, rows, err := csvBody(flags.rows)
	if err != nil || rows == 0 {
		return sentBatch{}, err
	}
	batch, err := batchID(flags.batch, getenv)
	if err != nil {
		return sentBatch{}, err
	}
	runToken, err := target.runToken(ctx, getenv)
	if err != nil {
		return sentBatch{}, err
	}
	sent := sentBatch{batch: batch, rows: rows, digest: sha256.Sum256(body)}
	request, err := target.request(ctx, http.MethodPut, "/exports/"+batch, bytes.NewReader(body))
	if err != nil {
		return sentBatch{}, err
	}
	request.Header.Set("Content-Type", "text/csv")
	request.Header.Set("Costroid-Installation-Id", flags.installation)
	request.Header.Set("Costroid-Content-SHA256", hex.EncodeToString(sent.digest[:]))
	request.Header.Set("Costroid-Focus-Version", exportFocusVersion)
	if runToken != "" {
		request.Header.Set("Costroid-Run-Token", runToken)
	}
	response, _, err := target.do(request, "batch "+batch)
	if err != nil {
		return sentBatch{}, err
	}
	sent.status, sent.quota = response.StatusCode, response.Header.Get("Costroid-Quota-State")
	return sent, nil
}

func csvBody(path string) ([]byte, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	var body bytes.Buffer
	rows, err := focus.WriteCSV(&body, file)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	return body.Bytes(), rows, nil
}

// batchID is the flag's, or on GitHub Actions the run's: the repository id,
// the run id and the run attempt. A retry of one run's send restates its
// batch, and a re-run attempt, which runs the agent again, adds one.
func batchID(given string, getenv func(string) string) (string, error) {
	if given != "" {
		return given, nil
	}
	if getenv("GITHUB_ACTIONS") != "true" {
		return "", errors.New("outside GitHub Actions, export needs --batch-id")
	}
	names := []string{"GITHUB_REPOSITORY_ID", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = getenv(name)
		if !runNumberPattern.MatchString(parts[i]) {
			return "", fmt.Errorf("%s is %q, not a number, so no batch id can be derived: pass --batch-id", name, parts[i])
		}
	}
	return strings.Join(parts, "-"), nil
}

// runToken is the job's OIDC token for this tenant's rows audience, on GitHub
// Actions, and empty elsewhere.
func (p platform) runToken(ctx context.Context, getenv func(string) string) (string, error) {
	if getenv("GITHUB_ACTIONS") != "true" {
		return "", nil
	}
	if getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" || getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "" {
		return "", errors.New("this job has no id-token permission, so no run token can name the run: give the job id-token: write")
	}
	tenant, err := p.tenant(ctx)
	if err != nil {
		return "", err
	}
	token, err := gateway.ActionsToken(ctx, p.client, getenv, rowsAudience+tenant+"/rows")
	if err != nil {
		return "", fmt.Errorf("no run token: %w", err)
	}
	return token, nil
}

// tenant reads the token's tenant from GET /identity.
func (p platform) tenant(ctx context.Context) (string, error) {
	request, err := p.request(ctx, http.MethodGet, "/identity", nil)
	if err != nil {
		return "", err
	}
	_, data, err := p.do(request, "the identity request")
	if err != nil {
		return "", err
	}
	var identity struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	}
	if err := json.Unmarshal(data, &identity); err != nil || !tenantIDPattern.MatchString(identity.Tenant.ID) {
		return "", errors.New("the identity answer of Costroid Platform names no tenant id")
	}
	return identity.Tenant.ID, nil
}

func (p platform) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("build the request to Costroid Platform: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("User-Agent", "costroid/"+version)
	return request, nil
}

// do returns a 2xx answer and its body, and any other answer as a refusal
// carrying the code the platform sent. No other part of a refusal's body is
// read, so nothing the platform says reaches the log but its code.
func (p platform) do(request *http.Request, what string) (*http.Response, []byte, error) {
	response, err := p.client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("send %s to Costroid Platform: %w", what, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPlatformAnswer))
	if err != nil {
		return nil, nil, fmt.Errorf("read Costroid Platform's answer to %s: %w", what, err)
	}
	if response.StatusCode/100 == 2 {
		return response, data, nil
	}
	var answer struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	code := ""
	if json.Unmarshal(data, &answer) == nil && refusalCodePattern.MatchString(answer.Error.Code) {
		code = answer.Error.Code
	}
	return nil, nil, &refusal{what: what, status: response.StatusCode, code: code, retryAfter: response.Header.Get("Retry-After")}
}

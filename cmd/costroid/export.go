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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	exportformat "github.com/Costroid/costroid/internal/export"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
)

const (
	platformTokenVariable = "COSTROID_PLATFORM_TOKEN"
	// integrationPath is where connect and doctor reach Costroid Platform:
	// integration/v0.2, whose installations and identity version 0.3 keeps.
	integrationPath = "/api/integration/v0.2"
	// rowsAudience opens a run token's audience, and the tenant's id and
	// /rows close it.
	rowsAudience       = "https://app.costroid.com/tenants/"
	exportFocusVersion = "1.4"
	maxPlatformAnswer  = 64 << 10
	platformTimeout    = 2 * time.Minute
	// installationNotBound is the refusal of a batch whose installation the
	// tenant holds no binding of, which costroid connect makes.
	installationNotBound = "INSTALLATION_NOT_BOUND"
	tokenVerified        = "token_verified"
	unverified           = "unverified"
	unattributed         = "unattributed"
	windowed             = "windowed"
)

// verdictPace is how often export asks for a batch's verdict, and for how long.
type verdictPace struct {
	interval, limit time.Duration
}

// ingestPace waits at most 60 s, so a CI job learns a batch's verdict and a
// slow ingest still ends the job in about a minute.
var ingestPace = verdictPace{interval: 2 * time.Second, limit: 60 * time.Second}

// contract is one version of the integration contract, as GET /identity
// states it under that version's path.
type contract struct {
	Version  string `json:"version"`
	TagSet   string `json:"tag_set"`
	FocusRow string `json:"focus_row"`
}

// path is where a platform that speaks the version serves it.
func (c contract) path() string {
	return "/api/integration/v" + c.Version
}

// Version 0.3 is 0.2 with rows of focus-row/v0.5 and tags of tags/v0.4, which
// carry the tags a caller names and the repository's visibility. Version 0.2
// is what export sent before it read the platform's contract, so it is also
// what export sends when the token may not read GET /identity.
var (
	contractV03 = contract{Version: "0.3", TagSet: "tags/v0.4", FocusRow: "focus-row/v0.5"}
	contractV02 = contract{Version: "0.2", TagSet: "tags/v0.2", FocusRow: "focus-row/v0.3"}
	// exportContracts are the versions export sends by, newest first.
	exportContracts = []contract{contractV03, contractV02}
)

// The patterns are integration/v0.2's, and version 0.3 keeps them.
var (
	integrationTokenPattern = regexp.MustCompile(`^costroid_it_[A-Za-z0-9_-]{43}$`)
	installationIDPattern   = regexp.MustCompile(`^(github|github-owner|gitlab-namespace):[1-9][0-9]{0,19}$`)
	installationNumber      = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	batchIDPattern          = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	tenantIDPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	refusalCodePattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	runNumberPattern        = regexp.MustCompile(`^[0-9]{1,20}$`)
	// pullRepositoryPattern and pullNumberPattern hold the costroid/repo and
	// costroid/pr tags a pull request link is built from. The agent's job
	// wrote them, so a value outside either gets no link.
	pullRepositoryPattern = regexp.MustCompile(`^[a-z0-9.-]+(/[a-z0-9._~-]+){1,8}$`)
	pullNumberPattern     = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
)

const exportHelp = `Usage:
  costroid export --rows rows.jsonl --platform-url URL --installation-id ID
                  [--batch-id ID]
  costroid export --rows rows.jsonl --format focus-csv|vantage|datadog
                  [--out PATH]

export sends a rows file to Costroid Platform as one batch of CSV, under the
integration token in COSTROID_PLATFORM_TOKEN. It reads GET /identity first and
sends by integration/v0.3, or by v0.2 to a platform that states no later
version, and names the version on standard error. Outside GitHub Actions a
token without identity:read still sends, by v0.2, with a warning.
--platform-url is the platform's
https origin, or an http one on a loopback address such as
http://127.0.0.1:8787. --installation-id names the installation the rows came
through: github:<id>, github-owner:<id> or gitlab-namespace:<id>. A bare
number is a GitHub App installation and is sent as github:<id>, as report
reads it.

On GitHub Actions the batch id is the repository id, the run id and the run
attempt, joined by hyphens, so sending a run's rows again restates its batch.
The job's OIDC token rides with the batch and names the run. That needs
id-token: write on the job, and identity:read beside rows:write on the token.
A row that names this run in GITHUB_RUN_ID and this repository in
GITHUB_REPOSITORY goes as token_verified, and every other row as unverified.
Elsewhere --batch-id names the batch, no run token is sent, and every row goes
as unverified.

Once the platform takes the batch, export asks for its verdict every 2 s for
up to 60 s, and prints ingested with a link to each pull request, or the code
the batch failed on.

On 402 or 429 the platform took nothing for now and nothing in the rows is
wrong. On 409 INSTALLATION_NOT_BOUND the tenant holds no binding of the
installation, which costroid connect makes. In each case export warns, keeps
the rows file and exits 0, so it can be sent later. docs/export.md lists every
answer.

With --format, export sends nothing and writes the rows as a file another
tool imports, to --out or to standard output. Every amount is written as the
row states it: none is converted, rounded or added up.

  focus-csv         FOCUS 1.4 CSV: the row contract's 40 columns, the CSV
                    the platform takes
  vantage           a Vantage custom provider's CSV. Past 10,000 rows or
                    2,000,000 bytes the rows go to more than one file, so
                    costs.csv becomes costs-1.csv, costs-2.csv and on, and
                    standard output is refused
  datadog           Datadog's Custom Costs CSV. It takes USD only, so a row in
                    another currency refuses the file, and the line names it.
                    Each tag that every row carries is a column

In vantage and datadog, BilledCost is the row's EffectiveCost, because a row's
own BilledCost is 0: Costroid issues no invoice.

docs/export.md says which column each format takes from the row.

Exit codes of export:
  0                 the batch ingested or its verdict did not come in 60 s, or
                    the platform answered 402, 409 INSTALLATION_NOT_BOUND or
                    429, or --format wrote its files
  1                 the platform refused or the batch failed, and the line
                    names its code, or the format refused rows, and the line
                    names them
  125               costroid could not read the rows, reach the platform or
                    write a file
`

type exportFlags struct {
	rows, origin, installation, batch string
	// format and out are the file export writes instead of sending.
	format exportformat.Format
	out    string
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

// unauthorized reports a request refused for the token: 401 or 403.
func (r *refusal) unauthorized() bool {
	return r.status == http.StatusUnauthorized || r.status == http.StatusForbidden
}

// unbound reports a batch refused because the tenant holds no binding of its
// installation. Nothing in the rows is wrong there either.
func (r *refusal) unbound() bool {
	return r.status == http.StatusConflict && r.code == installationNotBound
}

// platform is Costroid Platform's integration API under one token. origin
// is set where the version is still to be read, and base once it is.
type platform struct {
	client *http.Client
	origin string
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
	return export(ctx, args, client, os.Getenv, ingestPace, std.stdout, std.stderr)
}

func export(ctx context.Context, args []string, client *http.Client, getenv func(string) string, pace verdictPace, stdout, stderr io.Writer) int {
	flags, err := parseExport(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if flags.format != "" {
		return writeFormat(flags, stdout, stderr)
	}
	target := platform{client: client, origin: flags.origin, token: getenv(platformTokenVariable)}
	sent, err := sendBatch(ctx, flags, &target, getenv, stderr)
	var refused *refusal
	switch {
	case err == nil:
	case errors.As(err, &refused) && refused.later():
		retry := ""
		if runNumberPattern.MatchString(refused.retryAfter) {
			retry = ", retry after " + refused.retryAfter + " s"
		}
		fmt.Fprintf(stderr, "costroid: warning: %v%s. Nothing in the rows is wrong, and %s is kept to send again\n", err, retry, flags.rows)
		return 0
	case errors.As(err, &refused) && refused.unbound():
		fmt.Fprintf(stderr, "costroid: warning: %v. The tenant holds no binding of %s: run costroid connect, as docs/connect.md shows, and %s is kept to send again\n", err, flags.installation, flags.rows)
		return 0
	case errors.As(err, &refused):
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitRejected
	default:
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	sent.print(flags.rows, stdout, stderr)
	if sent.rows == 0 {
		return 0
	}
	verdict, err := target.awaitVerdict(ctx, sent, pace)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v. The platform took batch %s, and states its verdict at GET /exports/%s\n", err, sent.batch, sent.batch)
		return 0
	}
	return verdict.print(sent, flags.origin, stdout, stderr)
}

func parseExport(args []string, stdout, stderr io.Writer) (exportFlags, error) {
	set := flag.NewFlagSet("export", flag.ContinueOnError)
	set.SetOutput(stderr)
	rows := set.String("rows", "", "the rows file costroid run wrote")
	address := set.String("platform-url", "", "Costroid Platform's https origin")
	installation := set.String("installation-id", "", "the installation the rows came through")
	batch := set.String("batch-id", "", "the batch id; GitHub Actions derives one from the run")
	format := set.String("format", "", "the file format to write instead of sending")
	out := set.String("out", "", "the file --format writes; standard output when absent")
	if err := parseFlags(set, args, exportHelp, stdout, stderr); err != nil {
		return exportFlags{}, err
	}
	if set.NArg() > 0 {
		return exportFlags{}, errors.New("export takes no argument")
	}
	if *format != "" {
		return parseFormatFlags(*rows, *format, *out, *address != "" || *installation != "" || *batch != "")
	}
	if *out != "" {
		return exportFlags{}, errors.New("--out names the file --format writes, and export without --format sends to Costroid Platform")
	}
	if *rows == "" || *address == "" || *installation == "" {
		return exportFlags{}, errors.New("export needs --rows, --platform-url and --installation-id")
	}
	installationID, err := exportInstallation(*installation)
	if err != nil {
		return exportFlags{}, err
	}
	if *batch != "" && !batchIDPattern.MatchString(*batch) {
		return exportFlags{}, errors.New("--batch-id must be 1 to 128 letters, digits, dots, hyphens or underscores")
	}
	origin, err := platformOrigin(*address)
	if err != nil {
		return exportFlags{}, err
	}
	return exportFlags{rows: *rows, origin: origin, installation: installationID, batch: *batch}, nil
}

// parseFormatFlags reads the flags of a file export, which refuses any flag
// that names the platform: sends reports one.
func parseFormatFlags(rows, format, out string, sends bool) (exportFlags, error) {
	if rows == "" {
		return exportFlags{}, errors.New("export --format needs --rows")
	}
	if sends {
		return exportFlags{}, errors.New("export --format writes a file and sends nothing, so it takes no --platform-url, --installation-id or --batch-id")
	}
	parsed, err := exportformat.ParseFormat(format)
	if err != nil {
		return exportFlags{}, err
	}
	return exportFlags{rows: rows, format: parsed, out: out}, nil
}

// writeFormat writes the rows file in its format. Nothing is written until
// every row is taken, so a refused export leaves no file.
func writeFormat(flags exportFlags, stdout, stderr io.Writer) int {
	file, err := os.Open(flags.rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	defer file.Close()
	written, err := exportformat.Write(flags.format, file)
	var refused *exportformat.RowsError
	switch {
	case errors.Is(err, exportformat.ErrNoRows):
		fmt.Fprintf(stderr, "costroid: %s holds no rows, so nothing was written\n", flags.rows)
		return 0
	case errors.As(err, &refused):
		fmt.Fprintf(stderr, "costroid: %v. Nothing was written\n", err)
		return exitRejected
	case err != nil:
		fmt.Fprintf(stderr, "costroid: %s: %v\n", flags.rows, err)
		return exitFailure
	}
	if len(written.OmittedTags) > 0 {
		fmt.Fprintf(stderr, "costroid: warning: some rows lack the tags %s, so the %s file has no column for them\n", strings.Join(written.OmittedTags, ", "), flags.format)
	}
	if flags.out == "" {
		return writeStdout(flags.format, written.Files, stdout, stderr)
	}
	paths := partPaths(flags.out, len(written.Files))
	for i, part := range written.Files {
		if err := os.WriteFile(paths[i], part.Body, 0o644); err != nil {
			fmt.Fprintf(stderr, "costroid: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(stdout, "costroid: wrote %d rows as %s to %s\n", part.Rows, flags.format, paths[i])
	}
	return 0
}

// writeStdout writes a format of one file to standard output, and refuses one
// that needs more, which a stream cannot keep apart.
func writeStdout(format exportformat.Format, files []exportformat.File, stdout, stderr io.Writer) int {
	if len(files) > 1 {
		fmt.Fprintf(stderr, "costroid: the rows need %d %s files, so name them with --out. Nothing was written\n", len(files), format)
		return exitRejected
	}
	if _, err := stdout.Write(files[0].Body); err != nil {
		fmt.Fprintf(stderr, "costroid: write the %s file: %v\n", format, err)
		return exitFailure
	}
	return 0
}

// partPaths names the files of an export: the path itself for one file, and
// for more, the path with -1, -2 and on before its extension.
func partPaths(path string, count int) []string {
	if count == 1 {
		return []string{path}
	}
	extension := filepath.Ext(path)
	stem := strings.TrimSuffix(path, extension)
	paths := make([]string, count)
	for i := range paths {
		paths[i] = fmt.Sprintf("%s-%d%s", stem, i+1, extension)
	}
	return paths
}

// githubInstallation is the prefix of a GitHub App installation id.
const githubInstallation = "github:"

// exportInstallation reads --installation-id in the form the platform binds.
// A bare number is a GitHub App installation, as report reads it, and is sent
// as github:<id>.
func exportInstallation(value string) (string, error) {
	if installationNumber.MatchString(value) {
		value = githubInstallation + value
	}
	if !installationIDPattern.MatchString(value) {
		return "", errors.New("--installation-id must be github:<id>, a bare GitHub App installation number, github-owner:<id> or gitlab-namespace:<id>, with the number the platform bound")
	}
	return value, nil
}

// appInstallation reads --installation-id as report's account link names it:
// a GitHub App installation, github:<id> or the bare number. It returns the
// number, and false for any other form.
func appInstallation(value string) (string, bool) {
	number := strings.TrimPrefix(value, githubInstallation)
	return number, installationNumber.MatchString(number)
}

// integrationBase takes an origin only, so the contract's version in the path
// is this binary's to choose.
func integrationBase(address string) (string, error) {
	origin, err := platformOrigin(address)
	if err != nil {
		return "", err
	}
	return origin + integrationPath, nil
}

// platformOrigin reads --platform-url as an https origin. Plain http is taken
// only on a loopback address, where nothing crosses a network, so a test
// harness on 127.0.0.1 can be reached.
func platformOrigin(address string) (string, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback(parsed.Hostname()))) {
		return "", errors.New("--platform-url must be an https origin, such as https://app.costroid.com, or http on a loopback address")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// loopback reports a loopback IP address. A name such as localhost is not
// taken, because a resolver can answer it with another address.
func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sentBatch is what the platform took.
type sentBatch struct {
	batch  string
	rows   int
	digest [sha256.Size]byte
	status int
	quota  string
	// claim is the run the run token named, and verified counts the rows
	// that went as token_verified under it.
	claim    runClaim
	verified int
	pulls    []pullRequest
	// answer is the batch as the answer to the send stated it.
	answer batchAnswer
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
	if s.claim.runID != "" {
		fmt.Fprintf(stdout, "costroid: %d of %d rows name run %s of %s, so they went as token_verified and the rest as unverified\n", s.verified, s.rows, s.claim.runID, s.claim.repository)
	}
	if s.quota == "approaching" || s.quota == "exhausted" {
		fmt.Fprintf(stderr, "costroid: warning: Costroid Platform's export quota is %s\n", s.quota)
	}
}

// sendBatch sends the rows by the newest contract version the platform
// states, and says which on stderr. target keeps that version's path, where
// export then asks for the verdict.
func sendBatch(ctx context.Context, flags exportFlags, target *platform, getenv func(string) string, stderr io.Writer) (sentBatch, error) {
	if !integrationTokenPattern.MatchString(target.token) {
		return sentBatch{}, errors.New("COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform")
	}
	claim := runClaimOf(getenv)
	stamped, err := readStamped(flags.rows, claim)
	if err != nil || stamped.rows == 0 {
		return sentBatch{}, err
	}
	batch, err := batchID(flags.batch, getenv)
	if err != nil {
		return sentBatch{}, err
	}
	tenant, err := target.contractFor(ctx, getenv, stderr)
	if err != nil {
		return sentBatch{}, err
	}
	runToken, err := target.runToken(ctx, getenv, tenant)
	if err != nil {
		return sentBatch{}, err
	}
	sent := sentBatch{batch: batch, rows: stamped.rows, digest: sha256.Sum256(stamped.body), claim: claim, verified: stamped.verified, pulls: stamped.pulls}
	request, err := target.request(ctx, http.MethodPut, "/exports/"+batch, bytes.NewReader(stamped.body))
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
	response, data, err := target.do(request, "batch "+batch)
	if err != nil {
		return sentBatch{}, err
	}
	sent.status, sent.quota = response.StatusCode, response.Header.Get("Costroid-Quota-State")
	sent.answer = readBatchAnswer(data)
	return sent, nil
}

// stampedRows is a rows file as the batch's CSV, each row's run identity
// stamped.
type stampedRows struct {
	body     []byte
	rows     int
	verified int
	pulls    []pullRequest
}

func readStamped(path string, claim runClaim) (stampedRows, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return stampedRows{}, err
	}
	lines, verified, pulls := stampRows(data, claim)
	var body bytes.Buffer
	rows, err := focus.WriteCSV(&body, bytes.NewReader(lines))
	if err != nil {
		return stampedRows{}, fmt.Errorf("%s: %w", path, err)
	}
	return stampedRows{body: body.Bytes(), rows: rows, verified: verified, pulls: pulls}, nil
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

// runToken is the job's OIDC token for the tenant's rows audience, on GitHub
// Actions, and empty elsewhere.
func (p platform) runToken(ctx context.Context, getenv func(string) string, tenant string) (string, error) {
	if getenv("GITHUB_ACTIONS") != "true" {
		return "", nil
	}
	if getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" || getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "" {
		return "", errors.New("this job has no id-token permission, so no run token can name the run: give the job id-token: write")
	}
	token, err := gateway.ActionsToken(ctx, p.client, getenv, rowsAudience+tenant+"/rows")
	if err != nil {
		return "", fmt.Errorf("no run token: %w", err)
	}
	return token, nil
}

// contractFor sets p.base to the version export sends by, says which on
// stderr, and returns the token's tenant. Outside GitHub Actions a token
// without identity:read sent rows before export read the contract, so a
// 401 or 403 on GET /identity sends by integration/v0.2, as then, with a
// warning and no tenant. On GitHub Actions the run token needs the tenant,
// and any failure to read it, or a network failure anywhere, fails the export.
func (p *platform) contractFor(ctx context.Context, getenv func(string) string, stderr io.Writer) (string, error) {
	stated, tenant, err := p.statedContract(ctx)
	var refused *refusal
	switch {
	case err == nil:
		fmt.Fprintf(stderr, "costroid: Costroid Platform states integration/v%s, so export sends the batch by it\n", stated.Version)
		return tenant, nil
	case getenv("GITHUB_ACTIONS") != "true" && errors.As(err, &refused) && refused.unauthorized():
		p.base = p.origin + contractV02.path()
		fmt.Fprintf(stderr, "costroid: warning: %v, so export could not read the platform's contract and sends the batch by integration/v%s, as it did before. Give the token identity:read to send by the newest version\n", err, contractV02.Version)
		return "", nil
	default:
		return "", err
	}
}

// statedContract reads GET /identity under each version export sends by,
// newest first, and keeps the first whose answer states that version. A
// platform that does not speak a version serves no identity under its path,
// so the next version is asked, and the last one's error is returned. It
// sets p.base to the version's path and returns the token's tenant.
func (p *platform) statedContract(ctx context.Context) (contract, string, error) {
	var err error
	for _, version := range exportContracts {
		var tenant string
		tenant, err = p.identityUnder(ctx, version)
		if err == nil {
			p.base = p.origin + version.path()
			return version, tenant, nil
		}
	}
	return contract{}, "", err
}

// identityUnder reads GET /identity under one version's path, and returns the
// token's tenant when the answer states exactly that version.
func (p platform) identityUnder(ctx context.Context, version contract) (string, error) {
	p.base = p.origin + version.path()
	request, err := p.request(ctx, http.MethodGet, "/identity", nil)
	if err != nil {
		return "", err
	}
	_, data, err := p.do(request, "the identity request")
	if err != nil {
		return "", err
	}
	var identity struct {
		Contract contract `json:"contract"`
		Tenant   struct {
			ID string `json:"id"`
		} `json:"tenant"`
	}
	if err := json.Unmarshal(data, &identity); err != nil || !tenantIDPattern.MatchString(identity.Tenant.ID) {
		return "", errors.New("the identity answer of Costroid Platform names no tenant id")
	}
	if identity.Contract != version {
		return "", fmt.Errorf("the identity answer of Costroid Platform under %s does not state integration/v%s", version.path(), version.Version)
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

// runClaim is the run a run token names, as the rows' tags write it: the run
// id and github.com/ with the repository. Off GitHub Actions no run token is
// sent, and the zero claim names no row.
type runClaim struct {
	runID, repository string
}

func runClaimOf(getenv func(string) string) runClaim {
	if getenv("GITHUB_ACTIONS") != "true" {
		return runClaim{}
	}
	run, repository := getenv("GITHUB_RUN_ID"), getenv("GITHUB_REPOSITORY")
	if !runNumberPattern.MatchString(run) || repository == "" {
		return runClaim{}
	}
	return runClaim{runID: run, repository: "github.com/" + repository}
}

// names reports a row the run token stands behind: its run and repository
// are the token's, and its join to the pull request is not a time window.
func (c runClaim) names(tags map[string]string) bool {
	return c.runID != "" && tags[focus.TagWorkflowRunID] == c.runID && tags[focus.TagRepo] == c.repository &&
		tags[focus.TagAttributionGrade] != windowed
}

// pullRequest is a pull request the rows name, from their costroid/repo and
// costroid/pr tags.
type pullRequest struct {
	repository, number string
}

// stampRows sets costroid/run_identity on each row: token_verified on a row
// the claim names, unverified on every other. An unverified row's
// attribution_grade reads unattributed, as tags/v0.2 requires. A row already
// stamped so keeps its bytes. A line that is not a row is left as it is, so
// the CSV writer refuses it with its line number. It returns the rows, how
// many went as token_verified, and the pull requests they name.
func stampRows(data []byte, claim runClaim) ([]byte, int, []pullRequest) {
	lines := bytes.Split(data, []byte("\n"))
	verified := 0
	var pulls []pullRequest
	for i, line := range lines {
		var fields map[string]json.RawMessage
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &fields) != nil {
			continue
		}
		tags, err := readTags(fields["Tags"])
		if err != nil {
			continue
		}
		values := tags.values()
		if pull := (pullRequest{values[focus.TagRepo], values[focus.TagPR]}); pull.linked() && !slices.Contains(pulls, pull) {
			pulls = append(pulls, pull)
		}
		identity := unverified
		if claim.names(values) {
			identity, verified = tokenVerified, verified+1
		}
		changed := tags.set(focus.TagRunIdentity, identity)
		if identity == unverified && values[focus.TagAttributionGrade] != "" {
			changed = tags.set(focus.TagAttributionGrade, unattributed) || changed
		}
		if !changed {
			continue
		}
		fields["Tags"] = tags.encode()
		if stamped, err := json.Marshal(fields); err == nil {
			lines[i] = stamped
		}
	}
	return bytes.Join(lines, []byte("\n")), verified, pulls
}

func (p pullRequest) linked() bool {
	return pullRepositoryPattern.MatchString(p.repository) && pullNumberPattern.MatchString(p.number)
}

// link is the pull request's page on the platform.
func (p pullRequest) link(origin string) string {
	return origin + "/#/pulls?repo=" + p.repository + "&pr=" + p.number
}

// tagList is a Tags object in the order its row wrote it, which is the order
// its CSV cell keeps.
type tagList []tagEntry

type tagEntry struct {
	key, value string
}

// readTags reads a Tags object of strings, keeping its order.
func readTags(raw json.RawMessage) (tagList, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return nil, errors.New("the column Tags is not an object")
	}
	var tags tagList
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		tags = append(tags, tagEntry{key: key.(string), value: value})
	}
	return tags, nil
}

func (t tagList) values() map[string]string {
	values := make(map[string]string, len(t))
	for _, tag := range t {
		values[tag.key] = tag.value
	}
	return values
}

// set gives key the value, in its place or at the end, and reports whether
// anything changed.
func (t *tagList) set(key, value string) bool {
	for i, tag := range *t {
		if tag.key == key {
			(*t)[i].value = value
			return tag.value != value
		}
	}
	*t = append(*t, tagEntry{key: key, value: value})
	return true
}

func (t tagList) encode() json.RawMessage {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, tag := range t {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(tag.key)
		value, _ := json.Marshal(tag.value)
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes()
}

// batchAnswer is the part of an ExportBatch that export reads.
type batchAnswer struct {
	Status         string `json:"status"`
	Rows           *int   `json:"rows"`
	DroppedTagKeys *int   `json:"dropped_tag_keys"`
	Failure        *struct {
		Code string `json:"code"`
	} `json:"failure"`
}

// readBatchAnswer reads an answer that states a batch. Anything else reads
// as a batch with no status, whose verdict is still to come.
func readBatchAnswer(data []byte) batchAnswer {
	var answer batchAnswer
	if json.Unmarshal(data, &answer) != nil {
		return batchAnswer{}
	}
	return answer
}

// settled reports a batch that ingested or failed.
func (a batchAnswer) settled() bool {
	return a.Status == "ingested" || a.Status == "failed"
}

// pending reports a batch the platform holds and has not yet judged.
func (a batchAnswer) pending() bool {
	return a.Status == "" || a.Status == "accepted" || a.Status == "ingesting"
}

// awaitVerdict returns the batch once it ingested or failed: from the answer
// to the send when that settled it, else from GET /exports/{batch_id}, asked
// every pace.interval for at most pace.limit. A refusal of the request ends
// the wait. A server error or a lost connection is asked again.
func (p platform) awaitVerdict(ctx context.Context, sent sentBatch, pace verdictPace) (batchAnswer, error) {
	answer := sent.answer
	wait, cancel := context.WithTimeout(ctx, pace.limit)
	defer cancel()
	for answer.pending() {
		select {
		case <-wait.Done():
			return batchAnswer{}, fmt.Errorf("batch %s has no verdict after %d s", sent.batch, int(pace.limit.Seconds()))
		case <-time.After(pace.interval):
		}
		next, err := p.batch(wait, sent.batch)
		var refused *refusal
		switch {
		case errors.As(err, &refused) && refused.status < http.StatusInternalServerError:
			return batchAnswer{}, err
		case err == nil:
			answer = next
		}
	}
	if !answer.settled() {
		return batchAnswer{}, fmt.Errorf("the answer for batch %s names no status the integration contract defines", sent.batch)
	}
	return answer, nil
}

// batch reads GET /exports/{batch_id}.
func (p platform) batch(ctx context.Context, id string) (batchAnswer, error) {
	request, err := p.request(ctx, http.MethodGet, "/exports/"+id, nil)
	if err != nil {
		return batchAnswer{}, err
	}
	_, data, err := p.do(request, "the verdict request for batch "+id)
	if err != nil {
		return batchAnswer{}, err
	}
	return readBatchAnswer(data), nil
}

// print states the verdict and returns the exit code: 0 when the batch
// ingested, 1 when it failed. A failure code outside the contract's pattern
// is not printed, and the batch still failed.
func (a batchAnswer) print(sent sentBatch, origin string, stdout, stderr io.Writer) int {
	if a.Status == "failed" {
		code := "with no failure code"
		if a.Failure != nil && refusalCodePattern.MatchString(a.Failure.Code) {
			code = a.Failure.Code
		}
		fmt.Fprintf(stderr, "costroid: batch %s failed on ingest: %s. Costroid Platform stored none of its rows\n", sent.batch, code)
		return exitRejected
	}
	rows := sent.rows
	if a.Rows != nil {
		rows = *a.Rows
	}
	fmt.Fprintf(stdout, "costroid: batch %s ingested, %d rows\n", sent.batch, rows)
	if a.DroppedTagKeys != nil && *a.DroppedTagKeys > 0 {
		fmt.Fprintf(stdout, "costroid: Costroid Platform dropped %d Tags entries outside costroid/, and stored the rows without them\n", *a.DroppedTagKeys)
	}
	for _, pull := range sent.pulls {
		fmt.Fprintf(stdout, "costroid: pull request %s of %s: %s\n", pull.number, pull.repository, pull.link(origin))
	}
	return 0
}

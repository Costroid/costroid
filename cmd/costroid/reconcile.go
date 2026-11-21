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
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reconcile"
)

const (
	anthropicKeyVariable = "ANTHROPIC_ADMIN_KEY"
	openAIKeyVariable    = "OPENAI_ADMIN_KEY"

	storedRowsMode = 0o644
)

// clock is what a reconciliation reads the time from.
type clock func() time.Time

type reconcileFlags struct {
	rows       string
	credential string
	upstream   map[string]string
	out        string
	days       reconcile.Range
	reports    string
	asOf       time.Time
	// export sends the rows written to out to Costroid Platform, beside the
	// installation the job runs under.
	export       bool
	installation string
}

// upstreamSources are the sources an upstream credential can be named for.
var upstreamSources = []string{reconcile.SourceAnthropic, reconcile.SourceOpenAI}

// upstreamFlag reads --upstream-credential SOURCE=ID, once per source.
func upstreamFlag(into map[string]string) func(string) error {
	return func(value string) error {
		source, id, found := strings.Cut(value, "=")
		if !found || id == "" {
			return fmt.Errorf("%q must read SOURCE=ID, such as anthropic=apikey_01Example", value)
		}
		if !slices.Contains(upstreamSources, source) {
			return fmt.Errorf("source %q must be one of %s", source, strings.Join(upstreamSources, ", "))
		}
		if _, named := into[source]; named {
			return fmt.Errorf("%s is named twice: a gateway holds one key per provider", source)
		}
		into[source] = id
		return nil
	}
}

func parseReconcile(args []string, stdout io.Writer) (reconcileFlags, error) {
	set := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	rows := set.String("rows", "", "the FOCUS rows, as JSON lines")
	from := set.String("from", "", "the first UTC day, YYYY-MM-DD")
	to := set.String("to", "", "the last UTC day, YYYY-MM-DD")
	credential := set.String("credential", "", "the credential of rows whose SubAccountId is null")
	upstream := map[string]string{}
	set.Func("upstream-credential", "SOURCE=ID: the provider key a gateway holds, which rows drawn on its run keys are compared under", upstreamFlag(upstream))
	out := set.String("out", "", "the file reconciliation rows are kept in, one per source, credential and day")
	reports := set.String("reports", "", "a directory of saved provider reports, read in place of the provider")
	asOf := set.String("as-of", "", "when the saved reports were saved, as a UTC instant")
	send := set.Bool("export", false, "send the rows --out keeps to Costroid Platform, by integration/v0.6 or v0.5")
	installation := set.String("installation-id", "", "with --export, the installation the job runs under")
	if err := parseFlags(set, args, reconcileHelp, stdout); err != nil {
		return reconcileFlags{}, err
	}
	if *rows == "" || *from == "" || *to == "" || set.NArg() != 0 {
		return reconcileFlags{}, errors.New("reconcile needs --rows, --from and --to")
	}
	days, err := reconcile.ParseRange(*from, *to)
	if err != nil {
		return reconcileFlags{}, err
	}
	flags := reconcileFlags{rows: *rows, credential: *credential, upstream: upstream, out: *out, days: days, reports: *reports, export: *send}
	if (*reports == "") != (*asOf == "") {
		return reconcileFlags{}, errors.New("--reports and --as-of go together")
	}
	if *asOf != "" {
		if flags.asOf, err = time.Parse(focus.InstantLayout, *asOf); err != nil {
			return reconcileFlags{}, errors.New("--as-of must read like 2026-11-04T00:00:00Z")
		}
	}
	switch {
	case flags.export && flags.out == "":
		return reconcileFlags{}, errors.New("--export sends the rows --out keeps, so it needs --out")
	case *installation != "" && !flags.export:
		return reconcileFlags{}, errors.New("--installation-id names what --export sends beside the rows, so it goes with --export")
	case *installation != "":
		if flags.installation, err = exportInstallation(*installation); err != nil {
			return reconcileFlags{}, err
		}
	}
	return flags, nil
}

// fromSavedReports swaps the provider for a directory. A saved report is read
// as of the instant it was saved, because that is what decides which days it
// could have finished.
func fromSavedReports(flags reconcileFlags, getenv func(string) string, client reconcile.Doer, now clock) (func(string) string, reconcile.Doer, clock) {
	if flags.reports == "" {
		return getenv, client, now
	}
	noKey := func(string) string { return savedReportsHoldNoKey }
	return noKey, &savedReports{directory: flags.reports}, func() time.Time { return flags.asOf }
}

// reconcileCommand reaches Costroid Platform, for --export, with a client that
// follows no redirect, as export's does, so the token and the rows reach only
// the origin named.
func reconcileCommand(ctx context.Context, args []string, getenv func(string) string, client reconcile.Doer, now clock, stdout, stderr io.Writer) int {
	platformClient := &http.Client{
		Timeout:       platformTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return reconcileThrough(ctx, args, reconcileWorld{getenv: getenv, client: client, now: now, platform: platformClient, pace: ingestPace}, stdout, stderr)
}

// reconcileWorld is what a reconciliation reads and reaches: the environment,
// the providers' client and clock, and for --export the platform's client and
// how often a body is sent again after a server error, and for how long.
type reconcileWorld struct {
	getenv   func(string) string
	client   reconcile.Doer
	now      clock
	platform *http.Client
	pace     verdictPace
}

func reconcileThrough(ctx context.Context, args []string, world reconcileWorld, stdout, stderr io.Writer) int {
	flags, err := parseReconcile(args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return cli.Fail(stderr, cli.Mistaken("reconcile", err))
	}
	var sender *reconcileSender
	if flags.export {
		if sender, err = newReconcileSender(flags, world); err != nil {
			return cli.Fail(stderr, err)
		}
	}
	result, err := reconcileRows(ctx, flags, world.getenv, world.client, world.now, stderr)
	if err != nil {
		return cli.Fail(stderr, err)
	}
	if err := reconcile.Render(stdout, result); err != nil {
		return cli.Fail(stderr, err)
	}
	code := 0
	if result.Misses() > 0 {
		code = exitRejected
	}
	for _, refused := range result.Refused {
		fmt.Fprintf(stderr, "costroid: %v\n", refused)
		code = exitFailure
	}
	if sender != nil && !sender.send(ctx, result.Rows, stdout, stderr) {
		code = exitFailure
	}
	return code
}

func reconcileRows(ctx context.Context, flags reconcileFlags, getenv func(string) string, client reconcile.Doer, now clock, stderr io.Writer) (reconcile.Result, error) {
	getenv, client, now = fromSavedReports(flags, getenv, client, now)
	ledger, err := readLedger(flags)
	if err != nil {
		return reconcile.Result{}, err
	}
	if ledger.Uncredentialed > 0 {
		return reconcile.Result{}, fmt.Errorf("%d rows have a null SubAccountId: pass --credential to name their credential", ledger.Uncredentialed)
	}
	for _, source := range upstreamSources {
		if count := ledger.Gatewayed[source]; count > 0 {
			return reconcile.Result{}, fmt.Errorf("%d %s rows were drawn on a gateway's run keys, which the provider's report never names: pass --upstream-credential %s=ID with the id of the key the gateway sends with", count, source, source)
		}
	}
	if ledger.Repeats > 0 {
		fmt.Fprintf(stderr, "costroid: warning: %d repeated rows in %s were counted once\n", ledger.Repeats, flags.rows)
	}
	providers, err := providersFor(ledger, getenv, client)
	if err != nil {
		return reconcile.Result{}, err
	}
	pinned, err := catalog.Load()
	if err != nil {
		return reconcile.Result{}, err
	}
	result, err := reconcile.Reconcile(ctx, ledger, reconcile.Options{
		Range:     flags.days,
		Now:       now().UTC(),
		Catalog:   pinned,
		Providers: providers,
	})
	if err != nil {
		return reconcile.Result{}, err
	}
	if flags.out != "" {
		if err := storeRows(flags.out, result.Rows); err != nil {
			return reconcile.Result{}, err
		}
	}
	return result, nil
}

func readLedger(flags reconcileFlags) (*reconcile.Ledger, error) {
	rows, err := os.Open(flags.rows)
	if err != nil {
		return nil, err
	}
	ledger, err := reconcile.ReadLedgerWith(rows, flags.days, reconcile.LedgerOptions{Credential: flags.credential, Upstream: flags.upstream})
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.rows, closeErr)
	}
	return ledger, err
}

// providersFor reads each admin key from this process's own environment. A key
// is asked for only when the ledger holds that provider's rows.
func providersFor(ledger *reconcile.Ledger, getenv func(string) string, client reconcile.Doer) ([]reconcile.Provider, error) {
	constructors := map[string]struct {
		variable string
		build    func(reconcile.Doer, string) reconcile.Provider
	}{
		reconcile.SourceAnthropic: {anthropicKeyVariable, reconcile.NewAnthropic},
		reconcile.SourceOpenAI:    {openAIKeyVariable, reconcile.NewOpenAI},
	}
	var providers []reconcile.Provider
	for _, source := range ledger.Sources() {
		constructor := constructors[source]
		key := getenv(constructor.variable)
		if key == "" {
			return nil, fmt.Errorf("%s is not set: the %s report needs an admin key", constructor.variable, source)
		}
		providers = append(providers, constructor.build(client, key))
	}
	return providers, nil
}

// storeRows replaces the file in one rename, so a failed run leaves the last
// good rows in place.
func storeRows(path string, rows []reconcile.Row) error {
	var current io.Reader = strings.NewReader("")
	existing, err := os.Open(path)
	switch {
	case err == nil:
		defer existing.Close()
		current = existing
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := reconcile.Merge(current, rows, temporary); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Chmod(storedRowsMode); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", temporary.Name(), err)
	}
	return os.Rename(temporary.Name(), path)
}

// Version 0.5 is the first version of the integration contract that carries
// merge facts and reconciliation rows. Version 0.6 is 0.5 with a merge fact
// read as token_verified only from a protected branch, the stored fact's
// branch in its answer, and a repository's name compared in lower case.
var (
	contractV06 = contract{Version: "0.6", TagSet: "tags/v0.4", FocusRow: "focus-row/v0.5"}
	contractV05 = contract{Version: "0.5", TagSet: "tags/v0.4", FocusRow: "focus-row/v0.5"}
	// statementContracts are the versions merged and reconcile --export send
	// by, newest first. The last is the least they send by.
	statementContracts = []contract{contractV06, contractV05}
)

// statedOf reads GET /identity under each of versions, newest first, by the
// rule statedContract keeps for export's: the first whose answer states that
// version is kept. It sets p.base to the kept version's path and returns the
// token's tenant. When none is kept, the error is the newest one that is not
// a 404, so a platform that serves only the newest version and refuses the
// token is not said to serve none, or else the last one's.
func (p *platform) statedOf(ctx context.Context, versions []contract) (contract, string, error) {
	var err, served error
	for _, version := range versions {
		var tenant string
		if tenant, err = p.identityUnder(ctx, version); err == nil {
			p.base = p.origin + version.path()
			return version, tenant, nil
		}
		var refused *refusal
		if served == nil && !(errors.As(err, &refused) && refused.status == http.StatusNotFound) {
			served = err
		}
	}
	if served != nil {
		return contract{}, "", served
	}
	return contract{}, "", err
}

// The limits of GET /quotas a reconciliation body is cut at.
const (
	quotaLines     = "max_reconciliation_lines"
	quotaBytes     = "max_reconciliation_bytes"
	quotaLineBytes = "max_reconciliation_line_bytes"
)

// reconcileSender sends the rows a reconciliation wrote to the Costroid
// Platform COSTROID_PLATFORM_URL names, under the integration token in
// COSTROID_PLATFORM_TOKEN, as export sends a batch. No provider's admin key
// is read here.
type reconcileSender struct {
	target       platform
	getenv       func(string) string
	installation string
	out          string
	pace         verdictPace
}

// newReconcileSender reads where and how to send before any provider is
// asked, so a send that cannot happen costs no report.
func newReconcileSender(flags reconcileFlags, world reconcileWorld) (*reconcileSender, error) {
	address := world.getenv(platformVariable)
	if address == "" {
		return nil, errors.New("--export sends to the Costroid Platform COSTROID_PLATFORM_URL names, and it is not set")
	}
	origin, err := platformOrigin(address)
	if err != nil {
		return nil, errors.New("COSTROID_PLATFORM_URL must be an https origin, such as https://app.costroid.com, or http on a loopback address")
	}
	token := world.getenv(platformTokenVariable)
	if !integrationTokenPattern.MatchString(token) {
		return nil, errors.New("COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform")
	}
	sender := &reconcileSender{
		target:       platform{client: world.platform, origin: origin, token: token},
		getenv:       world.getenv,
		installation: flags.installation,
		out:          flags.out,
		pace:         world.pace,
	}
	if sender.mintsRunToken() && sender.installation == "" {
		return nil, errors.New("this job has id-token: write, so its run token goes with the rows, and the platform checks it against an installation: pass --installation-id")
	}
	return sender, nil
}

// mintsRunToken reports a GitHub Actions job that can mint a run token. Its
// token names the run that sent the rows; any other job sends none.
func (s *reconcileSender) mintsRunToken() bool {
	return s.getenv("GITHUB_ACTIONS") == "true" && s.getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "" && s.getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") != ""
}

// receipt is the platform's answer to a body it took: what its rows did.
type receipt struct {
	Stored    *int `json:"stored"`
	Unchanged *int `json:"unchanged"`
	Ignored   *int `json:"ignored"`
}

// sentRows totals the bodies the platform took.
type sentRows struct {
	bodies, rows               int
	stored, unchanged, ignored int
}

func (s sentRows) add(body reconcile.Body, answer receipt) sentRows {
	s.bodies++
	s.rows += len(body.Keys)
	s.stored += *answer.Stored
	s.unchanged += *answer.Unchanged
	s.ignored += *answer.Ignored
	return s
}

func (s sentRows) print(of int, stdout io.Writer) {
	if s.bodies == 0 {
		return
	}
	bodies := fmt.Sprintf("%d of %d bodies", s.bodies, of)
	switch {
	case s.bodies == of && of == 1:
		bodies = "1 body"
	case s.bodies == of:
		bodies = fmt.Sprintf("%d bodies", of)
	}
	fmt.Fprintf(stdout, "costroid: Costroid Platform took %d %s in %s: %d stored, %d unchanged, %d ignored\n", s.rows, plural("row", s.rows), bodies, s.stored, s.unchanged, s.ignored)
}

// send sends the rows in the bodies GET /quotas allows, and reports whether
// nothing failed. An answer that keeps the rows to send later warns and is no
// failure, as on export.
func (s *reconcileSender) send(ctx context.Context, rows []reconcile.Row, stdout, stderr io.Writer) bool {
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "costroid: reconcile wrote no rows, so nothing was sent to Costroid Platform")
		return true
	}
	bodies, runToken, err := s.prepare(ctx, rows, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return false
	}
	var sent sentRows
	for i, body := range bodies {
		answer, err := s.put(ctx, body, runToken, fmt.Sprintf("body %d of %d", i+1, len(bodies)))
		if err != nil {
			sent.print(len(bodies), stdout)
			return s.refused(err, body, stderr)
		}
		sent = sent.add(body, answer)
	}
	sent.print(len(bodies), stdout)
	return true
}

// prepare reads the contract, the limits and the run token, and cuts the
// bodies, before anything is sent.
func (s *reconcileSender) prepare(ctx context.Context, rows []reconcile.Row, stderr io.Writer) ([]reconcile.Body, string, error) {
	tenant, err := s.contract(ctx, stderr)
	if err != nil {
		return nil, "", err
	}
	limits, err := s.limits(ctx)
	if err != nil {
		return nil, "", err
	}
	bodies, err := reconcile.Bodies(rows, limits)
	if err != nil {
		return nil, "", err
	}
	if !s.mintsRunToken() {
		return bodies, "", nil
	}
	runToken, err := s.target.runToken(ctx, s.getenv, tenant)
	return bodies, runToken, err
}

// contract reads GET /identity under integration/v0.6, then v0.5, and returns
// the token's tenant. A platform that states neither is asked for the version
// it does state, which then names why nothing was sent.
func (s *reconcileSender) contract(ctx context.Context, stderr io.Writer) (string, error) {
	stated, tenant, err := s.target.statedOf(ctx, statementContracts)
	if err == nil {
		fmt.Fprintf(stderr, "costroid: Costroid Platform states integration/v%s, so reconcile sends the rows by it\n", stated.Version)
		return tenant, nil
	}
	var refused *refusal
	if errors.As(err, &refused) && refused.unauthorized() {
		return "", err
	}
	earlier := s.target
	if latest, _, statedErr := earlier.statedContract(ctx); statedErr == nil {
		return "", fmt.Errorf("the latest version Costroid Platform states is integration/v%s, and reconcile --export sends by integration/v0.5 or later, the first that carries reconciliation rows, so nothing was sent", latest.Version)
	}
	return "", err
}

// limits reads the three bounds of a body from GET /quotas. A bound the answer
// does not state is never guessed.
func (s *reconcileSender) limits(ctx context.Context) (reconcile.Limits, error) {
	request, err := s.target.request(ctx, http.MethodGet, "/quotas", nil)
	if err != nil {
		return reconcile.Limits{}, err
	}
	_, data, err := s.target.do(request, "the quota request")
	if err != nil {
		return reconcile.Limits{}, err
	}
	var answer struct {
		Quotas []struct {
			Name  string `json:"name"`
			Limit *int64 `json:"limit"`
		} `json:"quotas"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return reconcile.Limits{}, errors.New("the quota answer of Costroid Platform is not a quota report, so no body can be cut to its limits, and nothing was sent")
	}
	read := func(name string) (*int64, error) {
		for _, quota := range answer.Quotas {
			if quota.Name != name {
				continue
			}
			if quota.Limit != nil && *quota.Limit < 1 {
				return nil, fmt.Errorf("GET /quotas states %s as %d, so no body can hold a row, and nothing was sent", name, *quota.Limit)
			}
			return quota.Limit, nil
		}
		return nil, fmt.Errorf("GET /quotas states no %s, so no body can be cut to it, and nothing was sent", name)
	}
	var limits reconcile.Limits
	for _, bound := range []struct {
		name string
		into **int64
	}{{quotaLines, &limits.Lines}, {quotaBytes, &limits.Bytes}, {quotaLineBytes, &limits.LineBytes}} {
		if *bound.into, err = read(bound.name); err != nil {
			return reconcile.Limits{}, err
		}
	}
	return limits, nil
}

// bodyRefusal is a refused body, and the line the platform named, from 1, or
// 0 when it named none.
type bodyRefusal struct {
	*refusal
	line int
}

// put sends one body, and sends the same bytes again on a server error or a
// lost connection, every pace.interval for at most pace.limit, as export asks
// for a verdict. The replace rule makes a body sent twice store once.
func (s *reconcileSender) put(ctx context.Context, body reconcile.Body, runToken, what string) (receipt, error) {
	deadline := time.Now().Add(s.pace.limit)
	for {
		answer, again, err := s.putOnce(ctx, body, runToken, what)
		if !again {
			return answer, err
		}
		if time.Now().Add(s.pace.interval).After(deadline) {
			return receipt{}, fmt.Errorf("%w, sent again every %d s for %d s", err, int(s.pace.interval.Seconds()), int(s.pace.limit.Seconds()))
		}
		select {
		case <-ctx.Done():
			return receipt{}, err
		case <-time.After(s.pace.interval):
		}
	}
}

// putOnce sends a body once. It reports whether to send it again: on a server
// error or a lost connection. Only a refusal's code and line are read, so
// nothing else the platform says reaches the log.
func (s *reconcileSender) putOnce(ctx context.Context, body reconcile.Body, runToken, what string) (receipt, bool, error) {
	request, err := s.target.request(ctx, http.MethodPut, "/reconciliations", bytes.NewReader(body.Data))
	if err != nil {
		return receipt{}, false, err
	}
	request.Header.Set("Content-Type", "application/x-ndjson")
	if s.installation != "" {
		request.Header.Set("Costroid-Installation-Id", s.installation)
	}
	if runToken != "" {
		request.Header.Set("Costroid-Run-Token", runToken)
	}
	response, err := s.target.client.Do(request)
	if err != nil {
		return receipt{}, true, fmt.Errorf("send %s to Costroid Platform: %w", what, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPlatformAnswer))
	if err != nil {
		return receipt{}, true, fmt.Errorf("read Costroid Platform's answer to %s: %w", what, err)
	}
	if response.StatusCode == http.StatusOK {
		var answer receipt
		if json.Unmarshal(data, &answer) != nil || !answer.counted() {
			return receipt{}, false, fmt.Errorf("the answer to %s names no stored, unchanged and ignored counts, though Costroid Platform took it", what)
		}
		return answer, false, nil
	}
	refused := bodyRefusal{refusal: &refusal{what: what, status: response.StatusCode, retryAfter: response.Header.Get("Retry-After")}}
	var answer struct {
		Error struct {
			Code string `json:"code"`
			Line int    `json:"line"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &answer) == nil && refusalCodePattern.MatchString(answer.Error.Code) {
		refused.code = answer.Error.Code
		if answer.Error.Line >= 1 && answer.Error.Line <= len(body.Keys) {
			refused.line = answer.Error.Line
		}
	}
	return receipt{}, response.StatusCode >= http.StatusInternalServerError, &refused
}

func (r receipt) counted() bool {
	return r.Stored != nil && r.Unchanged != nil && r.Ignored != nil && *r.Stored >= 0 && *r.Unchanged >= 0 && *r.Ignored >= 0
}

// refused states a body the platform did not take, and reports whether that
// is no failure: the plan, a quota or a missing binding kept the rows from
// it, and nothing in them is wrong.
func (s *reconcileSender) refused(err error, body reconcile.Body, stderr io.Writer) bool {
	var refused *bodyRefusal
	switch {
	case !errors.As(err, &refused):
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return false
	case refused.later():
		retry := ""
		if runNumberPattern.MatchString(refused.retryAfter) {
			retry = ", retry after " + refused.retryAfter + " s"
		}
		fmt.Fprintf(stderr, "costroid: warning: %v%s. Nothing in the rows is wrong, and %s is kept to send again\n", err, retry, s.out)
		return true
	case refused.unbound():
		fmt.Fprintf(stderr, "costroid: warning: %v. The tenant holds no binding of %s: run costroid connect, as docs/connect.md shows, and %s is kept to send again\n", err, s.installation, s.out)
		return true
	case refused.status == http.StatusRequestEntityTooLarge:
		fmt.Fprintf(stderr, "costroid: %v. The body was cut at the limits GET /quotas stated, which can only rise, and nothing in it was stored. %s is kept to send again\n", err, s.out)
	case refused.status == http.StatusUnprocessableEntity && refused.line > 0:
		key := body.Keys[refused.line-1]
		fmt.Fprintf(stderr, "costroid: %v at line %d, the row of %s %s %s. Nothing in the body was stored, and %s is kept\n", err, refused.line, key.Source, key.Credential, key.Day, s.out)
	case refused.status >= http.StatusInternalServerError:
		fmt.Fprintf(stderr, "costroid: %v. Nothing in the rows is wrong, and %s is kept to send again\n", err, s.out)
	default:
		fmt.Fprintf(stderr, "costroid: %v\n", err)
	}
	return false
}

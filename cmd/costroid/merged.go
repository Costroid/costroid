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
	"slices"
	"strconv"
	"time"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/vcs"
)

const (
	mergeFactConflict = "MERGE_FACT_CONFLICT"
	// maxPushCommits bounds the commits merged asks GitHub about, one request
	// each. A push past it is asked about at its tip alone.
	maxPushCommits = 100
	// mergedInstant is merged_at's form: UTC, to the second.
	mergedInstant = "2006-01-02T15:04:05Z"
)

// mergeRefusals are the meanings integration/v0.5 gives the refusals of a
// merge fact that merged prints no other sentence for.
var mergeRefusals = map[string]string{
	"AUTH_TOKEN_INVALID":           "The token is absent, unknown, expired or revoked",
	"AUTH_TOKEN_SCOPE":             "The token lacks merges:write",
	"RUN_TOKEN_INVALID":            "The run token fails verification",
	"RUN_TOKEN_OWNER_MISMATCH":     "The run token names another owner than the installation",
	"MERGE_FACT_IDENTITY_MISMATCH": "A verified run token names another repository than repo",
	"MERGE_FACT_INVALID":           "A key is missing, unknown or of the wrong form",
}

const mergedHelp = `Usage:
  costroid merged --platform-url URL --installation-id ID [--json]
  costroid merged --dry-run

Example, in a job on a push to the default branch:

  costroid merged --platform-url https://app.costroid.com \
    --installation-id github-owner:7204411

merged sends Costroid Platform one merge fact for each pull request a push
closed: this pull request of this repository merged at this instant as this
commit. It runs on GitHub Actions, on a push. It asks GitHub, under
GITHUB_TOKEN, which merged pull requests each commit the push event lists
closed, and sends nothing when they closed none. On any other event it sends
nothing and says so.

It sends by integration/v0.5, under the integration token in
COSTROID_PLATFORM_TOKEN, and only to a platform that serves that version.
--platform-url and --installation-id are read as export reads them. The token
needs identity:read and merges:write. The merge id is the repository id and
the pull request's number, joined by a hyphen, so the same fact sent again
changes nothing. The job's OIDC token rides with each fact, as with a batch,
and needs id-token: write on the job. The platform stores a fact as
token_verified when the run token names the fact's commit, which is the tip of
the push. A fact sent without a run token, or a fact of a merge queue's group
whose commit is not the tip, is stored as unverified, and merged says why.

A server error or a lost connection is sent again every 2 s for up to 60 s.
On 402 or 409 INSTALLATION_NOT_BOUND nothing in the fact is wrong, and
nothing keeps it: merged warns and exits 0, and the job sends it when it runs
again. On 409 MERGE_FACT_CONFLICT the pull request already holds a different
fact, and merged does not send it again. docs/export.md lists every answer.

--json prints, on standard output, one object per fact sent: its merge id,
its pull request, the status the platform answered and the fact's
run_identity, or the refusal's code. --dry-run prints each fact's merge id and
document instead, and reaches nothing but GitHub.

On GitLab CI merged sends nothing: it reads the pull requests a commit closed
from GitHub alone.

Exit codes of merged:
  0                 every fact was stored or already held, or there was
                    nothing to send, or the platform answered 402 or 409
                    INSTALLATION_NOT_BOUND
  1                 the platform refused a fact, and the line names its code
  125               costroid could not read the flags, the run or GitHub's
                    answer, or reach Costroid Platform, or the platform does
                    not serve integration/v0.5
`

type mergedFlags struct {
	origin, installation string
	json, dryRun         bool
}

// mergeDocument is the body of PUT /merges/{merge_id}, in the contract's key
// order.
type mergeDocument struct {
	Repo           string `json:"repo"`
	PullRequest    int    `json:"pull_request"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
}

type mergeFact struct {
	id       string
	document mergeDocument
}

// pushRun is the push a merged job runs on: the repository, and the commits
// its event lists, oldest first, the tip last.
type pushRun struct {
	repo, repositoryID, tip string
	commits                 []string
}

// mergedSend is what merged knows when it sends: where, with which run token,
// and the push the token names.
type mergedSend struct {
	target       platform
	installation string
	runToken     string
	tip          string
	pace         verdictPace
	json         bool
	// say takes the lines about each fact: standard output, or standard
	// error under --json.
	say, stdout, stderr io.Writer
}

// mergedCommand sends with a client that follows no redirect, as export's does.
func mergedCommand(ctx context.Context, args []string, std stdio) int {
	client := &http.Client{
		Timeout:       platformTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return merged(ctx, args, client, os.Getenv, ingestPace, std.stdout, std.stderr)
}

// merged reads the push and GitHub's answer before it reaches the platform,
// so a push that closed nothing needs no token. The send is asked again at
// export's verdict pace.
func merged(ctx context.Context, args []string, client *http.Client, getenv func(string) string, pace verdictPace, stdout, stderr io.Writer) int {
	flags, err := parseMerged(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	push, skip, err := readPush(getenv, stderr)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	case skip != "":
		fmt.Fprintf(stderr, "costroid: %s, so nothing was sent\n", skip)
		return 0
	}
	facts, err := closedBy(ctx, push, client, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	say := stdout
	if flags.json {
		say = stderr
	}
	if len(facts) == 0 {
		fmt.Fprintf(say, "costroid: the push to %s closed no pull request, so no merge fact was sent\n", push.tip)
		return 0
	}
	if flags.dryRun {
		return printFacts(facts, stdout, stderr)
	}
	send := mergedSend{installation: flags.installation, tip: push.tip, pace: pace, json: flags.json, say: say, stdout: stdout, stderr: stderr}
	send.target = platform{client: client, origin: flags.origin, token: getenv(platformTokenVariable)}
	if code := send.open(ctx, getenv); code != 0 {
		return code
	}
	return send.all(ctx, facts)
}

func parseMerged(args []string, stdout, stderr io.Writer) (mergedFlags, error) {
	set := flag.NewFlagSet("merged", flag.ContinueOnError)
	set.SetOutput(stderr)
	address := set.String("platform-url", "", "Costroid Platform's https origin")
	installation := set.String("installation-id", "", "the installation the facts come through")
	asJSON := set.Bool("json", false, "print one object per fact sent")
	dryRun := set.Bool("dry-run", false, "print the documents and send nothing")
	if err := parseFlags(set, args, mergedHelp, stdout, stderr); err != nil {
		return mergedFlags{}, err
	}
	if set.NArg() > 0 {
		return mergedFlags{}, errors.New("merged takes no argument")
	}
	flags := mergedFlags{json: *asJSON, dryRun: *dryRun}
	if *dryRun && *address == "" && *installation == "" {
		return flags, nil
	}
	if *address == "" || *installation == "" {
		return mergedFlags{}, errors.New("merged needs --platform-url and --installation-id, or --dry-run")
	}
	var err error
	if flags.installation, err = exportInstallation(*installation); err != nil {
		return mergedFlags{}, err
	}
	if flags.origin, err = platformOrigin(*address); err != nil {
		return mergedFlags{}, err
	}
	return flags, nil
}

// readPush reads the push from GitHub Actions. A run that is not on a push
// returns the reason it sends nothing.
func readPush(getenv func(string) string, stderr io.Writer) (pushRun, string, error) {
	switch cicontext.Detect(getenv) {
	case cicontext.GitLabCI:
		return pushRun{}, "", errors.New("merged reads the pull requests a commit closed from GitHub alone, so on GitLab CI it sends nothing")
	case cicontext.NoSystem:
		return pushRun{}, "", errors.New("merged runs in a GitHub Actions job on a push, and this is no GitHub Actions run")
	}
	if event := getenv("GITHUB_EVENT_NAME"); event != "push" {
		return pushRun{}, fmt.Sprintf("merged sends the merge facts of a push, and this run is on %s", event), nil
	}
	run, _ := cicontext.Read(getenv)
	push := pushRun{repo: run.Repository, repositoryID: getenv("GITHUB_REPOSITORY_ID"), tip: getenv("GITHUB_SHA")}
	switch {
	case !vcs.IsCommit(push.tip):
		return pushRun{}, "", fmt.Errorf("GITHUB_SHA is %q, not a full commit, so no pushed commit can be read", push.tip)
	case push.repo == "":
		return pushRun{}, "", errors.New("GITHUB_REPOSITORY names no repository that costroid/repo can hold")
	case !runNumberPattern.MatchString(push.repositoryID):
		return pushRun{}, "", fmt.Errorf("GITHUB_REPOSITORY_ID is %q, not a number, so no merge id can be derived", push.repositoryID)
	}
	push.commits = pushedCommits(getenv("GITHUB_EVENT_PATH"), push.tip, stderr)
	return push, "", nil
}

// pushedCommits are the commits the push event lists, oldest first, with the
// tip last. Each is asked about, because a merge queue's group puts more than
// one pull request's commit on the branch in one push, and a commit the event
// marks as pushed before, as a group's commits were to the queue's branch, can
// still be the one a pull request merged as.
func pushedCommits(path, tip string, stderr io.Writer) []string {
	var event struct {
		Commits []struct {
			ID string `json:"id"`
		} `json:"commits"`
	}
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &event)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the event file could not be read (%v), so merged asks about the pushed commit alone\n", err)
		return []string{tip}
	}
	var commits []string
	for _, commit := range event.Commits {
		if commit.ID != tip && vcs.IsCommit(commit.ID) && !slices.Contains(commits, commit.ID) {
			commits = append(commits, commit.ID)
		}
	}
	if len(commits) >= maxPushCommits {
		fmt.Fprintf(stderr, "costroid: warning: the push lists %d commits, past the %d merged asks about, so it asks about the pushed commit alone\n", len(commits)+1, maxPushCommits)
		return []string{tip}
	}
	return append(commits, tip)
}

// closedBy asks GitHub which merged pull requests each commit of the push
// closed, and builds one fact for each. GitHub's client runs in the
// customer's CI, outside the service's egress allowlist, under the job's token.
func closedBy(ctx context.Context, push pushRun, client *http.Client, getenv func(string) string) ([]mergeFact, error) {
	token := getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN is not set: pass the job's token, which needs pull-requests: read, in GITHUB_TOKEN")
	}
	host, err := scm.NewGitHub(scm.GitHubOptions{
		API:        getenv("GITHUB_API_URL"),
		Repository: getenv("GITHUB_REPOSITORY"),
		Tokens:     scm.StaticToken(token),
		Client:     client,
	})
	if err != nil {
		return nil, err
	}
	var facts []mergeFact
	for _, commit := range push.commits {
		pulls, err := host.MergedPullsAt(ctx, commit)
		if err != nil {
			return nil, fmt.Errorf("ask GitHub which pull requests commit %s closed: %w", commit, err)
		}
		for _, pull := range pulls {
			id := push.repositoryID + "-" + strconv.Itoa(pull.Number)
			if slices.ContainsFunc(facts, func(fact mergeFact) bool { return fact.id == id }) {
				continue
			}
			facts = append(facts, mergeFact{id: id, document: mergeDocument{
				Repo:           push.repo,
				PullRequest:    pull.Number,
				MergedAt:       pull.MergedAt.UTC().Format(mergedInstant),
				MergeCommitSHA: pull.MergeCommitSHA,
			}})
		}
	}
	return facts, nil
}

// printFacts prints, for --dry-run, each fact's merge id and the document
// merged would send. A document of strings and a number always encodes.
func printFacts(facts []mergeFact, stdout, stderr io.Writer) int {
	for _, fact := range facts {
		line, _ := json.Marshal(struct {
			MergeID  string        `json:"merge_id"`
			Document mergeDocument `json:"document"`
		}{fact.id, fact.document})
		fmt.Fprintf(stdout, "%s\n", line)
	}
	fmt.Fprintf(stderr, "costroid: --dry-run printed %d merge facts and sent none\n", len(facts))
	return 0
}

// open checks the integration token, reads the platform's contract and mints
// the run token, and returns the exit code of a failure, or 0. A job without
// id-token: write sends its facts without a run token, and each is stored as
// unverified.
func (s *mergedSend) open(ctx context.Context, getenv func(string) string) int {
	if !integrationTokenPattern.MatchString(s.target.token) {
		fmt.Fprintf(s.stderr, "costroid: COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform\n")
		return exitFailure
	}
	tenant, err := s.target.identityUnder(ctx, contractV05)
	var refused *refusal
	switch {
	case errors.As(err, &refused) && refused.status == http.StatusNotFound:
		fmt.Fprintf(s.stderr, "costroid: Costroid Platform does not serve integration/v%s, and a merge fact needs integration/v%s or later, so nothing was sent\n", contractV05.Version, contractV05.Version)
		return exitFailure
	case errors.As(err, &refused):
		fmt.Fprintf(s.stderr, "costroid: %v\n", err)
		return exitRejected
	case err != nil:
		fmt.Fprintf(s.stderr, "costroid: %v\n", err)
		return exitFailure
	}
	s.target.base = s.target.origin + contractV05.path()
	if getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" || getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "" {
		return 0
	}
	if s.runToken, err = s.target.runToken(ctx, getenv, tenant); err != nil {
		fmt.Fprintf(s.stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

// all sends each fact and returns the exit code of the worst answer. A
// failure to reach the platform stops the send.
func (s *mergedSend) all(ctx context.Context, facts []mergeFact) int {
	worst := 0
	for _, fact := range facts {
		code := s.one(ctx, fact)
		if code == exitFailure {
			return code
		}
		worst = max(worst, code)
	}
	return worst
}

// mergedAnswer is the object --json prints for one fact.
type mergedAnswer struct {
	MergeID     string `json:"merge_id"`
	PullRequest int    `json:"pull_request"`
	Status      int    `json:"status"`
	RunIdentity string `json:"run_identity,omitempty"`
	Code        string `json:"code,omitempty"`
}

// one sends a fact and states the answer.
func (s *mergedSend) one(ctx context.Context, fact mergeFact) int {
	status, data, err := s.put(ctx, fact)
	var refused *refusal
	if errors.As(err, &refused) {
		s.printJSON(mergedAnswer{MergeID: fact.id, PullRequest: fact.document.PullRequest, Status: refused.status, Code: refused.code})
		return s.refused(fact, refused)
	}
	if err != nil {
		fmt.Fprintf(s.stderr, "costroid: %v\n", err)
		return exitFailure
	}
	var stored struct {
		RunIdentity string `json:"run_identity"`
	}
	if json.Unmarshal(data, &stored) != nil || stored.RunIdentity != tokenVerified && stored.RunIdentity != unverified {
		stored.RunIdentity = ""
	}
	s.printJSON(mergedAnswer{MergeID: fact.id, PullRequest: fact.document.PullRequest, Status: status, RunIdentity: stored.RunIdentity})
	s.printStored(fact, status, stored.RunIdentity)
	return 0
}

func (s *mergedSend) printJSON(answer mergedAnswer) {
	if !s.json {
		return
	}
	line, _ := json.Marshal(answer)
	fmt.Fprintf(s.stdout, "%s\n", line)
}

// printStored states the stored fact's grade and, for an unverified fact,
// the cause the contract gives, where it is one of this send's.
func (s *mergedSend) printStored(fact mergeFact, status int, grade string) {
	pull := fmt.Sprintf("merge %s, pull request %d of %s", fact.id, fact.document.PullRequest, fact.document.Repo)
	switch {
	case grade == "":
		fmt.Fprintf(s.stderr, "costroid: warning: Costroid Platform stored %s, and its answer names no run_identity the integration contract defines\n", pull)
		return
	case status == http.StatusOK:
		fmt.Fprintf(s.say, "costroid: %s: already held as %s, so nothing changed\n", pull, grade)
	default:
		fmt.Fprintf(s.say, "costroid: %s: stored as %s\n", pull, grade)
	}
	switch {
	case grade != unverified:
	case s.runToken == "":
		fmt.Fprintf(s.say, "costroid: merge %s reads unverified because no run token went with it: give the job id-token: write, so the run token names the commit\n", fact.id)
	case fact.document.MergeCommitSHA != s.tip:
		fmt.Fprintf(s.say, "costroid: merge %s reads unverified because its commit %s is not the tip of the push, %s, which the run token names, as in a merge queue's group\n", fact.id, fact.document.MergeCommitSHA, s.tip)
	}
}

// refused states a refusal and returns its exit code.
func (s *mergedSend) refused(fact mergeFact, refused *refusal) int {
	switch meaning, known := mergeRefusals[refused.code]; {
	case refused.status == http.StatusPaymentRequired:
		fmt.Fprintf(s.stderr, "costroid: warning: %v. Nothing in the merge fact is wrong, and nothing keeps it: run this job again once the plan allows writes\n", refused)
		return 0
	case refused.unbound():
		fmt.Fprintf(s.stderr, "costroid: warning: %v. The tenant holds no binding of %s: run costroid connect, as docs/connect.md shows, then run this job again\n", refused, s.installation)
		return 0
	case refused.status == http.StatusConflict && refused.code == mergeFactConflict:
		fmt.Fprintf(s.stderr, "costroid: %v. Pull request %d of %s already holds a different fact, and a pull request merges once, so merged does not send it again\n", refused, fact.document.PullRequest, fact.document.Repo)
	case known:
		fmt.Fprintf(s.stderr, "costroid: %v. %s. Nothing was stored\n", refused, meaning)
	default:
		fmt.Fprintf(s.stderr, "costroid: %v\n", refused)
	}
	return exitRejected
}

// put sends one fact by PUT /merges/{merge_id}, and sends it again on a
// server error or a lost connection, every pace.interval for at most
// pace.limit. The contract makes the same document under the same id change
// nothing, so a repeat is safe.
func (s *mergedSend) put(ctx context.Context, fact mergeFact) (int, []byte, error) {
	body, err := json.Marshal(fact.document)
	if err != nil {
		return 0, nil, fmt.Errorf("encode merge %s: %w", fact.id, err)
	}
	wait, cancel := context.WithTimeout(ctx, s.pace.limit)
	defer cancel()
	var last error
	for {
		status, data, err := s.putOnce(wait, fact.id, body)
		var refused *refusal
		switch {
		case err == nil || errors.As(err, &refused) && refused.status < http.StatusInternalServerError:
			return status, data, err
		case wait.Err() != nil && last != nil:
			return 0, nil, last
		}
		last = err
		select {
		case <-wait.Done():
			return 0, nil, last
		case <-time.After(s.pace.interval):
		}
	}
}

func (s *mergedSend) putOnce(ctx context.Context, id string, body []byte) (int, []byte, error) {
	request, err := s.target.request(ctx, http.MethodPut, "/merges/"+id, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Costroid-Installation-Id", s.installation)
	if s.runToken != "" {
		request.Header.Set("Costroid-Run-Token", s.runToken)
	}
	response, data, err := s.target.do(request, "merge "+id)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, data, nil
}

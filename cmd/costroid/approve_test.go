package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/recipelint"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	movedCommit    = "4444444444444444444444444444444444444444"
	approver       = "reviewer"
	pullAuthor     = "author"
	approvalRecipe = "../../recipes/github-approval.yml"
)

// reviewedRepository is a stub GitHub whose pull request 7 has the reviews in
// Reviews, served as GET /pulls/7/reviews answers them.
type reviewedRepository struct {
	*scmtest.Stub
	mu      sync.Mutex
	Reviews []map[string]any
}

func (r *reviewedRepository) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/pulls/7/reviews") {
		r.Stub.ServeHTTP(w, req)
		return
	}
	if req.Header.Get("Authorization") != "Bearer "+r.Token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	listed := []map[string]any{}
	if req.URL.Query().Get("page") == "1" {
		listed = r.Reviews
	}
	if err := json.NewEncoder(w).Encode(listed); err != nil {
		panic(err)
	}
}

// review is a review as GET /pulls/{n}/reviews lists it.
func review(id int64, login, state, commit string, at time.Time) map[string]any {
	return map[string]any{
		"id": id, "user": map[string]string{"login": login, "type": "User"}, "state": state,
		"commit_id": commit, "submitted_at": at.UTC().Format(time.RFC3339), "author_association": "MEMBER",
	}
}

// approvalEnvironment is example/widgets with pull request 7 by author, whose
// head is headCommit, and a report job that posts on it. costroid.yml on the
// base branch holds an enforced budget the recorded rows go over.
func approvalEnvironment(t *testing.T) *reviewedRepository {
	t.Helper()
	stub := reportEnvironment(t, pullEvent(headCommit))
	repository := &reviewedRepository{Stub: stub}
	server := httptest.NewServer(repository)
	t.Cleanup(server.Close)
	t.Setenv("GITHUB_API_URL", server.URL)
	stub.Head, stub.Author, stub.Permission = headCommit, pullAuthor, "write"
	stub.Branches["main"] = baseCommit
	stub.Files[configFile] = enforcedOver
	return repository
}

func pullEvent(head string) string {
	return eventJSON(map[string]any{"number": 7, "head": map[string]any{"sha": head, "repo": map[string]string{"full_name": "example/widgets"}}, "base": map[string]string{"sha": baseCommit, "ref": "main"}, "user": map[string]string{"login": pullAuthor}})
}

func eventJSON(pull map[string]any, extra ...map[string]any) string {
	event := map[string]any{"pull_request": pull}
	for _, fields := range extra {
		maps.Copy(event, fields)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// setEvent writes the event file the job reads.
func setEvent(t *testing.T, name, event string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_EVENT_NAME", name)
	t.Setenv("GITHUB_EVENT_PATH", path)
}

// reportOn runs the report job on a pull request event whose head is head.
func reportOn(t *testing.T, repository *reviewedRepository, head string) scmtest.Check {
	t.Helper()
	setEvent(t, "pull_request", pullEvent(head))
	before := len(repository.Checks)
	if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || len(repository.Checks) != before+1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	return repository.Checks[len(repository.Checks)-1]
}

// reviewEventJSON is the pull_request_review event of one review on head.
func reviewEventJSON(action, login, head string) string {
	pull := map[string]any{"number": 7, "head": map[string]any{"sha": head, "repo": map[string]string{"full_name": "example/widgets"}}, "base": map[string]string{"sha": baseCommit, "ref": "main"}, "user": map[string]string{"login": pullAuthor}}
	return eventJSON(pull, map[string]any{
		"action": action,
		"review": map[string]any{"id": 1, "state": "approved", "commit_id": head, "user": map[string]string{"login": login}},
		"sender": map[string]string{"login": login},
	})
}

// approve runs costroid approve on a review event, as the recipe does.
func approve(t *testing.T, event string) (int, string) {
	t.Helper()
	setEvent(t, approveEvent, event)
	var stdout, stderr bytes.Buffer
	code := approveCommand(context.Background(), nil, &stdout, &stderr)
	if stdout.Len() != 0 {
		t.Errorf("approve printed on stdout: %s", stdout.String())
	}
	return code, stderr.String()
}

// posted is a check run's result object.
type posted struct {
	CheckRunID *int64 `json:"check_run_id"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
	RowsSHA256 string `json:"rows_sha256"`
	Policy     struct {
		Conclusion string `json:"conclusion"`
	} `json:"policy"`
}

func objectOf(t *testing.T, check scmtest.Check) posted {
	t.Helper()
	var object posted
	body := strings.TrimSuffix(strings.TrimPrefix(check.Text, "```json\n"), "\n```")
	if err := json.Unmarshal([]byte(body), &object); err != nil {
		t.Fatalf("%v: %s", err, check.Text)
	}
	return object
}

// TestReviewFlipsCheckWithoutPush is the review path whole: the report fails
// the check, a writer's approval of the head turns it neutral with no new
// commit and no agent, and a dismissal turns it back.
func TestReviewFlipsCheckWithoutPush(t *testing.T) {
	repository := approvalEnvironment(t)
	failed := reportOn(t, repository, headCommit)
	if failed.Conclusion != policy.Failure {
		t.Fatalf("the report concluded %s, want failure", failed.Conclusion)
	}
	comments := len(repository.Comments)
	writes := len(repository.Writes())
	repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, time.Now().Add(-time.Hour))}

	code, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit))
	if code != 0 || len(repository.Checks) != 2 {
		t.Fatalf("exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	flipped := repository.Checks[1]
	object := objectOf(t, flipped)
	if flipped.Conclusion != policy.Neutral || flipped.HeadSHA != headCommit || object.Conclusion != policy.Neutral || object.Policy.Conclusion != policy.Failure {
		t.Errorf("check %+v, object %+v", flipped, object)
	}
	if object.HeadSHA != headCommit || object.RowsSHA256 != objectOf(t, failed).RowsSHA256 || object.CheckRunID == nil || *object.CheckRunID != flipped.ID {
		t.Errorf("the object is not the report's, named by its own check run: %+v", object)
	}
	for _, want := range []string{"**Verdict: neutral.** `reviewer` approved commit `9f2c6e1`", "The policy's verdict stays failure", fmt.Sprintf("Check run %d carries this report in full.", failed.ID)} {
		if !strings.Contains(flipped.Summary, want) {
			t.Errorf("no %q in the summary:\n%s", want, flipped.Summary)
		}
	}
	if repository.Head != headCommit {
		t.Error("the head moved")
	}
	// SPEC A.7: the approval posts and patches the check alone.
	for _, request := range repository.Writes()[writes:] {
		if !strings.HasPrefix(request, "POST /repos/example/widgets/check-runs") && !strings.HasPrefix(request, "PATCH /repos/example/widgets/check-runs/") {
			t.Errorf("approve wrote %s", request)
		}
	}
	if len(repository.Comments) != comments {
		t.Error("approve posted a comment")
	}

	if code, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit)); code != 0 || len(repository.Checks) != 2 || !strings.Contains(stderr, "already concludes neutral") {
		t.Fatalf("a second approval posted again: exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}

	repository.Reviews[0]["state"] = "DISMISSED"
	if code, stderr := approve(t, reviewEventJSON("dismissed", approver, headCommit)); code != 0 || len(repository.Checks) != 3 {
		t.Fatalf("exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	restored := repository.Checks[2]
	if restored.Conclusion != policy.Failure || objectOf(t, restored).Conclusion != policy.Failure || !strings.Contains(restored.Summary, "No approval binds commit `9f2c6e1`") {
		t.Errorf("the dismissal left %+v", restored)
	}
}

// TestApprovalContract holds each rule an approval must pass. Each refused
// review leaves the failed check as it is; scm.CountReview names the rule.
func TestApprovalContract(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour)
	cases := []struct {
		name    string
		review  map[string]any
		config  string
		setup   func(*testing.T, *reviewedRepository)
		refusal error
	}{
		{name: "a writer's approval of the head counts", review: review(31, approver, "APPROVED", headCommit, fresh)},
		{name: "a comment", review: review(31, approver, "COMMENTED", headCommit, fresh), refusal: scm.ErrNotApproved},
		{name: "a request for changes", review: review(31, approver, "CHANGES_REQUESTED", headCommit, fresh), refusal: scm.ErrNotApproved},
		{name: "a dismissed approval", review: review(31, approver, "DISMISSED", headCommit, fresh), refusal: scm.ErrNotApproved},
		{name: "a pending review", review: review(31, approver, "PENDING", headCommit, fresh), refusal: scm.ErrNotApproved},
		{name: "an App's account", review: review(31, "costroid-helper[bot]", "APPROVED", headCommit, fresh), refusal: scm.ErrBotApproval},
		{name: "a deleted account", review: map[string]any{"id": 31, "user": nil, "state": "APPROVED", "commit_id": headCommit, "submitted_at": fresh.UTC().Format(time.RFC3339)}, refusal: scm.ErrNoApprover},
		{name: "the author by default", review: review(31, pullAuthor, "APPROVED", headCommit, fresh), refusal: scm.ErrAuthorApproval},
		{name: "the author in another case", review: review(31, "AUTHOR", "APPROVED", headCommit, fresh), refusal: scm.ErrAuthorApproval},
		{name: "the author when allowed", review: review(31, pullAuthor, "APPROVED", headCommit, fresh), config: enforcedOver + "approval:\n  allow_author: true\n"},
		{name: "a commit off the head", review: review(31, approver, "APPROVED", mergeSHA, fresh), refusal: scm.ErrOffHead},
		{
			name: "a head moved since the review", review: review(31, approver, "APPROVED", headCommit, fresh), refusal: scm.ErrOffHead,
			setup: func(t *testing.T, r *reviewedRepository) {
				r.Head = movedCommit
				reportOn(t, r, movedCommit)
			},
		},
		{name: "an expired review", review: review(31, approver, "APPROVED", headCommit, now.Add(-7*day-time.Minute)), refusal: scm.ErrExpiredReview},
		{name: "a review inside a longer expiry", review: review(31, approver, "APPROVED", headCommit, now.Add(-9*day)), config: enforcedOver + "approval:\n  expires_days: 10\n"},
		{name: "a review past a shorter expiry", review: review(31, approver, "APPROVED", headCommit, now.Add(-25*time.Hour)), config: enforcedOver + "approval:\n  expires_days: 1\n", refusal: scm.ErrExpiredReview},
		{name: "a reviewer who can only read", review: review(31, approver, "APPROVED", headCommit, fresh), setup: func(_ *testing.T, r *reviewedRepository) { r.Permission = "read" }, refusal: scm.ErrNoWriteRole},
		{name: "a reviewer who can triage", review: review(31, approver, "APPROVED", headCommit, fresh), setup: func(_ *testing.T, r *reviewedRepository) { r.Permission = "triage" }, refusal: scm.ErrNoWriteRole},
		{name: "an approval key Costroid does not read", review: review(31, approver, "APPROVED", headCommit, fresh), config: enforcedOver + "approval:\n  allow_bots: true\n", refusal: errNoRules},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repository := approvalEnvironment(t)
			if tc.config != "" {
				repository.Files[configFile] = tc.config
			}
			failed := reportOn(t, repository, headCommit)
			if failed.Conclusion != policy.Failure {
				t.Fatalf("the report concluded %s", failed.Conclusion)
			}
			if tc.setup != nil {
				tc.setup(t, repository)
			}
			before := len(repository.Checks)
			repository.Reviews = []map[string]any{tc.review}
			code, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit))
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if tc.refusal == nil {
				if len(repository.Checks) != before+1 || repository.Checks[before].Conclusion != policy.Neutral {
					t.Fatalf("the approval did not count: %+v\n%s", repository.Checks, stderr)
				}
				return
			}
			if len(repository.Checks) != before {
				t.Fatalf("a refused review posted %+v\n%s", repository.Checks[before:], stderr)
			}
			if !strings.Contains(stderr, "costroid: warning: no approval counts: ") || !strings.Contains(stderr, "not posted: check run") {
				t.Errorf("the log does not say why:\n%s", stderr)
			}
			if tc.refusal == errNoRules {
				return
			}
			if err := countListedReview(t, repository.Head); !errors.Is(err, tc.refusal) {
				t.Errorf("refused with %v, want %v", err, tc.refusal)
			}
		})
	}
}

// errNoRules marks a case whose settings give no rules at all.
var errNoRules = errors.New("no rules")

// countListedReview holds the one review the host lists to every rule, under
// the settings at the base commit.
func countListedReview(t *testing.T, head string) error {
	t.Helper()
	ctx := context.Background()
	reporter, err := hostFromEnvironment(pullContext{head: head})
	if err != nil {
		t.Fatal(err)
	}
	host := reporter.(approvalHost)
	rules, err := approvalRules(ctx, host, baseCommit)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := host.Reviews(ctx, 7)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("%d reviews: %v", len(reviews), err)
	}
	_, err = scm.CountReview(ctx, host, rules, scm.PullRequest{Number: 7, HeadSHA: head, Author: pullAuthor}, reviews[0], time.Now())
	return err
}

// TestApprovalStandingIsEachReviewersLatestDecision: a later comment keeps an
// approval, a later request for changes ends it, and another writer's
// approval still counts.
func TestApprovalStandingIsEachReviewersLatestDecision(t *testing.T) {
	now := time.Now()
	pull := scm.PullRequest{Number: 7, HeadSHA: headCommit, Author: pullAuthor}
	rules := scm.ApprovalRules{Lasts: 7 * day}
	writer := roles{"reviewer": true, "second": true}
	for name, tc := range map[string]struct {
		reviews []scm.Review
		want    string
	}{
		"a later comment": {[]scm.Review{
			{ID: 1, Reviewer: approver, State: scm.ReviewApproved, CommitID: headCommit, SubmittedAt: now.Add(-2 * time.Hour)},
			{ID: 2, Reviewer: approver, State: scm.ReviewCommented, CommitID: headCommit, SubmittedAt: now.Add(-time.Hour)},
		}, approver},
		"a later request for changes": {[]scm.Review{
			{ID: 1, Reviewer: approver, State: scm.ReviewApproved, CommitID: headCommit, SubmittedAt: now.Add(-2 * time.Hour)},
			{ID: 2, Reviewer: "Reviewer", State: scm.ReviewChangesRequested, CommitID: headCommit, SubmittedAt: now.Add(-time.Hour)},
		}, ""},
		"another writer's approval": {[]scm.Review{
			{ID: 1, Reviewer: "second", State: scm.ReviewApproved, CommitID: headCommit, SubmittedAt: now.Add(-3 * time.Hour)},
			{ID: 2, Reviewer: approver, State: scm.ReviewApproved, CommitID: headCommit, SubmittedAt: now.Add(-2 * time.Hour)},
			{ID: 3, Reviewer: approver, State: scm.ReviewChangesRequested, CommitID: headCommit, SubmittedAt: now.Add(-time.Hour)},
		}, "second"},
	} {
		t.Run(name, func(t *testing.T) {
			approval, err := scm.ApprovalOf(context.Background(), writer, rules, pull, tc.reviews, now)
			if approval.Approver != tc.want || (tc.want == "") != errors.Is(err, scm.ErrNoApproval) {
				t.Errorf("approver %q, err %v", approval.Approver, err)
			}
		})
	}
}

type roles map[string]bool

func (r roles) CanWrite(_ context.Context, login string) (bool, error) {
	return r[login], nil
}

// TestReportCarriesAnApprovalOfItsHeadAlone: a report run again on an
// approved head concludes neutral and says who approved, and a push leaves
// the approval of the earlier commit behind.
func TestReportCarriesAnApprovalOfItsHeadAlone(t *testing.T) {
	repository := approvalEnvironment(t)
	repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, time.Now().Add(-time.Hour))}
	approved := reportOn(t, repository, headCommit)
	body := repository.Comments[0].Body
	if approved.Conclusion != policy.Neutral || objectOf(t, approved).Policy.Conclusion != policy.Failure || !strings.Contains(body, "**Verdict: neutral.** `reviewer` approved commit `9f2c6e1`") {
		t.Fatalf("check %s, comment:\n%s", approved.Conclusion, body)
	}
	repository.Head = movedCommit
	if pushed := reportOn(t, repository, movedCommit); pushed.Conclusion != policy.Failure {
		t.Errorf("the approval of %s held on %s: %s", headCommit, movedCommit, pushed.Conclusion)
	}
}

// TestApproveRefusesWhatIsNoReviewOfThisRepository: another event, a review
// with no signed-in account and a fork's review post nothing.
func TestApproveRefusesWhatIsNoReviewOfThisRepository(t *testing.T) {
	fork := strings.Replace(reviewEventJSON("submitted", approver, headCommit), `"full_name":"example/widgets"`, `"full_name":"stranger/widgets"`, 1)
	anonymous := strings.Replace(reviewEventJSON("submitted", approver, headCommit), `"user":{"login":"reviewer"}`, `"user":null`, 1)
	for name, tc := range map[string]struct{ event, eventName, says string }{
		"a pull_request event": {pullEvent(headCommit), "pull_request", "approve runs on the pull_request_review event"},
		"a comment":            {reviewEventJSON("created", approver, headCommit), "issue_comment", "approve runs on the pull_request_review event"},
		"a fork's review":      {fork, approveEvent, "a review on a fork's pull request runs with a read-only token"},
		"no signed-in account": {anonymous, approveEvent, "the event names no review by a signed-in account"},
	} {
		t.Run(name, func(t *testing.T) {
			repository := approvalEnvironment(t)
			reportOn(t, repository, headCommit)
			repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, time.Now().Add(-time.Hour))}
			setEvent(t, tc.eventName, tc.event)
			var stdout, stderr bytes.Buffer
			code := approveCommand(context.Background(), nil, &stdout, &stderr)
			if code != 0 || len(repository.Checks) != 1 || !strings.Contains(stderr.String(), "costroid: not posted: "+tc.says) {
				t.Errorf("exit %d, %d checks: %s", code, len(repository.Checks), stderr.String())
			}
		})
	}
	approvalEnvironment(t)
	t.Setenv("GITHUB_ACTIONS", "")
	var stderr bytes.Buffer
	if code := approveCommand(context.Background(), nil, &bytes.Buffer{}, &stderr); code != exitFailure {
		t.Errorf("outside GitHub Actions: exit %d: %s", code, stderr.String())
	}
}

// TestApprovalWorkflowLints holds the shipped approval recipe to the linter,
// and the linter to its two rules: a review may start a workflow with no
// agent, and an approval workflow holds no code, no agent and one write.
func TestApprovalWorkflowLints(t *testing.T) {
	source, err := os.ReadFile(approvalRecipe)
	if err != nil {
		t.Fatal(err)
	}
	if report := recipelint.LintBytes(approvalRecipe, source); len(report.Findings) != 0 {
		t.Fatalf("findings: %v", report.Findings)
	}
	const install = "      - uses: Costroid/costroid/action@"
	const checkout = "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n        with:\n          persist-credentials: false\n"
	edits := map[string]struct{ from, to, rule string }{
		"a checkout":             {install, checkout + install, recipelint.RuleApprovalCode},
		"a fetch":                {install, "      - run: git fetch origin \"$HEAD\"\n" + install, recipelint.RuleApprovalCode},
		"an action on disk":      {install, "      - uses: ./.github/actions/post\n" + install, recipelint.RuleApprovalCode},
		"an agent":               {install, "      - run: claude -p hello\n" + install, recipelint.RuleApprovalCode},
		"another event":          {"  pull_request_review:\n", "  pull_request:\n", recipelint.RuleApprovalCode},
		"a second event":         {"  pull_request_review:\n", "  issue_comment:\n  pull_request_review:\n", recipelint.RuleApprovalCode},
		"write on pull requests": {"      pull-requests: read\n", "      pull-requests: write\n", recipelint.RuleApprovalCode},
		"no pull-requests: read": {"      pull-requests: read\n", "", recipelint.RuleApprovalCode},
		"write on contents":      {"      contents: read\n", "      contents: write\n", recipelint.RuleApprovalCode},
		"an agent on a review":   {install, "      - run: claude -p hello\n" + install, recipelint.RuleAgentPromptInput},
		"a called workflow":      {"    runs-on: ubuntu-24.04\n", "    uses: example/widgets/.github/workflows/w.yml@3d3c42e5aac5ba805825da76410c181273ba90b1 # v1\n    runs-on: ubuntu-24.04\n", recipelint.RuleApprovalCode},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			edited := strings.Replace(string(source), edit.from, edit.to, 1)
			if edited == string(source) {
				t.Fatalf("the edit %q matched nothing", edit.from)
			}
			report := recipelint.LintBytes(approvalRecipe, []byte(edited))
			if !slices.ContainsFunc(report.Findings, func(finding recipelint.Finding) bool { return finding.Rule == edit.rule }) {
				t.Errorf("want %s, got %v", edit.rule, report.Findings)
			}
		})
	}
	agentOnReview := "on:\n  pull_request_review:\n" + agentWorkflowBody
	report := recipelint.LintBytes("agent.yml", []byte(agentOnReview))
	if !slices.ContainsFunc(report.Findings, func(finding recipelint.Finding) bool { return finding.Rule == recipelint.RuleAgentPromptInput }) {
		t.Errorf("a review starts an agent, and: %v", report.Findings)
	}
}

// agentWorkflowBody is one job that runs an agent, after the workflow's on.
const agentWorkflowBody = `permissions: {}
jobs:
  agent:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
    steps:
      - run: claude -p hello
`

// TestApprovalSettingsMatchTheSchema holds approval.* in the schema, the
// comment's reader and the policy's parser to one list, and the parser to the
// schema's bounds.
func TestApprovalSettingsMatchTheSchema(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(schema.Properties["approval"].Properties)); !slices.Equal(got, policy.ApprovalKeys()) {
		t.Errorf("the schema names %v, the policy parses %v", got, policy.ApprovalKeys())
	}
	if settings, err := policy.ParseApproval(nil); err != nil || settings != (policy.ApprovalSettings{ExpiresDays: policy.DefaultApprovalDays}) {
		t.Errorf("defaults %+v, %v", settings, err)
	}
	for _, document := range []string{
		"approval:\n  expires_days: 0\n", "approval:\n  expires_days: 91\n", "approval:\n  expires_days: 1.5\n",
		"approval:\n  expires_days: \"7\"\n", "approval:\n  allow_author: yes\n", "approval:\n  allow_bots: true\n", "approval: true\n",
	} {
		if _, err := policy.ParseApproval([]byte(document)); err == nil {
			t.Errorf("%q was read", document)
		}
	}
	if settings, err := policy.ParseApproval([]byte("approval:\n  allow_author: true\n  expires_days: 90\n")); err != nil || !settings.AllowAuthor || settings.ExpiresDays != 90 {
		t.Errorf("%+v, %v", settings, err)
	}
	if _, err := comment.ParseConfig([]byte("approval:\n  allow_author: true\n")); err != nil {
		t.Errorf("the comment's reader: %v", err)
	}
}

// TestApproveRepostsOnlyAnObjectItWrites: approve posts a check run's object
// again only when this release would write it byte for byte, so nothing but
// the conclusion and the check run's id can change.
func TestApproveRepostsOnlyAnObjectItWrites(t *testing.T) {
	repository := approvalEnvironment(t)
	text := reportOn(t, repository, headCommit).Text
	if _, err := comment.ParseResult(text); err != nil {
		t.Fatalf("the report's own object: %v", err)
	}
	object := strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(object)); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]string{
		"a field this release does not write": strings.Replace(text, "{\n", "{\n  \"approved_by\": \"someone\",\n", 1),
		"an older schema":                     strings.Replace(text, `"costroid.result/0.6"`, `"costroid.result/0.5"`, 1),
		"another layout":                      "```json\n" + compact.String() + "\n```",
		"a backtick":                          strings.Replace(text, `"amounts": "shown"`, "\"amounts\": \"shown`\"", 1),
		"no fence":                            object,
	} {
		t.Run(name, func(t *testing.T) {
			if changed == text {
				t.Fatal("the change matched nothing")
			}
			_, err := comment.ParseResult(changed)
			if err == nil {
				t.Fatal("read")
			}
			if strings.Contains(changed, "approved_by") && !strings.Contains(err.Error(), `unknown field "approved_by"`) {
				t.Errorf("the error does not name the field: %v", err)
			}
		})
	}
	repository.Checks[0].Text = strings.Replace(text, `"costroid.result/0.6"`, `"costroid.result/0.5"`, 1)
	repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, time.Now().Add(-time.Hour))}
	if code, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit)); code != 0 || len(repository.Checks) != 1 || !strings.Contains(stderr, "and this release writes costroid.result/0.6") {
		t.Errorf("exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
}

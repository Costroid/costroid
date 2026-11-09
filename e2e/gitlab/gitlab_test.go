//go:build gitlab

// Package gitlab runs the GitLab recipe's report on a self-managed GitLab CE
// and a GitLab Runner, both in Docker, on recorded rows. It uses no agent, no
// key and no network: the containers have loopback only.
//
// Run it with go test -tags gitlab ./e2e/gitlab. It needs Docker, the two
// images it names and about 4 GB of memory, and it takes about 5 minutes.
package gitlab

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	marker = "<!-- costroid:comment -->"
	// recordedCost is the list cost of cmd/costroid/demo/rows.jsonl, which
	// its README derives from the recorded run.
	recordedCost = "0.0391529"
	agentLabel   = "costroid-agent"
	sourceBranch = "change"
	agentJob     = "costroid:claude"
	reportJob    = "costroid:report"
)

// gate is what one pipeline must show.
type gate struct {
	pipeline string
	verdict  string
	exit     int
}

var (
	// overBudget is the recorded USD 0.0391529 against a base budget of USD
	// 0.01 with the warn-only period ended: exit 1, and the pipeline fails.
	overBudget = gate{pipeline: "failed", verdict: "failure", exit: 1}
	// withinBudget concludes neutral, because rows the agent's job wrote can
	// show a run over a limit and never one under it: exit 3, a warning, and
	// the pipeline passes.
	withinBudget = gate{pipeline: "success", verdict: "neutral", exit: 3}
)

func TestGitLabSelfManagedMergeRequest(t *testing.T) {
	gitlab := start(t, buildCostroid(t))
	root := gitlab.rootToken()
	project := newProject(t, gitlab, maintainer(t, root))

	project.commit("main", "", "Add the pipeline and the base budget",
		create(".gitlab-ci.yml", read(t, "testdata/gitlab-ci.yml")),
		create("costroid-gitlab-ci.yml", read(t, "../../recipes/gitlab-ci.yml")),
		create("costroid.yml", budget("0.01")),
		create("recorded-rows.jsonl", read(t, "../../cmd/costroid/demo/rows.jsonl")))
	head := project.commit(sourceBranch, "main", "Start the change", create("change.txt", "one\n"))
	request := project.openMergeRequest()

	project.expectGate(project.awaitPipeline(request, head), overBudget)
	first := project.stickyNote(request, head, overBudget)
	project.expectMergeRefused(request)

	head = project.commit(sourceBranch, "", "Change it again", update("change.txt", "two\n"))
	project.expectGate(project.awaitPipeline(request, head), overBudget)
	second := project.stickyNote(request, head, overBudget)
	if second.ID != first.ID || second.Body == first.Body {
		t.Fatalf("the second push did not edit note %d: it left note %d", first.ID, second.ID)
	}

	project.commit("main", "", "Raise the base budget", update("costroid.yml", budget("1")))
	head = project.commit(sourceBranch, "", "Change it a third time", update("change.txt", "three\n"))
	project.expectGate(project.awaitPipeline(request, head), withinBudget)
	if third := project.stickyNote(request, head, withinBudget); third.ID != first.ID {
		t.Fatalf("the third push left note %d beside note %d", third.ID, first.ID)
	}
	project.expectMerged(request)
}

// buildCostroid builds the binary the runner's jobs run.
func buildCostroid(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "costroid")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/costroid")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return binary
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func budget(usd string) string {
	return fmt.Sprintf("policy:\n  run_budget_usd: %q\n  enforce: true\n", usd)
}

func secret(t *testing.T) string {
	t.Helper()
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func expiry() string {
	return time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
}

// maintainer creates a user through the API and returns a client with a token
// for that user.
func maintainer(t *testing.T, root *client) *client {
	t.Helper()
	var user struct {
		ID int64 `json:"id"`
	}
	root.must(http.MethodPost, "/users", map[string]any{
		"username": "maintainer", "name": "Maintainer", "email": "maintainer@example.com",
		"password": secret(t), "skip_confirmation": true,
	}, &user)
	var token struct {
		Token string `json:"token"`
	}
	root.must(http.MethodPost, fmt.Sprintf("/users/%d/impersonation_tokens", user.ID), map[string]any{
		"name": "costroid-e2e", "scopes": []string{"api"}, "expires_at": expiry(),
	}, &token)
	return &client{t: t, http: root.http, token: token.Token}
}

// project is one GitLab project with the recipe's settings and a runner.
type project struct {
	*client
	t     *testing.T
	path  string
	until time.Time
	// bot is the user of the report's project access token.
	bot int64
}

// newProject creates the project, gives the report its token as docs/recipes.md
// says, turns the gate on, makes the pipeline a merge check and registers a
// shell runner for the project.
func newProject(t *testing.T, in *instance, user *client) *project {
	t.Helper()
	var created struct {
		ID int64 `json:"id"`
	}
	user.must(http.MethodPost, "/projects", map[string]any{
		"name": "widgets", "visibility": "private", "initialize_with_readme": true,
		"only_allow_merge_if_pipeline_succeeds": true,
	}, &created)
	p := &project{client: user, t: t, path: fmt.Sprintf("/projects/%d", created.ID), until: in.until}
	var token struct {
		Token  string `json:"token"`
		UserID int64  `json:"user_id"`
	}
	user.must(http.MethodPost, p.path+"/access_tokens", map[string]any{
		"name": "costroid-report", "scopes": []string{"api"}, "access_level": 20, "expires_at": expiry(),
	}, &token)
	p.bot = token.UserID
	user.must(http.MethodPost, p.path+"/variables", map[string]any{
		"key": "COSTROID_GITLAB_TOKEN", "value": token.Token, "masked": true, "protected": false,
		"environment_scope": "costroid-report",
	}, nil)
	user.must(http.MethodPost, p.path+"/variables", map[string]any{"key": "COSTROID_GATE", "value": "true"}, nil)
	p.registerRunner(in, created.ID)
	return p
}

// registerRunner creates a project runner through the API and registers the
// runner container with its token, passed in the environment.
func (p *project) registerRunner(in *instance, id int64) {
	p.t.Helper()
	var runner struct {
		Token string `json:"token"`
	}
	p.must(http.MethodPost, "/user/runners", map[string]any{
		"runner_type": "project_type", "project_id": id, "description": "costroid-e2e",
	}, &runner)
	if _, err := in.exec(in.runner, []string{"CI_SERVER_TOKEN=" + runner.Token},
		"gitlab-runner", "register", "--non-interactive", "--url", "http://127.0.0.1",
		"--executor", "shell", "--name", "costroid-e2e"); err != nil {
		p.t.Fatalf("register the runner: %v", err)
	}
}

type action struct {
	Action   string `json:"action"`
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func create(path, content string) action { return action{"create", path, content} }
func update(path, content string) action { return action{"update", path, content} }

// commit pushes one commit to branch through the API, creating the branch
// from from when from is not empty, and returns the commit's id.
func (p *project) commit(branch, from, message string, actions ...action) string {
	p.t.Helper()
	body := map[string]any{"branch": branch, "commit_message": message, "actions": actions}
	if from != "" {
		body["start_branch"] = from
	}
	var commit struct {
		ID string `json:"id"`
	}
	p.must(http.MethodPost, p.path+"/repository/commits", body, &commit)
	return commit.ID
}

func (p *project) openMergeRequest() int {
	p.t.Helper()
	var request struct {
		IID int `json:"iid"`
	}
	p.must(http.MethodPost, p.path+"/merge_requests", map[string]any{
		"source_branch": sourceBranch, "target_branch": "main", "title": "Change", "labels": agentLabel,
	}, &request)
	return request.IID
}

type pipeline struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
	Source string `json:"source"`
}

// awaitPipeline waits for the merge request's pipeline at head to finish.
func (p *project) awaitPipeline(request int, head string) pipeline {
	p.t.Helper()
	var found pipeline
	await(p.t, fmt.Sprintf("the pipeline at %s", head), p.until, func() (bool, string) {
		var pipelines []pipeline
		p.must(http.MethodGet, fmt.Sprintf("%s/merge_requests/%d/pipelines", p.path, request), nil, &pipelines)
		for _, candidate := range pipelines {
			if candidate.SHA == head {
				found = candidate
				return slices.Contains([]string{"success", "failed", "canceled", "skipped"}, candidate.Status), "status " + candidate.Status
			}
		}
		return false, "no pipeline yet"
	})
	if found.Source != "merge_request_event" {
		p.t.Fatalf("pipeline %d ran for %s, not a merge request", found.ID, found.Source)
	}
	return found
}

type job struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Stage        string `json:"stage"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
}

// expectGate checks that the agent's job and the report's .post job ran, that
// the report exited as the gate says and that the pipeline shows it.
func (p *project) expectGate(run pipeline, want gate) {
	p.t.Helper()
	var jobs []job
	p.must(http.MethodGet, fmt.Sprintf("%s/pipelines/%d/jobs", p.path, run.ID), nil, &jobs)
	byName := map[string]job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	if len(jobs) != 2 || byName[agentJob].Status != "success" {
		p.t.Fatalf("pipeline %d ran %+v, want %s to pass and %s", run.ID, jobs, agentJob, reportJob)
	}
	report := byName[reportJob]
	if report.Stage != ".post" || report.Status != "failed" || report.AllowFailure != (want.exit == 3) {
		p.t.Fatalf("pipeline %d: the report's job is %+v", run.ID, report)
	}
	var trace string
	p.must(http.MethodGet, fmt.Sprintf("%s/jobs/%d/trace", p.path, report.ID), nil, &trace)
	for _, line := range []string{
		fmt.Sprintf("costroid: the verdict is %s, so the report exits %d", want.verdict, want.exit),
		fmt.Sprintf("Job failed: exit status %d", want.exit),
	} {
		if !strings.Contains(trace, line) {
			p.t.Fatalf("pipeline %d: no %q in the report's log:\n%s", run.ID, line, trace)
		}
	}
	if run.Status != want.pipeline {
		p.t.Fatalf("pipeline %d is %s, want %s", run.ID, run.Status, want.pipeline)
	}
}

type note struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author struct {
		ID int64 `json:"id"`
	} `json:"author"`
}

// stickyNote returns the merge request's one note that is not GitLab's own,
// and fails unless the report's token wrote it about head with the verdict.
func (p *project) stickyNote(request int, head string, want gate) note {
	p.t.Helper()
	var all, written []note
	p.must(http.MethodGet, fmt.Sprintf("%s/merge_requests/%d/notes?sort=asc&per_page=100", p.path, request), nil, &all)
	for _, n := range all {
		if !n.System {
			written = append(written, n)
		}
	}
	if len(written) != 1 {
		p.t.Fatalf("the merge request holds %d notes, want one", len(written))
	}
	sticky := written[0]
	if sticky.Author.ID != p.bot || !strings.HasPrefix(sticky.Body, marker) {
		p.t.Fatalf("note %d is by user %d, not the report's user %d, or lacks the marker", sticky.ID, sticky.Author.ID, p.bot)
	}
	for _, want := range []string{`"head_sha": "` + head + `"`, "USD " + recordedCost, `"conclusion": "` + want.verdict + `"`} {
		if !strings.Contains(sticky.Body, want) {
			p.t.Fatalf("no %s in note %d:\n%s", want, sticky.ID, sticky.Body)
		}
	}
	return sticky
}

// expectMergeRefused tries the merge: with Pipelines must succeed on, a failed
// pipeline must stop it.
func (p *project) expectMergeRefused(request int) {
	p.t.Helper()
	path := fmt.Sprintf("%s/merge_requests/%d", p.path, request)
	if status, _ := p.call(http.MethodPut, path+"/merge", map[string]any{}, nil); status/100 == 2 {
		p.t.Fatal("the merge request merged on a failed pipeline")
	}
	var state struct {
		State string `json:"state"`
	}
	p.must(http.MethodGet, path, nil, &state)
	if state.State != "opened" {
		p.t.Fatalf("the merge request is %s after a refused merge", state.State)
	}
}

// expectMerged merges: a pipeline whose report only warned does not stop it.
func (p *project) expectMerged(request int) {
	p.t.Helper()
	var state struct {
		State string `json:"state"`
	}
	p.must(http.MethodPut, fmt.Sprintf("%s/merge_requests/%d/merge", p.path, request), map[string]any{}, &state)
	if state.State != "merged" {
		p.t.Fatalf("the merge request is %s after the merge", state.State)
	}
}

package cicontext

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

const (
	head   = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	base   = "1111111111111111111111111111111111111111"
	merge  = "2222222222222222222222222222222222222222"
	pullID = "7"
)

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func writeEvent(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func github(t *testing.T, event string) map[string]string {
	t.Helper()
	return map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "Acme/Widgets",
		"GITHUB_RUN_ID": "1234", "GITHUB_JOB": "agent", "GITHUB_SHA": merge, "GITHUB_EVENT_NAME": "pull_request",
		"GITHUB_EVENT_PATH": writeEvent(t, event),
	}
}

const pullRequestEvent = `{"pull_request":{"number":7,"head":{"sha":"` + head + `"},"base":{"sha":"` + base + `"}}}`

func TestOutsideCINothingIsStated(t *testing.T) {
	context, err := Read(environment(nil))
	if err != nil || context != (Context{}) {
		t.Fatalf("%+v, %v", context, err)
	}
	if len(context.Tags()) != 0 || context.ResourceAttributes("service.name=x") != "" {
		t.Fatalf("%v", context.Tags())
	}
}

func TestGitHubPullRequestStatesTheHeadNeverTheMergeCommit(t *testing.T) {
	context, err := Read(environment(github(t, pullRequestEvent)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		focus.TagRepo: "github.com/acme/widgets", focus.TagPR: pullID, focus.TagCommitSHA: head, focus.TagWorkflowRunID: "1234",
	}
	if got := context.Tags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if context.Base != base || context.RepositoryURL != "https://github.com/Acme/Widgets" {
		t.Fatalf("%+v", context)
	}
}

func TestGitHubPushStatesTheCommitItBuilt(t *testing.T) {
	values := github(t, `{"ref":"refs/heads/main"}`)
	values["GITHUB_EVENT_NAME"] = "push"
	context, err := Read(environment(values))
	if err != nil || context.Head != merge || context.PullRequest != "" {
		t.Fatalf("%+v, %v", context, err)
	}
}

func TestUnreadableEventOnAPullRequestStatesNoHeadAndSaysWhy(t *testing.T) {
	values := github(t, pullRequestEvent)
	values["GITHUB_EVENT_PATH"] = filepath.Join(t.TempDir(), "gone.json")
	context, err := Read(environment(values))
	if err == nil {
		t.Fatal("no error")
	}
	if context.Head != "" || context.Repository != "github.com/acme/widgets" || context.Run != "1234" {
		t.Fatalf("%+v", context)
	}
}

func TestMalformedEventIsAnErrorAndNeverAPanic(t *testing.T) {
	context, err := Read(environment(github(t, `{"pull_request":`)))
	if err == nil || context.PullRequest != "" {
		t.Fatalf("%+v, %v", context, err)
	}
}

func TestGitLabMergeRequestStatesTheSourceHeadInAMergedResultsPipeline(t *testing.T) {
	values := map[string]string{
		"GITLAB_CI": "true", "CI_SERVER_HOST": "gitlab.example", "CI_PROJECT_PATH": "Group/Sub/Widgets",
		"CI_PROJECT_URL": "https://gitlab.example/Group/Sub/Widgets", "CI_MERGE_REQUEST_IID": "12", "CI_COMMIT_SHA": merge,
		"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": head, "CI_MERGE_REQUEST_DIFF_BASE_SHA": base, "CI_PIPELINE_ID": "55", "CI_JOB_ID": "66",
	}
	context, err := Read(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		focus.TagRepo: "gitlab.example/group/sub/widgets", focus.TagPR: "12", focus.TagCommitSHA: head, focus.TagWorkflowRunID: "55",
	}
	if got := context.Tags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if context.Task != "66" || context.Base != base {
		t.Fatalf("%+v", context)
	}
}

func TestGitLabMergeRequestPipelineStatesTheCommitSHA(t *testing.T) {
	context, err := Read(environment(map[string]string{"GITLAB_CI": "true", "CI_COMMIT_SHA": head}))
	if err != nil || context.Head != head {
		t.Fatalf("%+v, %v", context, err)
	}
}

func TestAValueThatMissesItsPatternIsNeverStated(t *testing.T) {
	values := github(t, `{"pull_request":{"number":0,"head":{"sha":"not-a-sha"},"base":{"sha":"`+base+`"}}}`)
	values["GITHUB_REPOSITORY"] = "acme/wid gets"
	values["GITHUB_RUN_ID"] = "12 34"
	context, err := Read(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	if got := context.Tags(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestResourceAttributesCarryEveryJoinKey(t *testing.T) {
	context, err := Read(environment(github(t, pullRequestEvent)))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(context.ResourceAttributes(""), ",")
	want := []string{
		"cicd.pipeline.run.id=1234", "cicd.pipeline.task.run.id=agent", "vcs.change.id=7", "vcs.ref.base.revision=" + base,
		"vcs.ref.head.revision=" + head, "vcs.repository.url.full=https://github.com/Acme/Widgets",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestResourceAttributesKeepTheJobsOwnAndReplaceAStaleJoinKey(t *testing.T) {
	context := Context{PullRequest: pullID, Run: "99"}
	got := context.ResourceAttributes("service.name=ci,vcs.change.id=1,costroid.cost_basis=measured")
	want := "service.name=ci,costroid.cost_basis=measured,cicd.pipeline.run.id=99,vcs.change.id=7"
	if got != want {
		t.Fatalf("%s", got)
	}
}

func TestResourceAttributeValuesAreEscaped(t *testing.T) {
	if got := escapeAttribute("a,b;c d%e"); got != "a%2Cb%3Bc%20d%25e" {
		t.Fatal(got)
	}
	if got := escapeAttribute("https://x.example/a"); got != "https://x.example/a" {
		t.Fatal(got)
	}
}

func TestDetectNamesTheSystemAndPrefersGitHub(t *testing.T) {
	cases := []struct {
		variables map[string]string
		want      System
	}{
		{map[string]string{}, NoSystem},
		{map[string]string{"GITHUB_ACTIONS": "1", "GITLAB_CI": "1"}, NoSystem},
		{map[string]string{"GITHUB_ACTIONS": "true"}, GitHubActions},
		{map[string]string{"GITLAB_CI": "true"}, GitLabCI},
		{map[string]string{"GITHUB_ACTIONS": "true", "GITLAB_CI": "true"}, GitHubActions},
	}
	for _, c := range cases {
		if got := Detect(func(name string) string { return c.variables[name] }); got != c.want {
			t.Errorf("%v: got %v", c.variables, got)
		}
	}
}

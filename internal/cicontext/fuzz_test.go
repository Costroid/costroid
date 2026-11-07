package cicontext

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const fuzzHead = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"

// assertChecked holds every value a Context states to the tag convention's
// pattern for it.
func assertChecked(t *testing.T, c Context) {
	t.Helper()
	for name, field := range map[string]struct {
		value   string
		pattern *regexp.Regexp
	}{
		"pull request": {c.PullRequest, pullRequestPattern},
		"head":         {c.Head, commitPattern},
		"base":         {c.Base, commitPattern},
		"run":          {c.Run, runPattern},
		"task":         {c.Task, runPattern},
		"repository":   {c.Repository, repositoryPattern},
	} {
		if field.value != "" && !field.pattern.MatchString(field.value) {
			t.Fatalf("the %s reads %q", name, field.value)
		}
	}
	if len(c.Repository) > maxRepositoryLength || (c.Repository == "" && c.RepositoryURL != "") {
		t.Fatalf("repository %q, address %q", c.Repository, c.RepositoryURL)
	}
	for key, value := range c.Tags() {
		if value == "" || strings.ContainsAny(value, "\r\n") {
			t.Fatalf("tag %s reads %q", key, value)
		}
	}
}

// FuzzReadEvent reads a GitHub event file the job's runner wrote, under any
// event name and GITHUB_SHA. What the run states from it keeps the tag
// convention: a pull request is a number, a head and a base are full commit
// SHAs, and nothing else is kept.
func FuzzReadEvent(f *testing.F) {
	f.Add([]byte(`{"pull_request":{"number":7,"head":{"sha":"`+fuzzHead+`"},"base":{"sha":"`+fuzzHead+`"}}}`), "pull_request", fuzzHead)
	f.Add([]byte(`{"issue":{"number":7,"pull_request":{}}}`), "issue_comment", fuzzHead)
	f.Add([]byte(`{"pull_request":{"number":-7,"head":{"sha":"HEAD"}}}`), "pull_request_target", "")
	f.Add([]byte(`{"pull_request":{"number":99999999999999999999}}`), "push", fuzzHead)
	f.Add([]byte(`[]`), "schedule", "not a sha")
	f.Fuzz(func(t *testing.T, event []byte, name, sha string) {
		path := filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(path, event, 0o600); err != nil {
			t.Fatal(err)
		}
		env := map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_EVENT_PATH": path, "GITHUB_EVENT_NAME": name, "GITHUB_SHA": sha,
			"GITHUB_REPOSITORY": "acme/widgets", "GITHUB_RUN_ID": "42", "GITHUB_JOB": "agent",
		}
		context, err := Read(func(key string) string { return env[key] })
		assertChecked(t, context)
		if err != nil && context.PullRequest != "" {
			t.Fatalf("an event that could not be read named pull request %s", context.PullRequest)
		}
		parsed, readErr := ReadEvent(path)
		switch {
		case context.Head == "":
		case readErr == nil && parsed.PullRequest != nil:
			if context.Head != parsed.PullRequest.Head.SHA {
				t.Fatalf("a pull request's head read as %s, not the event's %s", context.Head, parsed.PullRequest.Head.SHA)
			}
		case !buildsItsCommit(name) || context.Head != sha:
			t.Fatalf("a %s event named head %s, which may be a merge commit", name, context.Head)
		}
	})
}

// FuzzResourceAttributes merges the run's attributes into the value a job
// set. Each of Costroid's keys appears once, with its own value escaped, and
// every pair the job set under another key is kept as it was.
func FuzzResourceAttributes(f *testing.F) {
	f.Add("service.name=agent,vcs.change.id=1", "7", "cicd run;1")
	f.Add("", "", "")
	f.Add(",,=,vcs.change.id", "12", "a,b=c%")
	f.Fuzz(func(t *testing.T, existing, pullRequest, run string) {
		c := Context{PullRequest: pullRequest, Head: fuzzHead, Run: run}
		merged := c.ResourceAttributes(existing)
		ours := map[string]bool{}
		for _, pair := range strings.Split(merged, ",") {
			key, value, _ := strings.Cut(pair, "=")
			switch strings.TrimSpace(key) {
			case attributeChange, attributeHead, attributeRun:
				if ours[key] || strings.ContainsAny(value, "\",;\\ ") || strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r > '~' }) {
					t.Fatalf("%s is repeated or unescaped in %q", key, merged)
				}
				ours[key] = true
			}
		}
		for _, pair := range strings.Split(existing, ",") {
			key, _, _ := strings.Cut(pair, "=")
			mine := map[string]bool{attributeChange: pullRequest != "", attributeHead: true, attributeRun: run != ""}[strings.TrimSpace(key)]
			if pair != "" && !mine && !strings.Contains(","+merged+",", ","+pair+",") {
				t.Fatalf("the job's %q is not kept in %q", pair, merged)
			}
		}
	})
}

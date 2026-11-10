package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// gitHubRun states a GitHub Actions pull request run whose event file holds
// repository. A host API is set up to fail the test if anything asks it,
// because the visibility comes from the event and never from a call.
func gitHubRun(t *testing.T, repository string) {
	t.Helper()
	event, err := json.Marshal(map[string]any{
		"pull_request": map[string]any{"number": 7, "head": map[string]string{"sha": headCommit}, "base": map[string]string{"sha": baseCommit}},
		"repository":   json.RawMessage(repository),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, event, 0o600); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the run asked the host %s %s", r.Method, r.URL.Path)
		http.Error(w, "no", http.StatusTeapot)
	}))
	t.Cleanup(host.Close)
	for name, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "example/widgets", "GITHUB_RUN_ID": "1234",
		"GITHUB_SHA": mergeSHA, "GITHUB_EVENT_PATH": path, "GITHUB_EVENT_NAME": "pull_request",
		"GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": host.URL, "GITHUB_TOKEN": "example-token",
	} {
		t.Setenv(name, value)
	}
}

// everyRowsVisibility returns the costroid/repo_visibility of every row in
// path, and fails on a file with no rows.
func everyRowsVisibility(t *testing.T, path string) []any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values []any
	for line := range bytes.Lines(data) {
		var row struct {
			Tags map[string]any `json:"Tags"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		values = append(values, row.Tags["costroid/repo_visibility"])
	}
	if len(values) == 0 {
		t.Fatal("no rows")
	}
	return values
}

// TestRowsCarryRepoVisibilityFromTheEvent runs run and ingest-run on GitHub
// Actions and reads every row's costroid/repo_visibility. It comes from the
// event's repository: its visibility field, else its private field.
func TestRowsCarryRepoVisibilityFromTheEvent(t *testing.T) {
	cases := []struct {
		name, repository, want string
	}{
		{"a public repository", `{"visibility":"public","private":false}`, "public"},
		{"a private repository", `{"visibility":"private","private":true}`, "private"},
		{"an internal repository", `{"visibility":"internal","private":true}`, "internal"},
		{"an event with only private true", `{"private":true}`, "private"},
		{"an event with only private false", `{"private":false}`, "public"},
	}
	// Each command's own setup clears the CI variables, so the run is stated
	// after it.
	commands := map[string]func(t *testing.T, repository, out string) (int, string){
		"run": func(t *testing.T, repository, out string) (int, string) {
			claude := replayClaude(t)
			gitHubRun(t, repository)
			code, _, stderr := execute(t, "run", "--budget-usd", "5", "--out", out, "--", claude, "-p", "Fix the failing test.")
			return code, stderr
		},
		"ingest-run": func(t *testing.T, repository, out string) (int, string) {
			clearCI(t)
			gitHubRun(t, repository)
			code, _, stderr := execute(t, "ingest-run", "--execution-file", executionFile, "--out", out)
			return code, stderr
		},
	}
	for command, start := range commands {
		for _, c := range cases {
			t.Run(command+" in "+c.name, func(t *testing.T) {
				out := filepath.Join(t.TempDir(), "rows.jsonl")
				code, stderr := start(t, c.repository, out)
				if code != 0 {
					t.Fatalf("exit %d: %s", code, stderr)
				}
				for index, value := range everyRowsVisibility(t, out) {
					if value != c.want {
						t.Errorf("row %d: costroid/repo_visibility reads %v, want %s", index+1, value, c.want)
					}
				}
			})
		}
	}
}

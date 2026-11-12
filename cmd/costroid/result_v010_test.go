package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// estimatedRows is the recorded rows file with a FOCUS cost file's rows added
// after them, as the action's step adds them, and the note of a second
// estimate whose file was not there.
func estimatedRows(t *testing.T) string {
	t.Helper()
	rows := writeTemp(t, "costroid-rows.jsonl", readFile(t, realRows))
	if code, _, stderr := execute(t, "ingest-run", "--from", "focus="+writeTemp(t, "infrastructure.csv", estimateFile), "--out", rows); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	missing := filepath.Join(t.TempDir(), "cluster.csv")
	if code, _, stderr := execute(t, "ingest-run", "--from", "focus="+missing, "--source", "cluster-estimate", "--optional", "--out", rows); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	return rows
}

// TestOlderResultsStillRead posts a report with an estimate, whose check run
// carries result/v0.10, and reads it back with costroid result, byte for
// byte. costroid approve then concludes it again and keeps its estimates.
// costroid result reads the newest check run's object of each version from
// 0.6 to 0.9 too.
func TestOlderResultsStillRead(t *testing.T) {
	repository := approvalEnvironment(t)
	setEvent(t, "pull_request", pullEvent(headCommit))
	if code, _, stderr := execute(t, "report", "--rows", estimatedRows(t)); code != 0 || len(repository.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	check := repository.Checks[0]
	object := checkBlock(t, check.Text)
	for _, want := range []string{`"schema": "costroid.result/0.10"`, `"list_cost": "35.008"`, `"source": "cluster-estimate",` + "\n" + `      "reason": "no_file"`} {
		if !strings.Contains(object, want) {
			t.Fatalf("the check run's object lacks %s:\n%s", want, object)
		}
	}
	if objectOf(t, check).Conclusion != "failure" {
		t.Fatalf("the report concluded %s, want failure over the enforced budget", objectOf(t, check).Conclusion)
	}
	if code, stdout, stderr := execute(t, "result", "--pr", "7", "--json"); code != 0 || stdout != object+"\n" {
		t.Errorf("result --json: exit %d, printed\n%s\nthe check run carries\n%s\n%s", code, stdout, object, stderr)
	}

	repository.Reviews = []map[string]any{review(31, approver, "APPROVED", headCommit, time.Now().Add(-time.Hour))}
	if code, stderr := approve(t, reviewEventJSON("submitted", approver, headCommit)); code != 0 || len(repository.Checks) != 2 {
		t.Fatalf("approve: exit %d, %d checks: %s", code, len(repository.Checks), stderr)
	}
	again := checkBlock(t, repository.Checks[1].Text)
	estimates := object[strings.Index(object, `"estimates": [`):strings.Index(object, `"estimates_omitted": 0`)]
	if objectOf(t, repository.Checks[1]).Conclusion != "neutral" || !strings.Contains(again, estimates) || !strings.Contains(again, `"costroid.result/0.10"`) {
		t.Errorf("the approved check lost its estimates or its conclusion:\n%s", again)
	}

	current := strings.Replace(object, `"costroid.result/0.10"`, `"costroid.result/0.9"`, 1)
	current = strings.Replace(current, ",\n  "+estimates+`"estimates_omitted": 0`, "", 1)
	for version, drop := range map[string][]string{
		"0.9": nil,
		"0.8": nil,
		"0.7": {"recorded"},
		"0.6": {"agent_stated", "recorded"},
	} {
		older := strings.Replace(current, `"costroid.result/0.9"`, `"costroid.result/`+version+`"`, 1)
		for _, field := range drop {
			older = strings.Replace(older, ",\n  \""+field+"\": null", "", 1)
		}
		if strings.Contains(older, "estimates") || !strings.Contains(older, version+`"`) {
			t.Fatalf("no %s object was made:\n%s", version, older)
		}
		repository.Checks[1].Text = "```json\n" + older + "\n```"
		if code, stdout, stderr := execute(t, "result", "--pr", "7", "--json"); code != 0 || stdout != older+"\n" {
			t.Errorf("result --json on a %s object: exit %d\n%s%s", version, code, stdout, stderr)
		}
	}
}

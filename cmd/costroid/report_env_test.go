package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
)

// The warnings a report prints about the cap it states.
const (
	emptyBudgetFlag = "costroid: warning: --budget-usd is empty, so the report states no cap. A variable that gives it, such as \"$COSTROID_BUDGET_USD\", reads empty where the job does not set it\n"
	otherCap        = "costroid: warning: --budget-usd 5 is ignored, because the run's result object states a cap of USD 2, and the report states that one. The report reads the cap from that object, so its step needs no budget-usd\n"
	uncappedRun     = "costroid: warning: --budget-usd 5 is ignored, because the run's result object states no cap, so the report states none: it states the cap the run or the read step was given\n"
	unreadCap       = "costroid: warning: the run's result object states a cap that is not an amount in USD above 0, so the report states no cap\n"
)

// TestReportReadsTheCapFromTheRunObject holds the report to the cap the run's
// result object records, whatever its step's budget-usd says. --budget-usd is
// read only beside no object, and an empty one there is no cap, never a cap
// of 0.
func TestReportReadsTheCapFromTheRunObject(t *testing.T) {
	clearCI(t)
	const capped = `{"cap":{"currency":"USD","amount":"2"}}`
	for _, test := range []struct {
		label   string
		args    []string
		object  string
		budget  string
		warning string
	}{
		{"a capped run", nil, capped, "2", ""},
		{"another budget-usd beside a capped run", []string{"--budget-usd", "5"}, capped, "2", otherCap},
		{"the same cap written another way", []string{"--budget-usd", "2.00"}, capped, "2", ""},
		{"an empty flag beside a capped run", []string{"--budget-usd="}, capped, "2", ""},
		{"a run with no cap", nil, `{"cap":null}`, "", ""},
		{"budget-usd beside a run with no cap", []string{"--budget-usd", "5"}, `{"cap":null}`, "", uncappedRun},
		{"an object from before caps were recorded", []string{"--budget-usd", "5"}, `{}`, "", uncappedRun},
		{"a cap in another currency", nil, `{"cap":{"currency":"EUR","amount":"2"}}`, "", unreadCap},
		{"a cap of 0", []string{"--budget-usd", "5"}, `{"cap":{"currency":"USD","amount":"0"}}`, "", unreadCap + uncappedRun},
		{"a cap that is no amount", nil, `{"cap":{"currency":"USD","amount":"two"}}`, "", unreadCap},
		{"no object, and budget-usd", []string{"--budget-usd", "5"}, "", "5", ""},
		{"no object, and an empty flag", []string{"--budget-usd", ""}, "", "", emptyBudgetFlag},
		{"no object, and a blank flag", []string{"--budget-usd", "  "}, "", "", emptyBudgetFlag},
		{"no object, and no flag", nil, "", "", ""},
	} {
		t.Run(test.label, func(t *testing.T) {
			rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
			copyInto(t, realRows, rows)
			if test.object != "" {
				if err := os.WriteFile(runResultPath(rows), []byte(test.object), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"--rows", rows}, test.args...)
			var stderr bytes.Buffer
			flags, err := parseReport(args, &stderr, &stderr)
			if err != nil {
				t.Fatalf("report %q: %v", args, err)
			}
			input := rowsInput(flags, &stderr)
			switch {
			case test.budget == "" && input.Budget != nil:
				t.Errorf("the report reads a cap of USD %s", input.Budget)
			case test.budget != "" && (input.Budget == nil || input.Budget.String() != test.budget):
				t.Errorf("the report reads the cap %v, want USD %s", input.Budget, test.budget)
			}
			object, err := comment.Result(input)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Cap *comment.BlockCap `json:"cap"`
			}
			if err := json.Unmarshal([]byte(object), &decoded); err != nil {
				t.Fatal(err)
			}
			if stated := decoded.Cap != nil; stated != (test.budget != "") || stated && decoded.Cap.Amount != test.budget {
				t.Errorf("the result object states the cap %+v, want %q", decoded.Cap, test.budget)
			}
			if got := stderr.String(); got != test.warning {
				t.Errorf("stderr %q, want %q", got, test.warning)
			}

			code, stdout, printed := execute(t, append([]string{"report", "--preview"}, args...)...)
			if code != 0 {
				t.Fatalf("report --preview: exit %d: %s", code, printed)
			}
			if printed != test.warning {
				t.Errorf("report --preview printed %q, want %q", printed, test.warning)
			}
			if held := strings.Contains(stdout, "Cap: USD "+test.budget+" ("); held != (test.budget != "") {
				t.Errorf("the comment states the cap: %v, want USD %q:\n%s", held, test.budget, stdout)
			}
		})
	}

	t.Run("the objects run and ingest-run write", func(t *testing.T) {
		agent := replayClaude(t)
		dir := t.TempDir()
		ran := filepath.Join(dir, "ran.jsonl")
		if code, _, stderr := execute(t, "run", "--budget-usd", "2", "--out", ran, "--", agent, "-p", "work"); code != 0 {
			t.Fatalf("run: exit %d: %s", code, stderr)
		}
		execution := filepath.Join(dir, "execution.json")
		messages := `[{"type":"system","subtype":"init","session_id":"` + sessionID + `","apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.316"},` +
			`{"type":"result","subtype":"success","is_error":false,"session_id":"` + sessionID + `","modelUsage":{}}]`
		if err := os.WriteFile(execution, []byte(messages), 0o644); err != nil {
			t.Fatal(err)
		}
		read := filepath.Join(dir, "read.jsonl")
		if code, _, stderr := execute(t, "ingest-run", "--execution-file", execution, "--cap-usd", "2.00", "--out", read); code != 0 {
			t.Fatalf("ingest-run: exit %d: %s", code, stderr)
		}
		for _, rows := range []string{ran, read} {
			var stderr bytes.Buffer
			flags, err := parseReport([]string{"--rows", rows, "--budget-usd", "5"}, &stderr, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if input := rowsInput(flags, &stderr); input.Budget == nil || input.Budget.String() != "2" || stderr.String() != otherCap {
				t.Errorf("%s: the report reads the cap %v, want USD 2 with one warning: %s", filepath.Base(rows), input.Budget, stderr.String())
			}
		}
		if code, stdout, _ := execute(t, "report", "--preview", "--rows", ran); code != 0 || !strings.Contains(stdout, "Cap: USD 2 (") {
			t.Errorf("exit %d, and the comment does not state the run's cap of USD 2:\n%s", code, stdout)
		}
	})
}

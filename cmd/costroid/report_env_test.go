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

// The warnings a report prints when an expression gave its budget empty.
const (
	emptyBudgetFlag = "costroid: warning: --budget-usd is empty, so the report states no cap. A variable that gives it, such as \"$COSTROID_BUDGET_USD\", reads empty where the job does not set it\n"
	emptyBudgetStep = "costroid: warning: the run was given a cap and the report's budget-usd is empty, so the report states no cap. An expression such as ${{ env.NAME }} reads empty in a job that does not set NAME: give the report's step the amount, or set NAME in the workflow's env\n"
)

// TestReportReadsAnEmptyBudgetAsNone gives the report a budget that an
// expression resolved empty: --budget-usd given empty or blank, or
// budget-usd empty in the report's step beside a run that was given a cap.
// The report states no cap, never a cap of 0, and one warning names the
// input.
func TestReportReadsAnEmptyBudgetAsNone(t *testing.T) {
	clearCI(t)
	const capped = `{"cap":{"currency":"USD","amount":"5"}}`
	for _, test := range []struct {
		label   string
		args    []string
		object  string
		budget  string
		warning string
	}{
		{"an empty flag", []string{"--budget-usd", ""}, "", "", emptyBudgetFlag},
		{"a blank flag", []string{"--budget-usd", "  "}, "", "", emptyBudgetFlag},
		{"an empty flag beside a capped run", []string{"--budget-usd="}, capped, "", emptyBudgetFlag},
		{"no flag beside a capped run", nil, capped, "", emptyBudgetStep},
		{"no flag beside a run with no cap", nil, `{"cap":null}`, "", ""},
		{"no flag and no run object", nil, "", "", ""},
		{"an amount beside a capped run", []string{"--budget-usd", "5"}, capped, "5", ""},
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
			flags, err := parseReport(args, &stderr)
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
				Cap json.RawMessage `json:"cap"`
			}
			if err := json.Unmarshal([]byte(object), &decoded); err != nil {
				t.Fatal(err)
			}
			if stated := string(decoded.Cap) != "null"; stated != (test.budget != "") {
				t.Errorf("the result object states a cap: %v, want %v", stated, test.budget != "")
			}
			if got := stderr.String(); got != test.warning {
				t.Errorf("stderr %q, want %q", got, test.warning)
			}

			code, _, printed := execute(t, append([]string{"report", "--preview"}, args...)...)
			if code != 0 {
				t.Fatalf("report --preview: exit %d: %s", code, printed)
			}
			if count := strings.Count(printed, "budget-usd"); test.warning != "" && (count != 1 || !strings.Contains(printed, test.warning)) || test.warning == "" && count != 0 {
				t.Errorf("report --preview names budget-usd %d times, want one warning %q: %s", count, test.warning, printed)
			}
		})
	}

	t.Run("the object a read step wrote", func(t *testing.T) {
		execution := filepath.Join(t.TempDir(), "execution.json")
		messages := `[{"type":"system","subtype":"init","session_id":"` + sessionID + `","apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.316"},` +
			`{"type":"result","subtype":"success","is_error":false,"session_id":"` + sessionID + `","modelUsage":{}}]`
		if err := os.WriteFile(execution, []byte(messages), 0o644); err != nil {
			t.Fatal(err)
		}
		rows := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
		if code, _, stderr := execute(t, "ingest-run", "--execution-file", execution, "--cap-usd", "5", "--out", rows); code != 0 {
			t.Fatalf("ingest-run: exit %d: %s", code, stderr)
		}
		code, _, printed := execute(t, "report", "--preview", "--rows", rows)
		if code != 0 || strings.Count(printed, "budget-usd") != 1 || !strings.Contains(printed, emptyBudgetStep) {
			t.Errorf("exit %d, and the report does not warn once that budget-usd is empty: %s", code, printed)
		}
	})
}

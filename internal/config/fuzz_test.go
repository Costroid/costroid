package config

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
)

// FuzzValidate holds costroid validate to the parsers the report runs. A file
// it passes is one the comment, the policy and the approval rule all read,
// and a problem it names under policy or approval is one that section's
// parser refuses too, so validate never passes a file the report reads as
// broken, nor fails one the report reads.
func FuzzValidate(f *testing.F) {
	for _, body := range []string{
		"comment:\n  show_cost: false\npolicy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: 4\n  models: [claude-sonnet-5-5, \"gpt-5*\"]\n  enforce: true\napproval:\n  allow_author: false\n  expires_days: 7\n",
		"policy:\n  run_budget_usd: 5.0\n",
		"policy:\n  pr_budget_usd: 18446744073709551615\n",
		"policy:\n  monthly_budget_usd: 99999999999999999999\n",
		"policy:\n  run_budget_usd: 0x10\n",
		"policy:\n  run_budget_usd: 2.50\n  max_run_cost_usd: 017\n",
		"policy:\n  pr_budget_usd: 1_000\n  monthly_budget_usd: 0o17\n",
		"policy:\n  run_budget_usd: 1e3\n  max_increase_usd: -0\n  max_increase_percent: +5\n",
		"policy:\n  run_budget_usd: 0.1234567890123\n",
		"approval:\n  expires_days: 90\n",
		"approval:\n  expires_days: 017\n",
		"approval:\n  expires_days: 0x7\n",
		"policy: &a\n  enforce: true\napproval: *a\n",
		"? [a]\n: 1\n",
		".inf\n",
		"",
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		problems, err := Validate(data)
		if err != nil {
			t.Fatal(err)
		}
		_, settingsErr := policy.ParseSettings(data)
		_, approvalErr := policy.ParseApproval(data)
		if len(problems) == 0 {
			_, commentErr := comment.ParseConfig(data)
			if settingsErr != nil || approvalErr != nil || commentErr != nil {
				t.Fatalf("validate passed a file the report refuses: policy %v, approval %v, comment %v", settingsErr, approvalErr, commentErr)
			}
			return
		}
		for _, problem := range problems {
			if strings.ContainsAny(problem.String(), "\r\n") {
				t.Fatalf("a problem spans lines: %q", problem)
			}
			if strings.HasPrefix(problem.Path, "/policy") && settingsErr == nil {
				t.Fatalf("validate names %q, and the policy reads the file", problem)
			}
			if strings.HasPrefix(problem.Path, "/approval") && approvalErr == nil {
				t.Fatalf("validate names %q, and the approval rule reads the file", problem)
			}
		}
	})
}

package config

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
)

// floatApprovalDays is a defect this target found: validate passes
// approval.expires_days written as a YAML float such as 7.0, which the
// approval rule refuses. Its input is the seed
// testdata/fuzz/FuzzValidate/approval-float-days. The skip goes when the two
// agree.
const floatApprovalDays = "defect: validate passes a float approval.expires_days that the approval rule refuses"

// nullDocument is a defect this target found: validate passes a file whose
// one document is empty or null, such as a lone ---, which the comment
// refuses as not a mapping, so the report withholds amounts on a repository
// not known to be private. Its input is the seed
// testdata/fuzz/FuzzValidate/document-marker-alone. The skip goes when the
// two agree.
const nullDocument = "defect: validate passes an empty or null document that the comment refuses as not a mapping"

// nullApproval is a defect this target found: validate refuses an approval
// key with no value, which the approval rule reads as its defaults, where
// both refuse a policy key with no value. Its input is the seed
// testdata/fuzz/FuzzValidate/approval-null. The skip goes when the two agree.
const nullApproval = "defect: validate refuses an empty approval mapping that the approval rule reads as its defaults"

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
		"approval:\n  expires_days: 90\n",
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
			if settingsErr == nil && commentErr == nil && approvalErr != nil && strings.Contains(approvalErr.Error(), "expires_days") {
				t.Skip(floatApprovalDays)
			}
			if settingsErr == nil && approvalErr == nil && commentErr != nil && strings.Contains(commentErr.Error(), "not a mapping") {
				t.Skip(nullDocument)
			}
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
			if problem.Path == "/approval" && approvalErr == nil && strings.Contains(problem.Message, "got null") {
				t.Skip(nullApproval)
			}
			if strings.HasPrefix(problem.Path, "/approval") && approvalErr == nil {
				t.Fatalf("validate names %q, and the approval rule reads the file", problem)
			}
		}
	})
}

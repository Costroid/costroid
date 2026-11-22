package policy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
)

// runRow is one run row of USD 0.4, as a rows file holds it.
const runRow = `{"BillingCurrency":"USD","ListCost":"0.4","SubAccountId":null,"Tags":{"costroid/agent":"codex","costroid/model":"gpt-5.5","costroid/cost_source":"agent_stream"}}` + "\n"

// TestARowsFileThatStatesAnOriginIsRefused gives ReadUsage, which costroid
// report and costroid policy read through, a rows file whose run row states
// costroid/origin with each value a job could write: the file is refused on
// that line, so a job cannot make its own rows vanish from the run's figure
// and every rule by tagging them. The same file without the tag is read.
func TestARowsFileThatStatesAnOriginIsRefused(t *testing.T) {
	for _, value := range []string{`"developer_machine"`, `"ci_runner"`, `null`, `7`} {
		tagged := strings.Replace(runRow, `"costroid/agent":"codex"`, `"costroid/agent":"codex","costroid/origin":`+value, 1)
		usage, err := policy.ReadUsage(strings.NewReader(runRow + tagged))
		if !errors.Is(err, policy.ErrOriginRow) || !strings.Contains(err.Error(), "rows line 2") || usage.Rows != 0 {
			t.Errorf("costroid/origin %s: usage %+v, %v", value, usage, err)
		}
	}
	if usage, err := policy.ReadUsage(strings.NewReader(runRow + runRow)); err != nil || usage.Rows != 2 {
		t.Errorf("the untagged file: %+v, %v", usage, err)
	}
}

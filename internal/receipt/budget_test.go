package receipt

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/policy"
)

// thousandDollarReceipt is a receipt of one session whose output tokens the
// pinned catalog prices at USD 1000: 100,000,000 tokens at USD 10 per million.
// Every name and count is synthetic.
func thousandDollarReceipt() Receipt {
	tier := "standard"
	return Receipt{
		Schema: Schema, Branch: "fix/retry-backoff", Written: "2026-11-22", Agents: []string{ClaudeCode},
		Sessions: []Session{{
			Agent: ClaudeCode, AgentVersion: "2.1.319", SessionID: "b81d02e4-6c3a-4f0e-8d5b-19a4c7e2f360",
			Model: "claude-sonnet-5-5", ServiceTier: &tier, FirstDay: "2026-11-21", LastDay: "2026-11-22",
			Requests: 40, LargestInput: "1000",
			Tokens: Tokens{InputUncached: "0", CacheRead: "0", CacheWrite5m: "0", CacheWrite1h: "0", Output: "100000000"},
		}},
	}
}

// receiptRows are the rows the report builds from that receipt, as JSON
// lines, and their ListCost added up in USD.
func receiptRows(t *testing.T) (string, string) {
	t.Helper()
	rows, err := Rows(thousandDollarReceipt(), prices(t), map[string]string{focus.TagPR: "7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines bytes.Buffer
	var total decimal.Decimal
	writer := focus.NewWriter(&lines)
	for _, row := range rows.rows {
		if row.BillingCurrency != "USD" {
			t.Fatalf("a receipt row is in %s", row.BillingCurrency)
		}
		total = total.Add(row.ListCost)
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	return lines.String(), total.String()
}

func verdictOf(t *testing.T, usage policy.Usage, trust policy.Trust, evidence policy.Evidence, settings string) policy.Verdict {
	t.Helper()
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage: usage, Trust: trust, Evidence: evidence,
		Config: policy.ConfigFile{Data: []byte(settings), Found: true},
		At:     time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return verdict
}

func resultOf(t *testing.T, verdict policy.Verdict, rule string) policy.Result {
	t.Helper()
	for _, result := range verdict.Results {
		if result.Rule == rule {
			return result
		}
	}
	t.Fatalf("no %s result in %+v", rule, verdict.Results)
	return policy.Result{}
}

// runRow is one run row of USD 0.4, as a rows file holds it.
const runRow = `{"BillingCurrency":"USD","ListCost":"0.4","SubAccountId":null,"Tags":{"costroid/agent":"codex","costroid/model":"gpt-5.5","costroid/cost_source":"agent_stream"}}` + "\n"

// TestReceiptRowsNeverSatisfyABudget puts the rows the report builds from a
// receipt of USD 1000 into a rows file, beside one run row of USD 0.4 and
// alone: the reader refuses the file each time, so no rule reads a row of
// it. A report gives the policy no usage from a refused file, and under a
// run budget and a run cost limit of USD 0.5, enforced, on rows a ledger
// checked and spend a gateway measured, that reads unproven for no_rows,
// neither passing nor failing.
func TestReceiptRowsNeverSatisfyABudget(t *testing.T) {
	stated, cost := receiptRows(t)
	if cost != "1000" {
		t.Fatalf("the receipt's rows come to USD %s, want 1000", cost)
	}
	for name, file := range map[string]string{"beside a run row": runRow + stated, "alone": stated} {
		usage, err := policy.ReadUsage(strings.NewReader(file))
		if !errors.Is(err, policy.ErrOriginRow) || usage.Rows != 0 || len(usage.Currencies) != 0 {
			t.Errorf("a receipt's rows %s: usage %+v, %v", name, usage, err)
		}
	}
	limits := "policy:\n  run_budget_usd: \"0.5\"\n  max_run_cost_usd: \"0.5\"\n  models: [\"gpt-*\"]\n  enforce: true\n"
	verdict := verdictOf(t, policy.Usage{}, policy.TokenVerified, policy.GatewayMeasured, limits)
	if got := resultOf(t, verdict, policy.RuleRunBudget); verdict.Conclusion == policy.Failure || got.Outcome != policy.Unproven || got.Reason != policy.ReasonNoRows {
		t.Errorf("a refused file's usage: the verdict concludes %s, the budget reads %+v", verdict.Conclusion, got)
	}
}

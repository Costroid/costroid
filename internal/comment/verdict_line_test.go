package comment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/policy"
)

// verdictParagraph is the paragraph that opens with the conclusion.
func verdictParagraph(t *testing.T, body string) string {
	t.Helper()
	_, after, found := strings.Cut(body, "**Verdict: ")
	if !found {
		t.Fatalf("no verdict in\n%s", body)
	}
	paragraph, _, _ := strings.Cut(after, "\n")
	return "**Verdict: " + paragraph
}

// neutralOnAgentRows is the recorded run under a USD 5 run budget, checked
// inside the warn-only period: within the budget, on rows the agent's job
// wrote.
func neutralOnAgentRows(t *testing.T) Input {
	t.Helper()
	return withVerdict(t, goldenVerdict(t, underBudget, policy.Unverified, insidePeriod))
}

// verdictOf is the recorded run's report under one verdict.
func verdictOf(t *testing.T, conclusion string, results ...policy.Result) Input {
	t.Helper()
	input := realInput(t)
	input.Verdict = &policy.Verdict{Conclusion: conclusion, Trust: policy.Unverified, Results: results}
	return input
}

// TestVerdictLineStatesTheFigureAgainstTheBudget reads the verdict line of
// each budget rule that applied: the figure against the limit in amounts a
// reviewer can check, the bare comparison when amounts are withheld, and the
// key that sets a budget when no rule applied. The line sits above the fold,
// in the comment, the check run's summary and the GitLab note alike.
func TestVerdictLineStatesTheFigureAgainstTheBudget(t *testing.T) {
	under := "Run budget: unverified rows cannot satisfy a budget. The run cost USD " + realTotal + " of the USD 5 budget."
	neutral := neutralOnAgentRows(t)
	gitlab := neutral
	gitlab.AccountHost, gitlab.Installation, gitlab.CheckRunID = AccountGitLab, gitlabNamespace, nil
	for name, input := range map[string]Input{"github": neutral, "gitlab": gitlab} {
		body := commentOf(t, input)
		if line := verdictParagraph(t, visiblePart(body)); !strings.Contains(line, under) {
			t.Errorf("%s: the verdict line does not state %q:\n%s", name, under, line)
		}
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		if line := verdictParagraph(t, visiblePart(check.Summary)); !strings.Contains(line, under) {
			t.Errorf("%s: the check's verdict line does not state %q:\n%s", name, under, line)
		}
	}
	if line := verdictParagraph(t, commentOf(t, withheld(neutral))); !strings.Contains(line, "Run budget: unverified rows cannot satisfy a budget. The run is within the budget.") || amountInWords.MatchString(line) {
		t.Errorf("withheld, the verdict line does not state the comparison without an amount:\n%s", line)
	}
	for _, tc := range []struct {
		name   string
		result policy.Result
		want   string
	}{
		{"a threshold", policy.Result{Rule: policy.RuleRunCostThreshold, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "2", CostUSD: "0.5"},
			"Run cost threshold: unverified rows cannot satisfy a budget. The run cost USD 0.5 of the USD 2 threshold."},
		{"a pull request budget", policy.Result{Rule: policy.RulePullRequestBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "10", CostUSD: "3.25"},
			"Pull request budget: unverified rows cannot satisfy a budget. This pull request's runs cost USD 3.25 of the USD 10 budget. The total adds every basis together, at list."},
		{"unattributed rows", policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnattributedRows, LimitUSD: "5", CostUSD: "1"},
			"Run budget: unattributed rows cannot satisfy a budget. The run cost USD 1 of the USD 5 budget."},
		{"over the budget", policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Fail, Reason: policy.ReasonOverLimit, LimitUSD: "0.001", CostUSD: realTotal},
			"Run budget: USD " + realTotal + " is over the USD 0.001 budget."},
		{"within on checked rows", policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Pass, Reason: policy.ReasonWithinLimit, LimitUSD: "5", CostUSD: "0.1"},
			"Run budget: USD 0.1 is within the USD 5 budget."},
		{"a rule that held no figure", policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "5"},
			"Run budget: unverified rows cannot satisfy a budget. The verdict"},
	} {
		if line := verdictParagraph(t, commentOf(t, verdictOf(t, policy.Neutral, tc.result))); !strings.Contains(line, tc.want) {
			t.Errorf("%s: the verdict line does not state %q:\n%s", tc.name, tc.want, line)
		}
	}
	none := withVerdict(t, goldenVerdict(t, "", policy.Unverified, insidePeriod))
	want := "No rule applied: `costroid.yml` on the base branch sets no budget and no threshold. Set `policy.run_budget_usd` to hold each run to a budget."
	if line := verdictParagraph(t, commentOf(t, none)); !strings.Contains(line, want) {
		t.Errorf("with no rule, the verdict line does not name the key that sets a budget:\n%s", line)
	}
}

// policyLink is the link the verdict line gives to the page that says what
// lets a rule pass.
const policyLink = "[The policy page](https://docs.costroid.com/policy)"

// TestNeutralVerdictSaysWhatWouldLetItPass holds a neutral verdict on rows
// the agent's own job wrote to one clause that says so, the rule for a run
// over budget as it stands, and one link to the policy page. Any other
// verdict carries none of it: a rule is never said to pass on those rows.
func TestNeutralVerdictSaysWhatWouldLetItPass(t *testing.T) {
	clause := "The verdict is neutral because the agent's own job wrote the rows."
	inside := neutralOnAgentRows(t)
	after := withVerdict(t, goldenVerdict(t, underBudget, policy.Unverified, afterPeriod))
	for name, tc := range map[string]struct {
		input Input
		over  string
	}{
		"inside the warn-only period": {inside, "Over budget, the rule would warn until the warn-only period ends, then fail."},
		"after the warn-only period":  {after, "Over budget, the rule would fail."},
		"amounts withheld":            {withheld(inside), "Over budget, the rule would warn until the warn-only period ends, then fail."},
	} {
		line := verdictParagraph(t, visiblePart(commentOf(t, tc.input)))
		for _, want := range []string{clause, tc.over, policyLink + " says what lets a budget pass."} {
			if strings.Count(line, want) != 1 {
				t.Errorf("%s: the verdict line does not state %q once:\n%s", name, want, line)
			}
		}
		if strings.Contains(line, "Warn-only") {
			t.Errorf("%s: a verdict with nothing over budget names the period as holding a failure:\n%s", name, line)
		}
		check, err := Check(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(check.Summary, clause) || !strings.Contains(check.Summary, policyLink) {
			t.Errorf("%s: the check's summary does not say why it is neutral:\n%s", name, check.Summary)
		}
	}
	allowlist := verdictOf(t, policy.Neutral, policy.Result{Rule: policy.RuleModelAllowlist, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows})
	if line := verdictParagraph(t, commentOf(t, allowlist)); !strings.Contains(line, clause) || !strings.Contains(line, policyLink+" says what lets a rule pass.") || strings.Contains(line, "Over budget") {
		t.Errorf("a model allowlist on the agent's rows:\n%s", line)
	}
	for name, input := range map[string]Input{
		"a pass":            goldenStates(t)["pass-shown"],
		"a warning":         withVerdict(t, goldenVerdict(t, overBudget, policy.Unverified, insidePeriod)),
		"a failure":         withVerdict(t, goldenVerdict(t, overBudget, policy.Unverified, afterPeriod)),
		"no rule":           withVerdict(t, goldenVerdict(t, "", policy.Unverified, insidePeriod)),
		"no policy":         realInput(t),
		"another unproven":  verdictOf(t, policy.Neutral, policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "5", CostUSD: "1"}, policy.Result{Rule: policy.RuleRunIncrease, Outcome: policy.Unproven, Reason: policy.ReasonNoBaseline, LimitUSD: "1"}),
		"an invalid config": goldenStates(t)["preview-config-invalid"],
	} {
		body := commentOf(t, input)
		if strings.Contains(body, clause) || strings.Contains(body, "docs.costroid.com/policy") {
			t.Errorf("%s: the comment says the agent's rows alone kept the check neutral:\n%s", name, visiblePart(body))
		}
	}
	for name, input := range goldenStates(t) {
		if body := commentOf(t, input); strings.Contains(body, clause) && input.Conclusion() != policy.Neutral {
			t.Errorf("%s concludes %s and says it is neutral", name, input.Conclusion())
		}
	}
}

// blockTrust is the trust the machine block's policy states, empty when no
// policy ran.
func blockTrust(t *testing.T, body string) policy.Trust {
	t.Helper()
	block := fencedBlock.FindString(body)
	var object Block
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(block, "```json\n"), "\n```")), &object); err != nil {
		t.Fatalf("the machine block does not parse: %v\n%s", err, block)
	}
	if object.Policy == nil {
		return ""
	}
	return object.Policy.Trust
}

// TestUnverifiedLineFollowsTheRowsTrust prints the line on who vouched for
// the rows from the trust the verdict states, the one the machine block's
// policy.trust carries: verified rows never read as the agent's own, and rows
// with no verdict read as the rows file's, which the agent's job wrote. Every
// golden state's comment agrees with its own machine block.
func TestUnverifiedLineFollowsTheRowsTrust(t *testing.T) {
	states := goldenStates(t)
	verified := states["pass-shown"]
	if verified.Verdict.Trust != policy.TokenVerified {
		t.Fatalf("the pass state's verdict reads %s", verified.Verdict.Trust)
	}
	for _, input := range []Input{verified, withheld(verified)} {
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := Summary(input)
		if err != nil {
			t.Fatal(err)
		}
		for where, text := range map[string]string{"comment": visiblePart(commentOf(t, input)), "check": check.Summary, "job summary": summary} {
			if strings.Contains(text, unverifiedLine) || !strings.Contains(text, verifiedLine) {
				t.Errorf("verified rows: the %s does not say what verified them:\n%s", where, text)
			}
		}
	}
	for name, input := range states {
		body := commentOf(t, input)
		trust := blockTrust(t, body)
		switch {
		case input.State != Reported:
			if strings.Contains(body, unverifiedLine) || strings.Contains(body, verifiedLine) {
				t.Errorf("%s labels figures it does not show", name)
			}
		case trust == policy.TokenVerified:
			if strings.Contains(body, unverifiedLine) || !strings.Contains(visiblePart(body), verifiedLine) {
				t.Errorf("%s: the block reads %s, the comment does not:\n%s", name, trust, visiblePart(body))
			}
		default:
			if strings.Contains(body, verifiedLine) || !strings.Contains(visiblePart(body), unverifiedLine) {
				t.Errorf("%s: the block reads %q, the comment does not:\n%s", name, trust, visiblePart(body))
			}
		}
	}
}

// TestCommentNamesTheCatalogDate names each catalog the rows were priced
// with beside the day its sheets were read, from the sheets the binary
// carries, and no day for a catalog it does not carry.
func TestCommentNamesTheCatalogDate(t *testing.T) {
	day := func(version string) string {
		read, known := catalog.PricesRead(version)
		if !known {
			t.Fatalf("the binary carries no catalog %s", version)
		}
		return read
	}
	if day("0.3.0") != "2026-11-08" || day("0.1.0") != "2026-11-01" {
		t.Fatalf("the sheets read 0.3.0 on %s and 0.1.0 on %s", day("0.3.0"), day("0.1.0"))
	}
	two, err := ReadRows(strings.NewReader(sampleFields().line() + "\n" + strings.Replace(sampleFields().line(), `"0.1.0"`, `"0.3.0"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	unknown := ledgerOf(t, sampleFields())
	unknown.Catalogs = []Data{"9.9.9"}
	for name, tc := range map[string]struct {
		ledger Ledger
		want   string
	}{
		"the pinned release":    {realLedger(t), "19 rows, catalog `0.4.1`, prices read 2026-11-12."},
		"an earlier release":    {ledgerOf(t, sampleFields()), "1 row, catalog `0.1.0`, prices read 2026-11-01."},
		"two releases":          {two, "2 rows, catalogs `0.1.0` (prices read 2026-11-01) and `0.3.0` (prices read 2026-11-08)."},
		"a release not carried": {unknown, "1 row, catalog `9.9.9`."},
	} {
		input := realInput(t)
		input.Ledger = tc.ledger
		for _, amounts := range []Amounts{AmountsShown, AmountsOff} {
			input.Amounts = amounts
			if body := commentOf(t, input); !strings.Contains(body, tc.want) {
				t.Errorf("%s, amounts %d: no %q in\n%s", name, amounts, tc.want, body)
			}
		}
	}
}

// TestWhatAReviewerNeedsStaysAboveTheFold holds each line a reviewer judges
// the pull request by outside every folded block: a warning, a failure
// reason, an agent's error, a withheld-amounts notice, a fork's or a skipped
// agent's notice, a reconciliation, a subscription mark, an agent's own
// figure, a fallback price, a recorded run and a run over its cap.
func TestWhatAReviewerNeedsStaysAboveTheFold(t *testing.T) {
	states := goldenStates(t)
	for name, wants := range map[string][]string{
		"warn-shown":                 {"Warn-only until 2026-11-09 09:00 UTC", "is over the USD 0.001 budget."},
		"fail-shown":                 {"**Verdict: failure.** Run budget: USD " + realTotal + " is over the USD 0.001 budget."},
		"agent-error-beside-figures": {"The agent ended with `rate_limit`"},
		"empty-run-agent_error":      {"Reason `agent_error`"},
		"neutral-withheld":           {"Amounts are off on this public repository"},
		"withheld-unsettled":         {"Amounts are withheld, because"},
		"no-figures-fork_no_rows":    {"No rows file reached this report"},
		"no-figures-agent_skipped":   {"The agent did not run on this pull request"},
		"reconciliation":             {"Last reconciliation of `apikey_02Example`: 2026-11-05, a token class missed"},
		"subscription":               {"; subscription: list-equivalent, no provider report to reconcile"},
		"gh-aw-stated-credits":       {"gh-aw stated 7.47 AI credits for this run."},
		"opencode-stated-usd":        {"OpenCode stated USD 0.00153317 for this run"},
		"fallback-price":             {"Fallback price: `gpt-9-preview`", "Over the cap by USD 175.1 and 1000000 tokens."},
		"codex-stopped-overshoot":    {"Over the cap by USD 0.0273136 and 16960 tokens."},
		"recorded-run":               {"These figures are a recorded run's, workflow run 42368537950"},
		"lifetime":                   {"Across this pull request: USD 0.00323945"},
		"figures-omitted":            {"3 more lines are left out of this table."},
		"gitlab-note-withheld":       {"Amounts are off on this public repository (`comment.show_cost: false`). The rows carry them."},
	} {
		visible := visiblePart(commentOf(t, states[name]))
		if name == "lifetime" {
			body, err := CommentWithLifetime(states[name], states[name].Lifetime)
			if err != nil {
				t.Fatal(err)
			}
			visible = visiblePart(body)
		}
		for _, want := range wants {
			if !strings.Contains(visible, want) {
				t.Errorf("%s: %q is folded or gone:\n%s", name, want, visible)
			}
		}
	}
}

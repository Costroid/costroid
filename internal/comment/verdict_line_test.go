package comment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/policy"
)

// sentenceOf is the verdict sentence as a comment or a check's summary opens
// with it: the first line after the marker, without its emphasis.
func sentenceOf(t *testing.T, body string) string {
	t.Helper()
	line, _, _ := strings.Cut(strings.TrimPrefix(body, Marker+"\n"), "\n")
	sentence, emphasised := strings.CutPrefix(line, "**")
	sentence, closed := strings.CutSuffix(sentence, "**")
	if !emphasised || !closed {
		t.Fatalf("no verdict sentence opens\n%s", body)
	}
	return sentence
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

// TestSentenceStatesTheFigureAgainstTheBudget reads the verdict sentence of
// each budget rule that decided: the figure against the limit in amounts a
// reviewer can check, the bare comparison when amounts are withheld, and
// under the fold the key that sets a budget when no rule applied. The
// sentence opens the comment, the check run's summary and the GitLab note
// alike.
func TestSentenceStatesTheFigureAgainstTheBudget(t *testing.T) {
	figure := "this run cost USD " + realTotal + " at list (List-equivalent, not billed)"
	under := "Within budget, by the job's own figures: " + figure + " of the USD 5.00 run budget."
	neutral := neutralOnAgentRows(t)
	gitlab := neutral
	gitlab.AccountHost, gitlab.Installation, gitlab.CheckRunID = AccountGitLab, gitlabNamespace, nil
	for name, input := range map[string]Input{"github": neutral, "gitlab": gitlab} {
		if sentence := sentenceOf(t, commentOf(t, input)); !strings.HasPrefix(sentence, under) {
			t.Errorf("%s: the sentence does not state %q:\n%s", name, under, sentence)
		}
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		if sentence := sentenceOf(t, check.Summary); !strings.HasPrefix(sentence, under) || sentence != check.Title {
			t.Errorf("%s: the check's summary does not open on its title, or the title does not state %q:\n%s", name, under, sentence)
		}
	}
	if sentence := sentenceOf(t, commentOf(t, withheld(neutral))); !strings.Contains(sentence, "this run is within its run budget, priced at list (List-equivalent, not billed).") || amountInWords.MatchString(sentence) {
		t.Errorf("withheld, the sentence does not state the comparison without an amount:\n%s", sentence)
	}
	for _, tc := range []struct {
		name       string
		conclusion string
		result     policy.Result
		want, line string
	}{
		{"a limit", policy.Neutral, policy.Result{Rule: policy.RuleRunCostThreshold, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "2", CostUSD: "0.5"},
			"Within limit, by the job's own figures: " + figure + " of the USD 2.00 run cost limit.", ""},
		{"a pull request budget", policy.Neutral, policy.Result{Rule: policy.RulePullRequestBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, LimitUSD: "10", CostUSD: "3.25"},
			"Within budget, by the job's own figures: this pull request's runs cost USD 3.25 at list (every basis together) of the USD 10.00 pull request budget.", ""},
		{"unattributed rows", policy.Neutral, policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnattributedRows, LimitUSD: "5", CostUSD: "1"},
			"Not decided: the run budget could not be checked. This run cost USD " + realTotal + " at list (List-equivalent, not billed).",
			"Run budget: unattributed rows cannot satisfy a budget. The run cost USD 1.00 at list (List-equivalent, not billed) of the USD 5.00 budget."},
		{"over the budget", policy.Failure, policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Fail, Reason: policy.ReasonOverLimit, LimitUSD: "0.001", CostUSD: realTotal},
			"Over budget: " + figure + ", over the USD 0.001 run budget.", ""},
		{"within on checked rows", policy.Success, policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Pass, Reason: policy.ReasonWithinLimit, LimitUSD: "5", CostUSD: "0.1"},
			"Within budget: " + figure + " of the USD 5.00 run budget. A ledger checked the rows against the job's OIDC token.", ""},
		{"a rule that held no figure", policy.Neutral, policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows},
			"Within budget, by the job's own figures: " + figure + ", within its run budget.", ""},
	} {
		body := commentOf(t, verdictOf(t, tc.conclusion, tc.result))
		if sentence := sentenceOf(t, body); !strings.HasPrefix(sentence, tc.want) {
			t.Errorf("%s: the sentence does not state %q:\n%s", tc.name, tc.want, sentence)
		}
		if above, _, _ := strings.Cut(body, "### Costroid"); (tc.line != "" && !strings.Contains(above, "\n"+tc.line+"\n")) || (tc.line == "" && strings.Contains(above, ruleLabel(tc.result.Rule, RequestNoun(AccountGitHub))+": ")) {
			t.Errorf("%s: the rule's own line follows only a sentence that leaves it unsaid, want %q:\n%s", tc.name, tc.line, above)
		}
	}
	none := withVerdict(t, goldenVerdict(t, "", policy.Unverified, insidePeriod))
	want := "No rule applied: `costroid.yml` on the base branch sets no budget and no limit. Set `policy.run_budget_usd` to hold each run to a budget."
	body := commentOf(t, none)
	if !strings.HasPrefix(sentenceOf(t, body), "No budget set: ") || !strings.Contains(body, want) || strings.Contains(visiblePart(body), want) {
		t.Errorf("with no rule, the sentence does not say so, or the fold does not name the key that sets a budget:\n%s", body)
	}
}

// policyLink is the link the comment gives to the page that says what lets a
// rule pass.
const policyLink = "[The policy page](https://docs.costroid.com/policy)"

// TestAFailingCheckNamesItsNextStepInOneSentenceWithOneLink holds every golden
// state whose check fails to a next step above the evidence, in the comment,
// the check's summary and the job summary: one sentence, with the one link
// that stands there. A failed rule's step is the approval
// page. A refused artifact's is not, because no approval changes that check:
// its sentence says so and links the section of the troubleshooting page that
// holds the artifact's line, which this test finds on the page.
func TestAFailingCheckNamesItsNextStepInOneSentenceWithOneLink(t *testing.T) {
	approve := "[The approval page](https://docs.costroid.com/approval) says how a reviewer approves a failed check."
	refused := "An approval does not change this check: find the step in the agent's job that wrote those entries, as [the troubleshooting page](https://docs.costroid.com/troubleshooting#installing-the-binary) says."
	steps := map[string]int{}
	for name, input := range goldenStates(t) {
		if input.Conclusion() != policy.Failure {
			continue
		}
		want := approve
		if input.Refused != nil {
			want = refused
		}
		r := renderState(t, input)
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		for surface, text := range map[string]string{"comment": r.comment, "check's summary": check.Summary, "job summary": r.summary} {
			if step, links := nextStepOf(text); step != want || links != 1 || !oneSentence(step) {
				t.Errorf("%s: the %s names the next step as %q, with %d links above the heading", name, surface, step, links)
			}
		}
		steps[want]++
	}
	if steps[approve] == 0 || steps[refused] == 0 {
		t.Fatalf("the golden states that fail name these steps: %v", steps)
	}

	// An approval of the head does not change a refused artifact's check.
	approved := goldenStates(t)["artifact-refused"]
	approved.Approval = &Approval{Approver: "octocat", HeadSHA: sampleHead, At: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)}
	approved.Verdict = &policy.Verdict{Conclusion: policy.Failure}
	if step, _ := nextStepOf(commentOf(t, approved)); approved.Conclusion() != policy.Failure || step != refused {
		t.Errorf("a refused artifact under an approval concludes %s and names the step %q", approved.Conclusion(), step)
	}

	// The link's anchor is a heading of the troubleshooting page, as GitHub
	// names it, and the section under that heading holds this failure: the
	// line the report's step prints for a refused artifact, the check it
	// posts, and the fix the sentence sends the reader to.
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "troubleshooting.md"))
	if err != nil {
		t.Fatal(err)
	}
	address, anchor, linked := strings.Cut(refusedPage, "#")
	var section string
	for heading := range strings.SplitSeq(string(page), "\n## ") {
		title, body, _ := strings.Cut(heading, "\n")
		if githubAnchor(title) == anchor {
			section, _, _ = strings.Cut(body, "\n## ")
		}
	}
	if !linked || address != "https://docs.costroid.com/troubleshooting" || section == "" {
		t.Fatalf("%s names no heading of docs/troubleshooting.md", refusedPage)
	}
	for _, held := range []string{
		"so the report reads none of it and fails its check: <entries>`",
		"posts a `failure` check with the run reason `artifact_refused`",
		"Find the step in the agent's job that wrote into the artifact",
	} {
		if !strings.Contains(section, held) {
			t.Errorf("the section %s links does not hold the refused artifact's row: no %q", refusedPage, held)
		}
	}
}

// githubAnchor is the anchor GitHub gives a heading: lower case, its spaces
// as hyphens, and nothing but letters, digits, hyphens and underscores.
func githubAnchor(heading string) string {
	var anchor strings.Builder
	for _, letter := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case letter == ' ':
			anchor.WriteRune('-')
		case letter == '-' || letter == '_' || (letter >= 'a' && letter <= 'z') || (letter >= '0' && letter <= '9'):
			anchor.WriteRune(letter)
		}
	}
	return anchor.String()
}

// TestNeutralVerdictSaysWhatWouldLetItPass holds a neutral verdict on rows
// the agent's own job wrote to one clause of its sentence that says so, and
// under the fold the rule for a run over budget as it stands and one link to
// the policy page. Any other verdict carries none of it: a rule is never said
// to pass on those rows.
func TestNeutralVerdictSaysWhatWouldLetItPass(t *testing.T) {
	clause := "Nothing outside the job checked the figures, so the check is neutral."
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
		body := commentOf(t, tc.input)
		if sentence := sentenceOf(t, body); strings.Count(sentence, clause) != 1 {
			t.Errorf("%s: the sentence does not say why the check is neutral, once:\n%s", name, sentence)
		}
		for _, want := range []string{tc.over, policyLink + " says what lets a budget pass."} {
			if strings.Count(body, want) != 1 || strings.Contains(visiblePart(body), want) {
				t.Errorf("%s: the fold does not state %q once:\n%s", name, want, body)
			}
		}
		if strings.Contains(body, "Warn-only") {
			t.Errorf("%s: a verdict with nothing over budget names the period as holding a failure:\n%s", name, body)
		}
		check, err := Check(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(check.Title, clause) || !strings.Contains(check.Summary, policyLink) {
			t.Errorf("%s: the check does not say why it is neutral:\n%s\n%s", name, check.Title, check.Summary)
		}
	}
	allowlist := verdictOf(t, policy.Neutral, policy.Result{Rule: policy.RuleModelAllowlist, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows})
	if body := commentOf(t, allowlist); !strings.Contains(body, policyLink+" says what lets a rule pass.") || strings.Contains(body, "Over budget") {
		t.Errorf("a model allowlist on the agent's rows:\n%s", body)
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
		if strings.Contains(body, policyLink) || (strings.Contains(body, clause) && name != "another unproven") {
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

// TestWhoVouchedForTheRowsIsSaidOnce holds every golden state that shows
// figures to one statement of who vouched for them, on each surface: the
// closing clause of the sentence where the sentence has one, and the line
// under the sentence everywhere else. Never both, which says one thing twice,
// and never neither. That holds of rows the agent's job wrote and of rows a
// ledger checked against the job's token. A sentence cut to the status limit
// loses its closing clause, and the line then stands under it.
func TestWhoVouchedForTheRowsIsSaidOnce(t *testing.T) {
	saidOnce := func(name string, input Input) (inSentence bool) {
		t.Helper()
		closing, line := "Nothing outside the job checked the figures, so the check is neutral.", "Unchecked: the agent's own job wrote these figures, and nothing outside it checked them."
		if input.Verdict != nil && input.Verdict.Trust == policy.TokenVerified {
			closing, line = "A ledger checked the rows against the job's OIDC token.", "Checked: a ledger checked the rows the verdict read against the job's OIDC token."
		}
		r := renderState(t, input)
		check, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		inSentence = strings.HasSuffix(strings.TrimSuffix(check.Title, " Amounts are withheld here."), closing)
		for surface, text := range map[string]string{"comment": readable(r.comment), "check's summary": check.Summary, "job summary": r.summary} {
			if closings, lines := strings.Count(text, closing), strings.Count(text, line); closings+lines != 1 || (closings == 1) != inSentence {
				t.Errorf("%s: the %s says who vouched for the rows %d times in its sentence and %d times under it:\n%s", name, surface, closings, lines, visiblePart(text))
			}
			if inSentence && !strings.HasPrefix(strings.TrimPrefix(text, Marker+"\n"), "**"+check.Title+"**\n\n") {
				t.Errorf("%s: the %s does not say it in its first line", name, surface)
			}
		}
		return inSentence
	}
	sentences, lines, checked := 0, 0, 0
	for name, input := range goldenStates(t) {
		if input.State != Reported {
			continue
		}
		inSentence := saidOnce(name, input)
		switch {
		case input.Verdict != nil && input.Verdict.Trust == policy.TokenVerified && inSentence:
			checked++
		case inSentence:
			sentences++
		default:
			lines++
		}
	}
	if sentences == 0 || lines == 0 || checked == 0 {
		t.Fatalf("%d golden states say it in the sentence, %d under it, and %d say in the sentence that a ledger checked the rows", sentences, lines, checked)
	}
	// Rows a ledger checked under a sentence that does not close on it keep
	// the line.
	unruled := goldenStates(t)["pass-shown"]
	passed := *unruled.Verdict
	passed.Results = nil
	unruled.Verdict = &passed
	if saidOnce("checked rows and no rule", unruled) {
		t.Error("a sentence with no rule closes on who checked the rows")
	}

	var rows []rowFields
	for _, currency := range []string{"XAA", "XAB", "XAC"} {
		fields := sampleFields()
		fields.currency = currency
		rows = append(rows, fields)
	}
	cut := realInput(t)
	cut.Ledger = ledgerOf(t, rows...)
	within := policy.Result{Rule: policy.RuleRunBudget, Outcome: policy.Unproven, Reason: policy.ReasonUnverifiedRows, CostUSD: "1", LimitUSD: "5"}
	cut.Verdict = &policy.Verdict{Conclusion: policy.Neutral, Results: []policy.Result{within}}
	if !saidOnce("a sentence that fits whole", cut) {
		t.Error("a sentence that fits whole does not close on who vouched for the rows")
	}
	within.LimitUSD = strings.Repeat("5", 90)
	cut.Verdict = &policy.Verdict{Conclusion: policy.Neutral, Results: []policy.Result{within}}
	if saidOnce("a sentence cut to the status limit", cut) {
		t.Error("a sentence with a budget of 90 digits kept its closing clause")
	}
}

// TestUncheckedLineFollowsTheRowsTrust prints the line on who vouched for
// the rows from the trust the verdict states, the one the machine block's
// policy.trust carries: verified rows never read as the agent's own, and rows
// with no verdict read as the rows file's, which the agent's job wrote. Every
// golden state's comment agrees with its own machine block. Where the
// sentence itself says nothing outside the job checked the figures, it stands
// for the line.
func TestUncheckedLineFollowsTheRowsTrust(t *testing.T) {
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
			if strings.Contains(text, uncheckedLine) || strings.Count(text, checkedClosing) != 1 || strings.Contains(text, checkedLine) {
				t.Errorf("verified rows: the %s does not say once, in its sentence, what verified them:\n%s", where, text)
			}
		}
	}
	for name, input := range states {
		body := commentOf(t, input)
		trust := blockTrust(t, body)
		switch {
		case input.State != Reported:
			if strings.Contains(body, uncheckedLine) || strings.Contains(body, checkedLine) {
				t.Errorf("%s labels figures it does not show", name)
			}
		case trust == policy.TokenVerified:
			if strings.Contains(body, uncheckedLine) || !(strings.Contains(visiblePart(body), checkedLine) || strings.Contains(sentenceOf(t, body), checkedClosing)) {
				t.Errorf("%s: the block reads %s, the comment does not:\n%s", name, trust, visiblePart(body))
			}
		default:
			if strings.Contains(body, checkedLine) || !(strings.Contains(visiblePart(body), uncheckedLine) || strings.Contains(sentenceOf(t, body), uncheckedClosing)) {
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
		"the pinned release":    {realLedger(t), "19 rows, catalog `0.4.3`, prices read 2026-11-21."},
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
// figure, a fallback price, a recorded run, a run over its cap, an approval,
// what a refused artifact held, a second rule that failed and how a failure
// is approved. Each says how far to trust this run's figure or what to do
// about its verdict. A figure that is not this run's is folded, as
// TestWhatStandsOpenIsCountedInEveryGoldenState holds.
func TestWhatAReviewerNeedsStaysAboveTheFold(t *testing.T) {
	states := goldenStates(t)
	for name, wants := range map[string][]string{
		"warn-shown":                 {"Warn-only until 2026-11-09 09:00 UTC", ", over the USD 0.001 run budget."},
		"fail-shown":                 {"**Over budget: this run cost USD " + realTotal + " at list (List-equivalent, not billed), over the USD 0.001 run budget.**"},
		"agent-error-beside-figures": {"The agent ended with `rate_limit`"},
		"empty-run-agent_error":      {"No figure (agent_error): ", "It ended with `authentication_failed`: "},
		"neutral-withheld":           {"Amounts are off on this public repository"},
		"withheld-unsettled":         {"Amounts are withheld, because"},
		"no-figures-fork_no_rows":    {"No figure (fork_no_rows): this pull request comes from a fork, and its workflow run left no rows."},
		"no-figures-agent_skipped":   {"No agent run: the workflow skipped the agent's job", "The recipes skip the agent's job for a first-time contributor"},
		"reconciliation":             {"Last reconciliation of `apikey_02Example`: 2026-11-05, a token class missed"},
		"subscription":               {"| USD 0.0302309, subscription |", "This run signed in on a subscription: list-equivalent, no provider report to reconcile."},
		"gh-aw-stated-credits":       {"gh-aw stated 7.47 AI credits for this run."},
		"opencode-stated-usd":        {"OpenCode stated USD 0.00153317 for this run"},
		"fallback-price":             {"Fallback price: `gpt-9-preview`", "USD 175.10 over its USD 5.00 cap.", "USD 175.10 and 1000000 tokens over the cap."},
		"codex-stopped-overshoot":    {"USD 0.0273136 over its USD 0.001 cap.", "USD 0.0273136 and 16960 tokens over the cap."},
		"approved-failure":           {"`octocat` approved commit `9f2c6e1` at 2026-11-12 09:30 UTC, so the check concludes neutral."},
		"artifact-refused":           {"The `costroid-rows` artifact held 2 entries besides the rows and the run object: `notes.txt` and `extra/rows.jsonl`."},
		"two-rules-fail":             {"Model allowlist: the run used `claude-haiku-5-5`, which is not on the list.", "[The approval page](https://docs.costroid.com/approval) says how a reviewer approves a failed check."},
		"recorded-run":               {"These figures are a recorded run's, workflow run 42368537950"},
		"figures-omitted":            {"3 more lines are left out of this table."},
		"gitlab-note-withheld":       {"Amounts are off on this public repository (`comment.show_cost: false`). The rows carry them."},
	} {
		visible := visiblePart(commentOf(t, states[name]))
		for _, want := range wants {
			if !strings.Contains(visible, want) {
				t.Errorf("%s: %q is folded or gone:\n%s", name, want, visible)
			}
		}
	}
}

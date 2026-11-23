package comment

import (
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/accuracy"
	"github.com/Costroid/costroid/internal/focus"
)

// The accuracy line's three states, as the golden record gives them.
const (
	accuracyCovered    = "Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 8 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count. [The accuracy page](https://docs.costroid.com/accuracy) lists every day. Nothing has compared this run with a provider's report."
	accuracyNotCovered = "Costroid's own test runs `claude-code` and `opencode` on Anthropic, not this run's agent and provider, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report."
	accuracyNoDay      = "Costroid's own test, not this run: this build of Costroid holds no day of it. [The accuracy page](https://docs.costroid.com/accuracy) lists the days. Nothing has compared this run with a provider's report."
)

// claudeCode is the agent of the page's every day, at its one version.
var claudeCode = accuracy.Agent{Name: "claude-code", Version: "2.1.316"}

// goldenRecord is the record as the accuracy page stood on 2026-11-23: 14
// days on Anthropic, 2026-11-06 to 2026-11-19, the first six failed, and
// OpenCode beside Claude Code on 2026-11-12. The goldens read it, so a new
// page moves no golden.
func goldenRecord() *accuracy.Record {
	var record accuracy.Record
	for day := 6; day <= 19; day++ {
		line := accuracy.Line{Day: fmt.Sprintf("2026-11-%02d", day), Sources: []string{"anthropic"}, Agents: []accuracy.Agent{claudeCode}, Verdict: accuracy.VerdictPass, TokenError: "0"}
		if day < 12 {
			line.Verdict, line.TokenError = "token miss, class dollar miss", "239150/6567"
		}
		if day == 12 {
			line.Agents = append(line.Agents, accuracy.Agent{Name: "opencode", Version: "1.19.4"})
		}
		record.Lines = append(record.Lines, line)
	}
	return &record
}

// accuracyStates are the line's golden states beside the neutral report on
// Claude Code's rows: covered, shown and withheld, a run of Codex on OpenAI,
// and a build whose record holds no day.
func accuracyStates(t *testing.T, neutral Input) map[string]Input {
	t.Helper()
	notCovered := realInput(t)
	notCovered.Ledger = ledgerOf(t, sampleFields())
	noDay := neutral
	noDay.Accuracy = &accuracy.Record{}
	return map[string]Input{
		"accuracy-covered":          neutral,
		"accuracy-covered-withheld": withheld(neutral),
		"accuracy-not-covered":      notCovered,
		"accuracy-no-day":           noDay,
	}
}

// claudeCodeFields is a Claude Code row of one million output tokens at
// Anthropic's price.
func claudeCodeFields() rowFields {
	fields := sampleFields()
	fields.agent, fields.model = "claude-code", "claude-sonnet-5"
	fields.priceID, fields.description = "anthropic:claude-sonnet-5:global:standard:all:output", "claude-sonnet-5 output tokens"
	return fields
}

// accuracyOf is the line for a run of these rows under a record.
func accuracyOf(t *testing.T, record *accuracy.Record, rows ...rowFields) string {
	t.Helper()
	input := realInput(t)
	input.Ledger, input.Accuracy = ledgerOf(t, rows...), record
	return input.accuracyLine()
}

// window is 14 days of Claude Code on Anthropic from 2026-11-06, each passed
// or failed as passed says, with its largest token error.
func window(passed func(day int) bool, tokenError string) *accuracy.Record {
	var record accuracy.Record
	for day := range 14 {
		line := accuracy.Line{Day: fmt.Sprintf("2026-11-%02d", 6+day), Sources: []string{"anthropic"}, Agents: []accuracy.Agent{claudeCode}, Verdict: "token miss", TokenError: "100"}
		if passed(day) {
			line.Verdict, line.TokenError = accuracy.VerdictPass, tokenError
		}
		record.Lines = append(record.Lines, line)
	}
	return &record
}

func TestTheAccuracyLineSaysNothingComparedThisRun(t *testing.T) {
	for name, c := range map[string]struct {
		record *accuracy.Record
		rows   rowFields
		want   string
	}{
		"covered":     {goldenRecord(), claudeCodeFields(), accuracyCovered},
		"not covered": {goldenRecord(), sampleFields(), accuracyNotCovered},
		"no day":      {&accuracy.Record{}, claudeCodeFields(), accuracyNoDay},
	} {
		got := accuracyOf(t, c.record, c.rows)
		if got != c.want {
			t.Errorf("%s: the line reads\n%s\nwant\n%s", name, got, c.want)
		}
		opening, _, _ := strings.Cut(got, ". ")
		if !strings.Contains(opening, "not this run") || !strings.HasSuffix(got, " Nothing has compared this run with a provider's report.") {
			t.Errorf("%s: the line does not say in its opening that the test is not this run, or does not close on what compared it:\n%s", name, got)
		}
	}
}

// TestTheAccuracyLineCountsEveryDayItRan counts the window the page holds,
// the days that failed included, and the days of another agent or provider
// not at all.
func TestTheAccuracyLineCountsEveryDayItRan(t *testing.T) {
	record := window(func(day int) bool { return day >= 7 }, "0")
	record.Lines = append(record.Lines,
		accuracy.Line{Day: "2026-11-12", Sources: []string{"openai"}, Agents: []accuracy.Agent{{Name: "codex", Version: "0.164.0"}}, Verdict: accuracy.VerdictPass, TokenError: "0"},
		accuracy.Line{Day: "2026-11-13", Sources: []string{"anthropic"}, Agents: []accuracy.Agent{claudeCode}, Verdict: "no provider report"},
	)
	want := "Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 6 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count."
	if got := accuracyOf(t, record, claudeCodeFields()); !strings.HasPrefix(got, want) {
		t.Errorf("the line reads\n%s\nwant it to open\n%s", got, want)
	}
	seven := window(func(day int) bool { return day >= 7 }, "0")
	if got := accuracyOf(t, seven, claudeCodeFields()); !strings.Contains(got, " passed 7 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC), ") {
		t.Errorf("a window whose first seven days failed reads\n%s", got)
	}
	none := window(func(int) bool { return false }, "")
	want = "Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 0 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC). [The accuracy page]"
	if got := accuracyOf(t, none, claudeCodeFields()); !strings.HasPrefix(got, want) {
		t.Errorf("a window with no pass reads\n%s", got)
	}
}

// TestARunOfAnotherAgentOrProviderIsNotCovered reads the not-covered line
// for Codex on OpenAI, Claude Code on a fallback price, which names no
// provider, and a run of two agents on two providers, and the covered line for Claude Code on
// Anthropic.
func TestARunOfAnotherAgentOrProviderIsNotCovered(t *testing.T) {
	fallback := claudeCodeFields()
	fallback.model, fallback.priceID, fallback.description = "claude-next", "anthropic:fallback:output", "claude-next output tokens; "+focus.NoteFallback
	for name, rows := range map[string][]rowFields{
		"codex on openai":           {sampleFields()},
		"claude-code on a fallback": {fallback},
		"claude-code beside codex":  {claudeCodeFields(), sampleFields()},
	} {
		if got := accuracyOf(t, goldenRecord(), rows...); got != accuracyNotCovered {
			t.Errorf("%s: the line reads\n%s", name, got)
		}
	}
	if got := accuracyOf(t, goldenRecord(), claudeCodeFields(), claudeCodeFields()); got != accuracyCovered {
		t.Errorf("claude-code on anthropic: the line reads\n%s", got)
	}
	if !goldenRecord().Covers("opencode", "anthropic") || goldenRecord().Covers("codex", "openai") || goldenRecord().Covers("claude-code", "") {
		t.Error("the record covers another agent or provider than its days name")
	}
}

func TestTheLargestTokenErrorIsRoundedUp(t *testing.T) {
	for tokenError, want := range map[string]string{
		"0":          "matched Anthropic's usage report count for count.",
		"1/1000":     "was within 0.01% of Anthropic's usage report.",
		"21/50":      "was within 0.42% of Anthropic's usage report.",
		"4201/10000": "was within 0.43% of Anthropic's usage report.",
		"1":          "was within 1.00% of Anthropic's usage report.",
	} {
		record := window(func(day int) bool { return day >= 7 }, tokenError)
		if got := accuracyOf(t, record, claudeCodeFields()); !strings.Contains(got, ", and on each day that passed every token class "+want) {
			t.Errorf("a largest error of %s%% reads\n%s", tokenError, got)
		}
	}
	for percent, want := range map[string]string{"1/100000": "0.01%", "0": "0.00%", "123/100": "1.23%", "1234567/100000": "12.35%"} {
		size, _ := new(big.Rat).SetString(percent)
		if got := percentUp(size); got != want {
			t.Errorf("%s%% reads %s, want %s", percent, got, want)
		}
	}
}

// TestTheAccuracyLineHoldsNoAmount keeps every golden invariant on the
// withheld states with the line, an amount after a currency code among them,
// and finds no amount in the line itself.
func TestTheAccuracyLineHoldsNoAmount(t *testing.T) {
	states := goldenStates(t)
	checked := 0
	for name, input := range states {
		line := input.accuracyLine()
		if line == "" {
			continue
		}
		if found := amountInWords.FindString(line); found != "" {
			t.Errorf("%s: the line holds %q", name, found)
		}
		if input.Amounts == AmountsShown {
			continue
		}
		r := renderState(t, input)
		if !strings.Contains(r.comment, line) || !strings.Contains(r.summary, line) {
			t.Errorf("%s: the withheld comment or job summary does not hold the line", name)
		}
		for _, problem := range goldenProblems(input, r) {
			t.Errorf("%s: %s", name, problem)
		}
		checked++
	}
	if checked < 20 || states["accuracy-covered-withheld"].accuracyLine() != accuracyCovered {
		t.Fatalf("%d withheld states hold the line, or the covered withheld state reads another", checked)
	}
}

// TestTheVerdictReadsTheSameWithOrWithoutTheAccuracyRecord renders every
// golden state with its record and without one: the sentence, the
// conclusion, the check's title, every output and every copy of the result
// object read the same bytes.
func TestTheVerdictReadsTheSameWithOrWithoutTheAccuracyRecord(t *testing.T) {
	compared := 0
	for name, with := range goldenStates(t) {
		for _, record := range []*accuracy.Record{with.Accuracy, goldenRecord(), {}} {
			with.Accuracy = record
			without := with
			without.Accuracy = nil
			if VerdictSentence(with) != VerdictSentence(without) || with.Conclusion() != without.Conclusion() {
				t.Errorf("%s: the sentence or the conclusion moved with the record", name)
			}
			check, outputs, objects := surfaces(t, with)
			plainCheck, plainOutputs, plainObjects := surfaces(t, without)
			if check.Title != plainCheck.Title || check.Text != plainCheck.Text {
				t.Errorf("%s: the check's title or object moved with the record", name)
			}
			if !slices.Equal(outputs, plainOutputs) {
				t.Errorf("%s: an output moved with the record", name)
			}
			for copied, object := range objects {
				if object != plainObjects[copied] {
					t.Errorf("%s: the %s moved with the record", name, copied)
				}
			}
			compared++
		}
	}
	if compared < 300 {
		t.Fatalf("%d comparisons, fewer than every golden state with each record", compared)
	}
}

// TestTheAccuracyLineStandsInsideTheFold holds the line, on every golden
// state with figures, to the last paragraph of the folded details on the
// comment and the check, and to the last line of the job summary, folded
// where the summary folds its details. Nothing a reader sees before opening
// the fold holds it.
func TestTheAccuracyLineStandsInsideTheFold(t *testing.T) {
	checked := 0
	for name, input := range goldenStates(t) {
		line := input.accuracyLine()
		if input.State != Reported {
			if line != "" {
				t.Errorf("%s: a report with no figures holds the line", name)
			}
			continue
		}
		view := input.view()
		r := renderState(t, input)
		_, checkSummary, _ := strings.Cut(r.check, "\n# Summary\n\n")
		for surface, text := range map[string]string{"comment": r.comment, "check": checkSummary} {
			opening := "<details>\n<summary>" + view.DetailsSummary + "</summary>\n"
			_, fold, found := strings.Cut(text, opening)
			fold, _, closed := strings.Cut(fold, "</details>")
			if !found || !closed || !strings.HasSuffix(fold, "\n"+line+"\n\n") {
				t.Errorf("%s: the %s's fold does not end on the line:\n%s", name, surface, text)
			}
			if strings.Count(text, line) != 1 || strings.Contains(visiblePart(text), line) {
				t.Errorf("%s: the %s holds the line outside its fold", name, surface)
			}
		}
		last := "\n\n" + line + "\n"
		if view.ShowAmounts {
			last = "\n\n" + line + "\n\n</details>\n"
		}
		if !strings.HasSuffix(r.summary, last) || strings.Count(r.summary, line) != 1 {
			t.Errorf("%s: the job summary does not end on the line:\n%s", name, r.summary)
		}
		checked++
	}
	if checked < 80 {
		t.Fatalf("%d states with figures, fewer than the goldens hold", checked)
	}
}

package copilot_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/copilot"
	"github.com/Costroid/costroid/internal/comment"
)

// TestCopilotStatesItsCreditsAsWritten carries Copilot CLI's totalNanoAiu
// from its session file to the result object and the comment, in nano-AI
// units and with every character Copilot wrote: the recorded run's 0 is
// stated, not dropped, and a figure written with a trailing zero keeps it.
// No figure of the report holds it.
func TestCopilotStatesItsCreditsAsWritten(t *testing.T) {
	for _, written := range []string{"", "2500000000", "2500000000.50"} {
		name := written
		if name == "" {
			name = "the recorded 0"
		}
		t.Run(name, func(t *testing.T) {
			events := recordedEvents(t)
			want := written
			if written == "" {
				want = "0"
			} else {
				events = editedEvents(t, func(events []line) {
					object(t, ofType(events, "session.shutdown")[0], "data")["totalNanoAiu"] = json.Number(written)
				})
			}
			report := readEvents(t, events, false)
			if stated := report.AgentStated; stated == nil || *stated != (adapter.Stated{Unit: copilot.UnitNanoAIU, Quantity: want, Source: copilot.Name}) {
				t.Fatalf("the session states %+v, want %s nano-AI units as written", stated, want)
			}
			report.Enforcement = adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog}
			ledger, err := comment.ReadRows(bytes.NewReader(writtenRows(t, report)))
			if err != nil {
				t.Fatal(err)
			}
			input := comment.Input{Ledger: ledger, State: comment.Reported, HeadSHA: strings.Repeat("a", 40), Amounts: comment.AmountsShown, AgentStated: report.AgentStated}
			encoded, err := comment.Result(input)
			if err != nil {
				t.Fatal(err)
			}
			var block comment.Block
			if err := json.Unmarshal([]byte(encoded), &block); err != nil {
				t.Fatal(err)
			}
			if block.AgentStated == nil || *block.AgentStated != (comment.BlockAgentStated{Unit: "nano-AIU", Quantity: want, Source: "copilot"}) {
				t.Errorf("the result object states %+v", block.AgentStated)
			}
			if len(block.Figures) != 1 || block.Figures[0].ListCost != "0.00329423" {
				t.Errorf("the figures read %+v, want the rows' USD 0.00329423 alone", block.Figures)
			}
			body, err := comment.Comment(input)
			if err != nil {
				t.Fatal(err)
			}
			if line := "Copilot CLI stated " + want + " nano-AI units for this run, which Costroid never converts to dollars."; strings.Count(body, line) != 1 {
				t.Errorf("the comment does not state %q once:\n%s", line, body)
			}
		})
	}
}

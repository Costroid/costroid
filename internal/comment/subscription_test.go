package comment

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

const subscriptionLine = "subscription: list-equivalent, no provider report to reconcile"

// subscriptionFields is a row of a run that proved a subscription login, as
// the run writes it.
func subscriptionFields() rowFields {
	fields := sampleFields()
	fields.basis = string(focus.ListEquivalent)
	fields.description = "gpt-5.5 output tokens; " + focus.NoteSubscription + "; none: the run was not started by costroid"
	return fields
}

// TestTheRecordedSubscriptionLoginSaysSoBesideItsUnchangedFigure renders the
// rows ingest-run wrote for the recorded claude-code-action run on
// claude_code_oauth_token. The line sits beside the figure, and the figure
// is the run's own total_cost_usd, 0.0302309 USD, at its list-equivalent
// basis.
func TestTheRecordedSubscriptionLoginSaysSoBesideItsUnchangedFigure(t *testing.T) {
	input := subscriptionInput(t)
	figures := input.Ledger.Figures
	if len(figures) != 1 || figures[0].Cost.String() != "0.0302309" || figures[0].Basis != string(focus.ListEquivalent) || figures[0].Rows != 12 || !figures[0].Subscription {
		t.Fatalf("figures %+v", figures)
	}
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	row := "| USD 0.0302309; " + subscriptionLine + " | List-equivalent, not billed | unattributed |"
	if !strings.Contains(body, row) {
		t.Errorf("the comment holds no row %q:\n%s", row, body)
	}
	if !strings.Contains(body, "This run cost USD 0.0302309 at list (List-equivalent, not billed).") {
		t.Error("the first line changed")
	}
	if strings.Count(body, subscriptionLine) != 1 {
		t.Errorf("the comment states the line %d times", strings.Count(body, subscriptionLine))
	}
	block, err := input.block()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(block, "subscription") {
		t.Error("the result object names the subscription, and its schema has no place for it")
	}
}

// TestOnlyAFigureWhoseRowsAllNoteASubscriptionSaysSo holds the line to every
// row of a figure: one row without the note, or a note that only contains
// the words, leaves the figure unmarked and its cost unchanged.
func TestOnlyAFigureWhoseRowsAllNoteASubscriptionSaysSo(t *testing.T) {
	unmarked := subscriptionFields()
	unmarked.description = "gpt-5.5 output tokens; none: the run was not started by costroid"
	inside := subscriptionFields()
	inside.description = "gpt-5.5 output tokens; a " + focus.NoteSubscription
	cases := []struct {
		name string
		rows []rowFields
		want bool
	}{
		{"every row", []rowFields{subscriptionFields(), subscriptionFields()}, true},
		{"one row without the note", []rowFields{subscriptionFields(), unmarked}, false},
		{"the words inside another note", []rowFields{inside}, false},
		{"no row", []rowFields{unmarked}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := realInput(t)
			input.Ledger = ledgerOf(t, tc.rows...)
			figure := input.Ledger.Figures[0]
			if figure.Subscription != tc.want || figure.Cost.String() != map[int]string{1: "0.1", 2: "0.2"}[len(tc.rows)] {
				t.Errorf("subscription %t at %s, want %t", figure.Subscription, figure.Cost, tc.want)
			}
			for _, shown := range []Input{input, withheld(input)} {
				body, err := Comment(shown)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(body, subscriptionLine) != tc.want {
					t.Errorf("amounts %d: the comment states the line %t, want %t", shown.Amounts, !tc.want, tc.want)
				}
			}
		})
	}
}

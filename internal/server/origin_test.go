package server

import (
	"net/http"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// TestALedgerRefusesARowFromADevelopersMachine sends the ledger a row that
// states costroid/origin, with the tags a receipt's row carries and with
// another value, alone and beside a run's row: each body is refused with 422
// ROW_INVALID and nothing of it is stored, because this version of the tag
// set does not define the key. The run's row alone is stored.
func TestALedgerRefusesARowFromADevelopersMachine(t *testing.T) {
	origin := func(value string) map[string]any {
		r := row("req-2", focus.Output, 100)
		for key, tag := range map[string]string{
			"costroid/origin": value, focus.TagCostSource: "agent_session_file", focus.TagCostBasis: string(focus.ListEquivalent),
			focus.TagAttributionGrade: "unattributed", focus.TagEnforcementGrade: "none", focus.TagRunIdentity: "unverified",
		} {
			tags(r)[key] = tag
		}
		delete(tags(r), "costroid/workflow_run_id")
		return r
	}
	for name, body := range map[string][]map[string]any{
		"a receipt's row":    {origin("developer_machine")},
		"another value":      {origin("ci_runner")},
		"beside a run's row": {row("req-1", focus.Output, 100), origin("developer_machine")},
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			got := w.rows(w.token(), lines(t, body...)).want(t, http.StatusUnprocessableEntity)
			if got.code(t) != codeInvalidRow || len(ledger.rows) != 0 {
				t.Fatalf("code %s, %d rows stored: %s", got.code(t), len(ledger.rows), got.body)
			}
		})
	}
	w, ledger := memoryWorld(t)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	if len(ledger.rows) != 1 {
		t.Errorf("the run's row alone: %d rows stored", len(ledger.rows))
	}
}

package reconcile

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
)

// gatewayRows is anthropicRows as a gateway run writes them: each row names
// the run's key by its id, which the provider never sees.
func gatewayRows() []row {
	runKey := gateway.KeyID(gateway.NewKey())
	rows := anthropicRows()
	for i := range rows {
		rows[i].credential = runKey
	}
	return rows
}

func readGatewayRows(t *testing.T, rows []row, upstream map[string]string) *Ledger {
	t.Helper()
	var lines []string
	for _, r := range rows {
		lines = append(lines, r.line(t))
	}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadLedgerWith(strings.NewReader(strings.Join(lines, "\n")), days, LedgerOptions{Upstream: upstream})
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestGatewayRowsReconcileAgainstTheUpstreamKey(t *testing.T) {
	ledger := readGatewayRows(t, gatewayRows(), map[string]string{SourceAnthropic: anthropicID})
	if got := ledger.Gatewayed[SourceAnthropic]; got != 0 {
		t.Errorf("Gatewayed = %d, want 0 once the upstream key is named", got)
	}
	result := pass(t, ledger, anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	got := only(t, result)
	if got.Credential != anthropicID || got.State != Matched {
		t.Fatalf("credential %s in state %s, want %s matched: %+v", got.Credential, got.State, anthropicID, got.Tokens)
	}
	if check := classCheck(t, got, focus.InputUncached); check.Ledger.String() != "100000" || !check.Within {
		t.Errorf("input check %+v, want the run's 100000 tokens within 1%%", check)
	}
	wantDollars(t, got, "0.81", "0.81", "0", "0", Within)
	if len(result.Unmatched) != 0 {
		t.Errorf("the upstream key reads as unmatched: %+v", result.Unmatched)
	}
	if !strings.Contains(rendered(t, result), "the provider key the gateway holds") {
		t.Errorf("the row does not say it was joined by the gateway's key:\n%s", rendered(t, result))
	}
}

func TestGatewayRowsWithNoUpstreamKeyAreCounted(t *testing.T) {
	rows := gatewayRows()
	ledger := readGatewayRows(t, rows, nil)
	if got := ledger.Gatewayed[SourceAnthropic]; got != len(rows) {
		t.Errorf("Gatewayed = %d, want %d", got, len(rows))
	}
	if got := ledger.Credentials(SourceAnthropic); len(got) != 1 || got[0] != rows[0].credential {
		t.Errorf("credentials %v, want the run key's id alone", got)
	}
	openAI := readGatewayRows(t, rows, map[string]string{SourceOpenAI: openAIID})
	if got := openAI.Gatewayed[SourceAnthropic]; got != len(rows) {
		t.Errorf("an OpenAI upstream key took Anthropic rows: Gatewayed = %d, want %d", got, len(rows))
	}
}

func TestOnlyAGatewayKeysShapeReadsAsOne(t *testing.T) {
	for _, credential := range []string{anthropicID, openAIID, "wrkspc_01Example", strings.Repeat("A", gatewayKeyIDLength), strings.Repeat("a", gatewayKeyIDLength-1)} {
		if isGatewayKeyID(credential) {
			t.Errorf("%q reads as a gateway key's id", credential)
		}
	}
	if id := gateway.KeyID(gateway.NewKey()); !isGatewayKeyID(id) {
		t.Errorf("the gateway's own key id %q does not read as one", id)
	}
}

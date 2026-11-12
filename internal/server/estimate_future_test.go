package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/store"
)

// estimateFor is the rows costroid ingest-run --from focus writes in the
// fixtures' pull request for one resource's estimate over a period. The
// figure is synthetic.
func estimateFor(t testing.TB, start, end string) []byte {
	t.Helper()
	read, err := focusfile.Read(strings.NewReader(
		"BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName,SubAccountId\n"+
			"USD,"+start+","+end+",1000,aws_instance.web,Amazon EC2,example-account-a\n"),
		focusfile.Options{Tags: map[string]string{store.TagRepo: acmeRepo, store.TagPR: acmePull, store.TagCommitSHA: acmeHead, store.TagWorkflowRunID: acmeRun}})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := focus.NewWriter(&body)
	for _, row := range read.Rows {
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	return body.Bytes()
}

// estimatesStored are the estimate rows the ledger holds for the fixtures'
// tenant, by charge period start.
func estimatesStored(t *testing.T, ledger *memoryLedger) []string {
	t.Helper()
	var starts []string
	for _, record := range ledger.stored(t, tenantA) {
		if record.Row.Tags[focus.TagCostBasis] == focusfile.Basis {
			starts = append(starts, record.Row.ChargePeriodStart)
		}
	}
	return starts
}

// TestServeTakesAnEstimateRowForNextMonth sends an agent's row beside an
// estimate for December 2026, the month after the server's clock, in one
// batch. The ledger stores both.
func TestServeTakesAnEstimateRowForNextMonth(t *testing.T) {
	w, ledger := memoryWorld(t)
	agent := row("req-beside-next-month", focus.Output, 1000)
	body := append(lines(t, agent), estimateFor(t, "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z")...)
	w.rows(w.token(), body).want(t, http.StatusOK)
	if starts := estimatesStored(t, ledger); len(starts) != 1 || starts[0] != "2026-12-01T00:00:00Z" {
		t.Errorf("the ledger holds estimates starting %v, want 2026-12-01", starts)
	}
	if stored := len(ledger.stored(t, tenantA)); stored != 2 {
		t.Errorf("the ledger holds %d rows, want the agent's and the estimate's", stored)
	}
}

// TestServeStillRefusesAMeasuredRowInTheFuture holds every row that is not an
// estimate to the clock: a measured row that starts in December 2026 is
// refused, alone or beside a valid estimate, and nothing of the batch is
// stored.
func TestServeStillRefusesAMeasuredRowInTheFuture(t *testing.T) {
	w, ledger := memoryWorld(t)
	future := row("req-in-the-future", focus.Output, 1000)
	future["ChargePeriodStart"], future["ChargePeriodEnd"] = "2026-12-01T10:00:00Z", "2026-12-01T10:00:01Z"
	for name, body := range map[string][]byte{
		"alone":              lines(t, future),
		"beside an estimate": append(lines(t, future), estimateFor(t, "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z")...),
	} {
		refused := w.rows(w.token(), body).want(t, http.StatusUnprocessableEntity)
		if code := refused.code(t); code != codeInvalidRow || !strings.Contains(string(refused.body), "starts in the future") {
			t.Errorf("%s: %s", name, refused.body)
		}
	}
	if stored := ledger.stored(t, tenantA); len(stored) != 0 {
		t.Errorf("a refused batch stored %d rows", len(stored))
	}
}

// TestServeBoundsHowFarAheadAnEstimateStarts takes an estimate that starts up
// to a year after the server's clock, 2026-11-02, and refuses one that starts
// later, naming the year.
func TestServeBoundsHowFarAheadAnEstimateStarts(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), estimateFor(t, "2027-11-01T00:00:00Z", "2027-12-01T00:00:00Z")).want(t, http.StatusOK)
	refused := w.rows(w.token(), estimateFor(t, "2027-12-01T00:00:00Z", "2028-01-01T00:00:00Z")).want(t, http.StatusUnprocessableEntity)
	if code := refused.code(t); code != codeInvalidRow || !strings.Contains(string(refused.body), "an estimate may start at most a year ahead") {
		t.Errorf("an estimate 13 months ahead: %s", refused.body)
	}
	if starts := estimatesStored(t, ledger); len(starts) != 1 || starts[0] != "2027-11-01T00:00:00Z" {
		t.Errorf("the ledger holds estimates starting %v, want 2027-11-01 alone", starts)
	}
}

package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	samples     = "testdata/documentation-samples"
	day         = "2026-10-14"
	anthropicID = "apikey_01ExampleCi"
	openAIID    = "key_ExampleCi"
	sonnet      = "claude-sonnet-5"
	sol         = "gpt-5.6-sol"
)

// settledNow is well after the sample day, and before October's rerun days end.
var settledNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// row is one ledger row as the wrapper writes it, cut to what reconcile reads.
type row struct {
	provider   string
	model      string
	class      focus.TokenClass
	tokens     int64
	list       string
	credential string
	day        string
	basis      focus.CostBasis
	skuPrice   string
	note       string
}

var rowCounter int

func (r row) line(t *testing.T) string {
	t.Helper()
	rowCounter++
	unit := "Tokens"
	if r.class.Informational() {
		unit = "Informational Tokens"
	}
	var credential any
	if r.credential != "" {
		credential = r.credential
	}
	data, err := json.Marshal(map[string]any{
		"BillingCurrency":     "USD",
		"ChargeDescription":   r.model + " tokens" + r.note,
		"ChargePeriodStart":   r.day + "T09:31:02Z",
		"ConsumedQuantity":    fmt.Sprint(r.tokens),
		"ConsumedUnit":        unit,
		"HostProviderName":    r.provider,
		"ListCost":            r.list,
		"ServiceProviderName": r.provider,
		"SkuMeter":            r.class,
		"SkuPriceId":          r.skuPrice,
		"SubAccountId":        credential,
		"Tags":                map[string]string{focus.TagCostBasis: string(r.basis), focus.TagModel: r.model},
		"x_CostroidRowId":     fmt.Sprintf("row-%d", rowCounter),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// anthropicRows is the ledger that usage.json matches exactly: 0.81 USD at list.
func anthropicRows() []row {
	return []row{
		anthropicRow(focus.InputUncached, 100000, "0.2"),
		anthropicRow(focus.CacheWrite5m, 20000, "0.05"),
		anthropicRow(focus.CacheRead, 300000, "0.06"),
		anthropicRow(focus.Output, 50000, "0.5"),
	}
}

func anthropicRow(class focus.TokenClass, tokens int64, list string) row {
	return row{
		provider: "Anthropic", model: sonnet, class: class, tokens: tokens, list: list,
		credential: anthropicID, day: day, basis: focus.Measured,
		skuPrice: "anthropic:claude-sonnet-5:global:standard:all:" + string(class),
	}
}

// openAIRows is the ledger that openai/usage.json matches: 0.825 USD at list.
func openAIRows() []row {
	return []row{
		openAIRow(focus.InputUncached, 100000, "0.4"),
		openAIRow(focus.CacheRead, 50000, "0.02"),
		openAIRow(focus.CacheWrite30m, 1000, "0.005"),
		openAIRow(focus.Output, 20000, "0.4"),
	}
}

func openAIRow(class focus.TokenClass, tokens int64, list string) row {
	return row{
		provider: "OpenAI", model: sol, class: class, tokens: tokens, list: list,
		credential: openAIID, day: day, basis: focus.Measured,
		skuPrice: "openai:gpt-5.6-sol:global:standard:to-272000:" + string(class),
	}
}

func readRows(t *testing.T, rows []row, fallback string) *Ledger {
	t.Helper()
	var lines []string
	for _, r := range rows {
		lines = append(lines, r.line(t))
	}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadLedger(strings.NewReader(strings.Join(lines, "\n")), days, fallback)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

// anthropicReplay builds a fetcher that answers from two saved samples.
func anthropicReplay(t *testing.T, usage, cost string) Provider {
	t.Helper()
	return NewAnthropic(replay(t, map[string]string{"/analytics/usage": "anthropic/" + usage, "/analytics/cost": "anthropic/" + cost}), "sk-sample")
}

func openAIReplay(t *testing.T, usage, costs string) Provider {
	t.Helper()
	return NewOpenAI(replay(t, map[string]string{"/usage/completions": "openai/" + usage, "/organization/costs": "openai/" + costs}), "sk-sample")
}

func pass(t *testing.T, ledger *Ledger, provider Provider, now time.Time) Result {
	t.Helper()
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(context.Background(), ledger, Options{Range: days, Now: now, Catalog: pinned, Providers: []Provider{provider}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func only(t *testing.T, result Result) Row {
	t.Helper()
	if len(result.Rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(result.Rows), result.Rows)
	}
	return result.Rows[0]
}

func classCheck(t *testing.T, row Row, class focus.TokenClass) TokenCheck {
	t.Helper()
	index := slices.IndexFunc(row.Tokens, func(check TokenCheck) bool { return check.Class == class })
	if index < 0 {
		t.Fatalf("no %s check in %+v", class, row.Tokens)
	}
	return row.Tokens[index]
}

// wantDollars asserts the residual and that the row's arithmetic closes.
func wantDollars(t *testing.T, row Row, ledger, provider, residual, unattributed string, verdict DollarVerdict) {
	t.Helper()
	check := row.Dollars
	if check == nil {
		t.Fatal("row has no dollar check")
	}
	got := []string{check.Ledger.String(), check.Provider.String(), check.Residual.String(), check.Unattributed.String()}
	want := []string{ledger, provider, residual, unattributed}
	if !slices.Equal(got, want) {
		t.Errorf("ledger, provider, residual, unattributed = %v, want %v", got, want)
	}
	if check.Verdict != verdict {
		t.Errorf("verdict %s, want %s", check.Verdict, verdict)
	}
	sum := check.Unattributed
	for _, cause := range check.Causes {
		sum = sum.Add(cause.Amount)
	}
	if sum.Cmp(check.Residual) != 0 {
		t.Errorf("causes and unattributed sum to %s, residual is %s", sum, check.Residual)
	}
}

func wantCauses(t *testing.T, row Row, want ...Cause) {
	t.Helper()
	got := row.Dollars.Causes
	if len(got) != len(want) {
		t.Fatalf("causes %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Amount.Cmp(want[i].Amount) != 0 {
			t.Errorf("cause %d is %s %s, want %s %s", i, got[i].Name, got[i].Amount, want[i].Name, want[i].Amount)
		}
	}
}

func cause(name CauseName, amount string) Cause {
	magnitude, err := parseAmount(amount)
	if err != nil {
		panic(err)
	}
	return Cause{name, magnitude}
}

func rendered(t *testing.T, result Result) string {
	t.Helper()
	var out bytes.Buffer
	if err := Render(&out, result); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestTokensOnePercentToleranceBothSides(t *testing.T) {
	cases := []struct {
		name   string
		tokens int64
		within bool
	}{
		{"0.9% above the provider passes", 100900, true},
		{"1.0% above the provider passes, the bound is inclusive", 101000, true},
		{"1.1% above the provider fails", 101100, false},
		{"0.9% below the provider passes", 99100, true},
		{"1.1% below the provider fails", 98900, false},
		{"one token past the bound fails", 101001, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := anthropicRows()
			rows[0].tokens = tc.tokens
			got := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
			if check := classCheck(t, got, focus.InputUncached); check.Within != tc.within {
				t.Errorf("Within = %t, want %t: %+v", check.Within, tc.within, check)
			}
			wantState := Matched
			if !tc.within {
				wantState = TokensMiss
			}
			if got.State != wantState {
				t.Errorf("state %s, want %s", got.State, wantState)
			}
		})
	}
}

func TestATokensMissIsReportedAsADefect(t *testing.T) {
	rows := anthropicRows()
	rows[0].tokens = 101100
	result := pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	text := rendered(t, result)
	for _, want := range []string{"MISS", "1.10%", "defect"} {
		if !strings.Contains(text, want) {
			t.Errorf("table lacks %q:\n%s", want, text)
		}
	}
	if result.Misses() != 1 {
		t.Errorf("Misses = %d, want 1", result.Misses())
	}
}

func TestProviderZeroIsMatchedOnlyByZero(t *testing.T) {
	if within(decimal.FromInt(1), decimal.Decimal{}, tokenTolerancePercent) {
		t.Error("one ledger token against a provider zero passed")
	}
	if !within(decimal.Decimal{}, decimal.Decimal{}, tokenTolerancePercent) {
		t.Error("zero against zero failed")
	}
}

func TestInformationalClassesNeverReachTheComparison(t *testing.T) {
	rows := append(anthropicRows(),
		anthropicRow(focus.ReasoningOutput, 9000, "0"),
		anthropicRow(focus.CachedInputReported, 300000, "0"))
	got := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
	if got.State != Matched {
		t.Errorf("state %s: %+v", got.State, got.Tokens)
	}
	if len(got.Tokens) != 4 {
		t.Errorf("%d classes compared, want the 4 billed ones", len(got.Tokens))
	}
}

func TestARowReadTwiceCountsOnce(t *testing.T) {
	line := anthropicRows()[0].line(t)
	days, _ := ParseRange(day, day)
	ledger, err := ReadLedger(strings.NewReader(line+"\n"+line+"\n"), days, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Repeats != 1 {
		t.Errorf("Repeats = %d, want 1", ledger.Repeats)
	}
	key := Key{SourceAnthropic, anthropicID, day}
	if got := ledger.groups[key].tokens[focus.InputUncached].String(); got != "100000" {
		t.Errorf("tokens = %s, want 100000", got)
	}
}

func TestReadLedgerLeavesOutWhatTheReportCannotHold(t *testing.T) {
	estimate := anthropicRow(focus.InputUncached, 5, "0")
	estimate.basis = "estimate"
	billed := anthropicRow(focus.InputUncached, 5, "0")
	billed.basis = "billed"
	other := anthropicRow(focus.InputUncached, 5, "0")
	other.provider = "Example AI"
	earlier := anthropicRow(focus.InputUncached, 5, "0")
	earlier.day = "2026-10-13"
	ledger := readRows(t, []row{estimate, billed, other, earlier}, "")
	if len(ledger.groups) != 0 {
		t.Errorf("groups = %+v, want none", ledger.groups)
	}
}

func TestReadLedgerLeavesOutAModelACloudHosted(t *testing.T) {
	line := strings.Replace(anthropicRows()[0].line(t), `"HostProviderName":"Anthropic"`, `"HostProviderName":"Amazon Web Services"`, 1)
	days, _ := ParseRange(day, day)
	ledger, err := ReadLedger(strings.NewReader(line), days, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.groups) != 0 {
		t.Errorf("groups = %+v, want none", ledger.groups)
	}
}

func TestANullCredentialIsCountedAndTheFallbackFillsIt(t *testing.T) {
	rows := anthropicRows()
	for i := range rows {
		rows[i].credential = ""
	}
	if got := readRows(t, rows, "").Uncredentialed; got != 4 {
		t.Errorf("Uncredentialed = %d, want 4", got)
	}
	ledger := readRows(t, rows, anthropicID)
	if ledger.Uncredentialed != 0 || ledger.groups[Key{SourceAnthropic, anthropicID, day}] == nil {
		t.Errorf("fallback did not fill the credential: %+v", ledger)
	}
}

func TestSourceNamesMatchTheCatalog(t *testing.T) {
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range sources {
		if got, err := pinned.ServiceProviderName(source); err != nil || got != name {
			t.Errorf("sheet %s names %q (%v), want %q", source, got, err, name)
		}
	}
}

// TestDescriptionNotesMatchTheWriter holds the two notes to the file that
// writes them, because row version 0.1 has no column for either.
func TestDescriptionNotesMatchTheWriter(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "run", "rows.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range []string{lifetimeAssumedNote, residencyNote} {
		if !bytes.Contains(source, []byte(note)) {
			t.Errorf("internal/run/rows.go no longer writes %q", note)
		}
	}
}

func TestAResidualInsideTenPercentWithNoNamedCauseReadsUnattributed(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-unexplained.json"), settledNow)
		got := only(t, result)
		wantDollars(t, got, "0.81", "0.85", "0.04", "0.04", Within)
		wantCauses(t, got)
		if text := rendered(t, result); !strings.Contains(text, "unattributed 0.04") {
			t.Errorf("table does not say so:\n%s", text)
		}
	})
	t.Run("openai", func(t *testing.T) {
		result := pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage.json", "costs.json"), settledNow)
		got := only(t, result)
		wantDollars(t, got, "0.825", "0.86", "0.035", "0.035", Within)
		wantCauses(t, got)
		if text := rendered(t, result); !strings.Contains(text, "unattributed 0.035") {
			t.Errorf("table does not say so:\n%s", text)
		}
	})
}

func TestTheTenPercentBoundIsInclusiveAndAgainstTheProvider(t *testing.T) {
	if !within(decimal.MustParse("1.1"), decimal.MustParse("1"), dollarTolerancePercent) {
		t.Error("exactly 10% was refused")
	}
	if within(decimal.MustParse("1.100000000001"), decimal.MustParse("1"), dollarTolerancePercent) {
		t.Error("past 10% was accepted")
	}
}

func TestAMatchingBillHasNoResidualToExplain(t *testing.T) {
	result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	got := only(t, result)
	wantDollars(t, got, "0.81", "0.81", "0", "0", Within)
	if text := rendered(t, result); !strings.Contains(text, "none needed") {
		t.Errorf("table:\n%s", text)
	}
}

func TestContractRateIsTheGapTheProviderStatesBetweenAmountAndList(t *testing.T) {
	result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-contract-rate.json"), settledNow)
	got := only(t, result)
	wantDollars(t, got, "0.81", "0.7695", "-0.0405", "0", Within)
	wantCauses(t, got, cause(CauseContractRate, "-0.0405"))
	if text := rendered(t, result); !strings.Contains(text, "contract_rate -0.0405") {
		t.Errorf("table:\n%s", text)
	}
}

func TestToolChargesStayOutOfTheTokenComparison(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-contract-rate.json"), settledNow))
	if got.Dollars.OtherCost.String() != "1" {
		t.Errorf("OtherCost = %s, want 1", got.Dollars.OtherCost)
	}
}

// The batch sheet prices input at 1 and output at 5: 0.1 + 0.25. It names no
// batch cache price, so those two lines stay at standard, 0.05 + 0.06, and say
// so. The walk then reaches 0.46, which is the bill.
func TestTierIsTheServedTierPricedThroughTheCatalog(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-batch.json", "cost-batch.json"), settledNow))
	wantDollars(t, got, "0.81", "0.46", "-0.35", "0", Outside)
	wantCauses(t, got, cause(CauseTier, "-0.35"))
	if !slices.ContainsFunc(got.Notes, func(note string) bool { return strings.Contains(note, "tier batch has no catalog price") }) {
		t.Errorf("notes %q do not name the unpriced tier", got.Notes)
	}
}

// 0.81 times the sheet's 1.1 for US inference is 0.891.
func TestResidencyIsTheServedRegionPricedThroughTheCatalog(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-us.json", "cost-us.json"), settledNow))
	wantDollars(t, got, "0.81", "0.891", "0.081", "0", Within)
	wantCauses(t, got, cause(CauseResidency, "0.081"))
}

// Priority Tier tokens carry no cost on the report: the commitment pays for
// them elsewhere. The whole list figure is therefore the tier's.
func TestPriorityTierTokensCostNothingOnTheReport(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-priority.json", "cost-priority.json"), settledNow))
	wantDollars(t, got, "0.81", "0", "-0.81", "0", Outside)
	wantCauses(t, got, cause(CauseTier, "-0.81"))
}

func TestFastModeIsATierOnTheSheetAndASpeedOnTheReport(t *testing.T) {
	fast, standard := "fast", "standard"
	if got := (anthropicGroup{ServiceTier: &standard, Speed: &fast}).tier(); got != fast {
		t.Errorf("tier %s, want fast", got)
	}
	if got := (anthropicGroup{}).tier(); got != catalog.TierStandard {
		t.Errorf("a row with no tier reads %s", got)
	}
}

func TestALedgerPricedAtTheServedRegionLeavesNoResidency(t *testing.T) {
	multiplied := []string{"0.22", "0.055", "0.066", "0.55"}
	rows := anthropicRows()
	for i := range rows {
		rows[i].list = multiplied[i]
		rows[i].note = "; list price times 1.1 for data residency in us; agent-flag"
	}
	got := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage-us.json", "cost-us.json"), settledNow))
	wantDollars(t, got, "0.891", "0.891", "0", "0", Within)
	wantCauses(t, got)
}

func TestSharedKeyIsTrafficTheLedgerNeverSaw(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-shared.json", "cost-shared.json"), settledNow))
	if got.State != TokensMiss {
		t.Errorf("state %s, want a miss", got.State)
	}
	wantDollars(t, got, "0.81", "1.62", "0.81", "0", Outside)
	wantCauses(t, got, cause(CauseSharedKey, "0.81"))
}

func TestALedgerAboveTheProviderIsNeverASharedKey(t *testing.T) {
	rows := anthropicRows()
	rows[3].tokens = 60000
	rows[3].list = "0.6"
	got := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
	if got.State != TokensMiss {
		t.Errorf("state %s, want a miss", got.State)
	}
	wantDollars(t, got, "0.91", "0.81", "-0.1", "-0.1", Outside)
	wantCauses(t, got)
}

func TestListEquivalentRowsHaveNoBillToCompare(t *testing.T) {
	rows := anthropicRows()
	for i := range rows {
		rows[i].basis = focus.ListEquivalent
	}
	result := pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	got := only(t, result)
	if got.State != Matched {
		t.Errorf("tokens still reconcile: state %s", got.State)
	}
	if got.Dollars.Verdict != NoBill {
		t.Errorf("verdict %s, want %s", got.Dollars.Verdict, NoBill)
	}
	if text := rendered(t, result); !strings.Contains(text, "no API bill") {
		t.Errorf("table:\n%s", text)
	}
}

func TestADayPastTheRefreshStampIsPendingAndNeverAMiss(t *testing.T) {
	rows := anthropicRows()
	rows[0].tokens = 900000
	for name, files := range map[string][2]string{
		"stamp before the day ends": {"usage-refreshing.json", "cost-refreshing.json"},
		"null stamp":                {"usage-no-data-yet.json", "cost-no-data-yet.json"},
	} {
		t.Run(name, func(t *testing.T) {
			result := pass(t, readRows(t, rows, ""), anthropicReplay(t, files[0], files[1]), settledNow)
			got := only(t, result)
			if got.State != Pending || got.Tokens != nil || got.Dollars != nil {
				t.Errorf("row %+v, want pending with no figure", got)
			}
			if result.Misses() != 0 {
				t.Errorf("Misses = %d", result.Misses())
			}
		})
	}
}

func TestADayThatHasNotEndedIsPending(t *testing.T) {
	noon := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), noon))
	if got.State != Pending {
		t.Errorf("state %s, want pending", got.State)
	}
}

func TestOpenAIStampsNothingSoADayWaitsOutTheLag(t *testing.T) {
	cases := map[time.Time]State{
		time.Date(2026, 10, 15, 23, 59, 59, 0, time.UTC): Pending,
		time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC):    Matched,
	}
	for now, want := range cases {
		got := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage.json", "costs-at-list.json"), now))
		if got.State != want {
			t.Errorf("at %s state %s, want %s", now, got.State, want)
		}
	}
}

func TestProviderTrafficWithNoLedgerRowsThatDayIsAMiss(t *testing.T) {
	rows := anthropicRows()
	for i := range rows {
		rows[i].day = "2026-10-13"
	}
	pinned, _ := catalog.Load()
	lines := []string{}
	for _, r := range rows {
		lines = append(lines, r.line(t))
	}
	days, _ := ParseRange("2026-10-13", day)
	ledger, err := ReadLedger(strings.NewReader(strings.Join(lines, "\n")), days, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(context.Background(), ledger, Options{
		Range: days, Now: settledNow, Catalog: pinned,
		Providers: []Provider{anthropicReplay(t, "usage.json", "cost-at-list.json")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want both days", len(result.Rows))
	}
	if result.Rows[0].State != TokensMiss || result.Rows[1].State != TokensMiss {
		t.Errorf("states %s and %s, want a miss on each side", result.Rows[0].State, result.Rows[1].State)
	}
}

func TestACredentialTheLedgerNeverNamesIsListedAndNotJudged(t *testing.T) {
	result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-other-key.json", "cost-at-list.json"), settledNow)
	if got := only(t, result); got.Credential != anthropicID || got.State != Matched {
		t.Errorf("row %+v", got)
	}
	want := Unmatched{SourceAnthropic, "apikey_01ExampleProd"}
	if !slices.Contains(result.Unmatched, want) {
		t.Errorf("Unmatched = %+v", result.Unmatched)
	}
	if text := rendered(t, result); !strings.Contains(text, "ledger never names it") {
		t.Errorf("table:\n%s", text)
	}
}

func TestOpenAIClassesAreDisjointOnBothResponseAges(t *testing.T) {
	for _, usage := range []string{"usage.json", "usage-older-response.json"} {
		t.Run(usage, func(t *testing.T) {
			got := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, usage, "costs-at-list.json"), settledNow))
			if got.State != Matched {
				t.Errorf("state %s: %+v", got.State, got.Tokens)
			}
			if check := classCheck(t, got, focus.InputUncached); check.Provider.String() != "100000" {
				t.Errorf("input_uncached %s, want input_tokens with cached and writes taken out", check.Provider)
			}
		})
	}
}

func TestOpenAIWritesCompareAsOneBecauseTheLedgerCannotSeeTheirLifetime(t *testing.T) {
	got := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage-twelve-hour-writes.json", "costs-at-list.json"), settledNow))
	if got.State != Matched {
		t.Errorf("state %s: %+v", got.State, got.Tokens)
	}
	if check := classCheck(t, got, cacheWrite); check.Provider.String() != "1000" {
		t.Errorf("cache writes %s, want 1000", check.Provider)
	}
}

func TestAnAssumedLifetimeJoinsAnthropicWrites(t *testing.T) {
	rows := anthropicRows()
	rows[1].class = focus.CacheWrite1h
	exact := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
	if exact.State != TokensMiss {
		t.Errorf("a stated lifetime compares by class: state %s", exact.State)
	}
	rows[1].note = "; cache lifetime assumed"
	joined := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
	if joined.State != Matched {
		t.Errorf("an assumed lifetime joins the writes: state %s, %+v", joined.State, joined.Tokens)
	}
}

func TestOpenAIBatchIsATierAndAnUnknownTierIsNoted(t *testing.T) {
	batch := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage-batch.json", "costs-batch.json"), settledNow))
	wantDollars(t, batch, "0.825", "0.4125", "-0.4125", "0", Outside)
	wantCauses(t, batch, cause(CauseTier, "-0.4125"))

	priority := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage-priority.json", "costs.json"), settledNow))
	wantDollars(t, priority, "0.825", "0.86", "0.035", "0.035", Within)
	if !slices.ContainsFunc(priority.Notes, func(note string) bool { return strings.Contains(note, "tier priority has no catalog price") }) {
		t.Errorf("notes %q", priority.Notes)
	}
}

func TestParseAmountIsExact(t *testing.T) {
	cases := map[string]string{
		"0.06":                "0.06",
		"8.6e-1":              "0.86",
		"8.6E-1":              "0.86",
		"1.5e2":               "150",
		"-0.25":               "-0.25",
		"41280.000000":        "41280",
		"0.30000000000000004": "0.30000000000000004",
	}
	for in, want := range cases {
		got, err := parseAmount(in)
		if err != nil {
			t.Errorf("parseAmount(%q): %v", in, err)
		} else if got.String() != want {
			t.Errorf("parseAmount(%q) = %s, want %s", in, got, want)
		}
	}
	for _, in := range []string{"", "abc", "1e", "1e999", "--1", "NaN"} {
		if _, err := parseAmount(in); err == nil {
			t.Errorf("parseAmount(%q) was accepted", in)
		}
	}
}

func TestOpenAIAmountsKeepTheirDigitsThroughScientificNotation(t *testing.T) {
	got := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage.json", "costs-scientific.json"), settledNow))
	if got.Dollars.Provider.String() != "0.86" {
		t.Errorf("provider %s, want 0.86", got.Dollars.Provider)
	}
}

func TestRerunWindowEndsOnTheEighthDayAfterMonthEnd(t *testing.T) {
	cases := []struct {
		day  string
		now  time.Time
		want bool
	}{
		{"2026-10-14", time.Date(2026, 11, 8, 23, 59, 59, 0, time.UTC), false},
		{"2026-10-14", time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), true},
		{"2026-10-31", time.Date(2026, 11, 8, 23, 59, 59, 0, time.UTC), false},
		{"2026-12-31", time.Date(2027, 1, 8, 12, 0, 0, 0, time.UTC), false},
		{"2026-12-31", time.Date(2027, 1, 9, 0, 0, 0, 0, time.UTC), true},
		{"2028-02-29", time.Date(2028, 3, 8, 12, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		if got := isFinal(tc.day, tc.now); got != tc.want {
			t.Errorf("isFinal(%s, %s) = %t, want %t", tc.day, tc.now, got, tc.want)
		}
	}
}

func TestAnOpenMonthSaysHowLongItCanBeRerun(t *testing.T) {
	result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	if text := rendered(t, result); !strings.Contains(text, "2026-10 can be restated: rerun through 2026-11-08.") {
		t.Errorf("table:\n%s", text)
	}
}

func TestARowCarriesDailyTotalsAndNoSecret(t *testing.T) {
	result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	data, err := json.Marshal(result.Rows)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("sk-sample")) {
		t.Error("a row carries the admin key")
	}
	if !regexp.MustCompile(`"ledger":"100000"`).Match(data) {
		t.Errorf("token counts are not decimal strings: %s", data)
	}
}

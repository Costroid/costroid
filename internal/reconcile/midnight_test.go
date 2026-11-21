package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
)

const nextDay = "2026-10-15"

// reportedDay is one day of a provider's report for anthropicID: the run's
// tokens by class, and their cost in cents at list.
type reportedDay struct {
	day                          string
	input, write5m, read, output int64
	cents                        string
}

// split is the run of anthropicRows as the provider reports it when the run
// crossed midnight: three fifths of each class on the first day and two
// fifths on the next. At the catalog's prices the run is USD 0.81.
var split = []reportedDay{
	{day, 60000, 12000, 180000, 30000, "48.600000"},
	{nextDay, 40000, 8000, 120000, 20000, "32.400000"},
}

// splitByClass is split's cost by token type, in cents at list: input at
// USD 2, 5-minute writes at 2.5, reads at 0.2 and output at 10 per million.
var splitByClass = map[string]map[string]string{
	day:     {"uncached_input_tokens": "12.000000", "cache_creation.ephemeral_5m_input_tokens": "3.000000", "cache_read_input_tokens": "3.600000", "output_tokens": "30.000000"},
	nextDay: {"uncached_input_tokens": "8.000000", "cache_creation.ephemeral_5m_input_tokens": "2.000000", "cache_read_input_tokens": "2.400000", "output_tokens": "20.000000"},
}

// anthropicDays answers the usage and cost endpoints with the days given,
// each day's cost on one line.
func anthropicDays(t *testing.T, days []reportedDay) Provider {
	t.Helper()
	return anthropicReport(t, days, nil)
}

// anthropicReport answers as anthropicDays does. With byClass, a day's cost
// is one line per token type, at the cents byClass gives that day.
func anthropicReport(t *testing.T, days []reportedDay, byClass map[string]map[string]string) Provider {
	t.Helper()
	return anthropicBilled(t, days, byClass, nil)
}

// anthropicBilled answers as anthropicReport does, and bills a day billed
// names at the cents it gives in place of the list figure. It applies to a
// day whose cost is one line.
func anthropicBilled(t *testing.T, days []reportedDay, byClass map[string]map[string]string, billed map[string]string) Provider {
	t.Helper()
	var usage, cost []map[string]any
	for _, each := range days {
		start := each.day + "T00:00:00Z"
		group := map[string]any{"api_key_id": anthropicID, "model": sonnet, "service_tier": "standard", "inference_geo": "global", "speed": "standard"}
		usage = append(usage, map[string]any{"starting_at": start, "group": group, "usage": map[string]any{
			"uncached_input_tokens":   each.input,
			"cache_creation":          map[string]int64{"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": each.write5m},
			"cache_read_input_tokens": each.read,
			"output_tokens":           each.output,
		}})
		if byClass == nil {
			amount := each.cents
			if cents, found := billed[each.day]; found {
				amount = cents
			}
			cost = append(cost, map[string]any{"starting_at": start, "group": map[string]any{"api_key_id": anthropicID, "cost_type": "tokens"},
				"cost": map[string]any{"currency": "USD", "amount": amount, "list_amount": each.cents}})
			continue
		}
		for tokenType, cents := range byClass[each.day] {
			group := map[string]any{"api_key_id": anthropicID, "model": sonnet, "service_tier": "standard", "inference_geo": "global", "speed": "standard", "cost_type": "tokens", "token_type": tokenType}
			cost = append(cost, map[string]any{"starting_at": start, "group": group,
				"cost": map[string]any{"currency": "USD", "amount": cents, "list_amount": cents}})
		}
	}
	body := func(data []map[string]any) string {
		page, err := json.Marshal(map[string]any{"data": data, "has_more": false, "next_page": nil, "data_refreshed_at": "2026-10-16T08:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		return string(page)
	}
	return NewAnthropic(doerFunc(func(request *http.Request) (*http.Response, error) {
		page := body(usage)
		if strings.HasSuffix(request.URL.Path, "/analytics/cost") {
			page = body(cost)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(page))}, nil
	}), sampleKey)
}

// runRows is anthropicRows written as one run from start to end.
func runRows(t *testing.T, start, end string) []string {
	t.Helper()
	var lines []string
	for _, r := range anthropicRows() {
		var fields map[string]any
		if err := json.Unmarshal([]byte(r.line(t)), &fields); err != nil {
			t.Fatal(err)
		}
		fields["ChargePeriodStart"], fields["ChargePeriodEnd"] = start, end
		line, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(line))
	}
	return lines
}

func passOver(t *testing.T, lines []string, from, to string, provider Provider, now time.Time) Result {
	t.Helper()
	days, err := ParseRange(from, to)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadLedger(strings.NewReader(strings.Join(lines, "\n")), days, "")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(context.Background(), ledger, Options{Range: days, Now: now, Catalog: pinned, Providers: []Provider{provider}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRunAcrossMidnightNamesItsCause(t *testing.T) {
	result := passOver(t, runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z"), day, nextDay, anthropicDays(t, split), settledNow)
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want one per day: %+v", len(result.Rows), result.Rows)
	}
	if result.Misses() != 0 {
		t.Errorf("Misses = %d, want 0: a run across midnight is no miss", result.Misses())
	}
	want := Span{Cause: CauseRunSpansDays, From: day, To: nextDay}
	for _, got := range result.Rows {
		if got.State != Matched {
			t.Errorf("%s: state %s, want %s", got.Day, got.State, Matched)
		}
		if got.Span == nil || *got.Span != want {
			t.Errorf("%s: span %+v, want %+v", got.Day, got.Span, want)
		}
	}
	first, second := result.Rows[0], result.Rows[1]
	if check := classCheck(t, first, focus.Output); check.Ledger.String() != "50000" || check.Provider.String() != "50000" || !check.Within {
		t.Errorf("the first day holds %+v, want both days' 50000 output tokens on each side", check)
	}
	if len(second.Tokens) != 0 {
		t.Errorf("the next day repeats the joined tokens: %+v", second.Tokens)
	}
	text := rendered(t, result)
	for _, refused := range []string{"MISS", "defect", "shared_key", "outside 10%"} {
		if strings.Contains(text, refused) {
			t.Errorf("the table reads %q:\n%s", refused, text)
		}
	}
	if !strings.Contains(text, "crossed midnight UTC") {
		t.Errorf("the table does not name the cause:\n%s", text)
	}
}

// The ledger holds the whole run, USD 0.81 at list, on its first day, and the
// provider serves USD 0.486 of it that day and USD 0.324 the next. The first
// day's dollars are both days' on each side, so the residual is what the
// report bills apart from list: the next day's USD 0.024 below it, as a
// contract rate. No residual names run_spans_days, since nothing moved out of
// the days compared.
func TestASpanComparesItsDollarsAsOne(t *testing.T) {
	provider := anthropicBilled(t, split, nil, map[string]string{nextDay: "30.000000"})
	result := passOver(t, runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z"), day, nextDay, provider, settledNow)
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want one per day: %+v", len(result.Rows), result.Rows)
	}
	first := result.Rows[0]
	if first.Span == nil || first.Day != first.Span.From {
		t.Fatalf("the first row is not the span's first day: %+v", first)
	}
	wantDollars(t, first, "0.81", "0.786", "-0.024", "0", Within)
	wantCauses(t, first, cause(CauseContractRate, "-0.024"))
	check := first.Dollars
	if check.ProviderEffective.String() != "0.786" || check.ProviderList == nil || check.ProviderList.String() != "0.81" {
		t.Errorf("provider_effective %s, provider_list %v, want both days' USD 0.786 and 0.81", check.ProviderEffective, check.ProviderList)
	}
	if check.Rounding == nil || check.Rounding.Rows != 4 || check.Rounding.Variance.Sign() != 0 || !check.Rounding.Within {
		t.Errorf("rounding %+v, want the 4 rows of both days and no variance", check.Rounding)
	}
	if text := rendered(t, result); strings.Contains(text, string(CauseRunSpansDays)+" ") {
		t.Errorf("a residual names run_spans_days:\n%s", text)
	}
	if result.Misses() != 0 {
		t.Errorf("Misses = %d, want 0", result.Misses())
	}
}

// A later day of a span holds no comparison of its own: its tokens and its
// dollars are on the first day's row, and it keeps the span, the span's state
// and its own prices. The dollar table prints the span once.
func TestALaterSpanDayCarriesNoDollars(t *testing.T) {
	result := passOver(t, runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z"), day, nextDay, anthropicReport(t, split, splitByClass), settledNow)
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want one per day: %+v", len(result.Rows), result.Rows)
	}
	later := result.Rows[1]
	if later.Day != nextDay || later.Span == nil || *later.Span != (Span{CauseRunSpansDays, day, nextDay}) {
		t.Fatalf("the next day's row is %+v", later)
	}
	if later.Dollars != nil || later.Tokens != nil {
		t.Errorf("the next day holds tokens %+v and dollars %+v of its own", later.Tokens, later.Dollars)
	}
	if later.State != Matched || len(later.Prices) == 0 {
		t.Errorf("the next day reads %s with prices %+v, want the span's state and its own prices", later.State, later.Prices)
	}
	if want := "this day's tokens and dollars are compared with " + day + "'s as one, on that day's row"; !slices.ContainsFunc(later.Notes, func(note string) bool { return strings.Contains(note, want) }) {
		t.Errorf("the next day's notes %q do not say %q", later.Notes, want)
	}
	if text := rendered(t, result); strings.Count(text, "within 10%") != 1 {
		t.Errorf("the dollar table does not print the span once:\n%s", text)
	}
}

// A price that misses on a later day of a span makes the span price_miss, on
// every day of it, and the span is one defect.
func TestASpanIsAPriceMissWhenAnyOfItsDaysMisses(t *testing.T) {
	byClass := map[string]map[string]string{day: splitByClass[day], nextDay: maps.Clone(splitByClass[nextDay])}
	byClass[nextDay]["output_tokens"] = "20.020000"
	result := passOver(t, runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z"), day, nextDay, anthropicReport(t, split, byClass), settledNow)
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want one per day: %+v", len(result.Rows), result.Rows)
	}
	first, later := result.Rows[0], result.Rows[1]
	if priceMissed(first.Prices) || !priceMissed(later.Prices) {
		t.Fatalf("prices: first day missed %t, next day %t; want the next day's alone", priceMissed(first.Prices), priceMissed(later.Prices))
	}
	for _, got := range result.Rows {
		if got.State != PriceMiss || got.Span == nil {
			t.Errorf("%s: state %s, span %+v; want %s in the span", got.Day, got.State, got.Span, PriceMiss)
		}
	}
	if want := "a price the report implies on " + nextDay + ", a day of this span, missed the catalog"; !slices.ContainsFunc(first.Notes, func(note string) bool { return strings.HasPrefix(note, want) }) {
		t.Errorf("the first day's notes %q do not name the day that missed", first.Notes)
	}
	if result.Misses() != 1 {
		t.Errorf("Misses = %d, want the span once", result.Misses())
	}
	if text := rendered(t, result); !strings.Contains(text, "Credential days with a price miss: 1.") {
		t.Errorf("the table does not count the span once:\n%s", text)
	}
}

func TestRunWithinOneDayStillMissesWhenTheProviderSplitsIt(t *testing.T) {
	result := passOver(t, runRows(t, day+"T09:31:02Z", day+"T09:50:00Z"), day, nextDay, anthropicDays(t, split), settledNow)
	if result.Misses() != 2 {
		t.Errorf("Misses = %d, want both days: no row crossed midnight", result.Misses())
	}
	for _, got := range result.Rows {
		if got.State != TokensMiss || got.Span != nil {
			t.Errorf("%s: state %s, span %+v; want %s and no span", got.Day, got.State, got.Span, TokensMiss)
		}
	}
}

func TestRunEndingAtMidnightHoldsOneDay(t *testing.T) {
	result := passOver(t, runRows(t, day+"T23:50:00Z", nextDay+"T00:00:00Z"), day, nextDay, anthropicDays(t, split), settledNow)
	if result.Misses() != 2 {
		t.Errorf("Misses = %d, want 2: a period that ends at midnight never reaches the next day", result.Misses())
	}
}

func TestHalfARunAcrossMidnightIsPending(t *testing.T) {
	rows := runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z")
	cases := []struct {
		name, from, to string
		now            time.Time
		want           string
	}{
		{"the range stops at midnight", day, day, settledNow, fmt.Sprintf("reconcile from %s to %s", day, nextDay)},
		{"the range starts at midnight", nextDay, nextDay, settledNow, fmt.Sprintf("reconcile from %s to %s", day, nextDay)},
		{"the next day is not reported yet", day, nextDay, time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), "midnight UTC into " + nextDay + ", which the provider has not finished reporting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := passOver(t, rows, tc.from, tc.to, anthropicDays(t, split), tc.now)
			if result.Misses() != 0 {
				t.Errorf("Misses = %d, want 0", result.Misses())
			}
			for _, got := range result.Rows {
				if got.State != Pending {
					t.Errorf("%s: state %s, want %s", got.Day, got.State, Pending)
				}
			}
			if text := rendered(t, result); !strings.Contains(text, tc.want) {
				t.Errorf("the table does not say %q:\n%s", tc.want, text)
			}
		})
	}
}

// The ledger holds the whole run on its first day, and the provider serves
// three fifths of each class that day. Each class's dollars are judged over
// both days, on the first day's row, as its tokens are, so no class misses.
func TestClassDollarsJudgeAMidnightRunAcrossItsDays(t *testing.T) {
	result := passOver(t, runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z"), day, nextDay, anthropicReport(t, split, splitByClass), settledNow)
	if len(result.Rows) != 2 {
		t.Fatalf("got %d rows, want one per day: %+v", len(result.Rows), result.Rows)
	}
	first, second := result.Rows[0], result.Rows[1]
	if first.Span == nil || first.State != Matched {
		t.Fatalf("the first day reads %s with span %+v", first.State, first.Span)
	}
	want := map[focus.TokenClass]string{focus.InputUncached: "0.2", focus.CacheWrite5m: "0.05", focus.CacheRead: "0.06", focus.Output: "0.5"}
	if len(first.Dollars.Classes) != len(want) {
		t.Fatalf("the first day compares %d classes, want %d: %+v", len(first.Dollars.Classes), len(want), first.Dollars.Classes)
	}
	for _, check := range first.Dollars.Classes {
		if check.Ledger.String() != want[check.Class] || check.Provider.String() != want[check.Class] || !check.Within {
			t.Errorf("%s: ledger USD %s, provider USD %s, within %t; want USD %s on both sides of both days", check.Class, check.Ledger, check.Provider, check.Within, want[check.Class])
		}
	}
	if second.Dollars != nil {
		t.Errorf("the next day repeats the classes: %+v", second.Dollars)
	}
}

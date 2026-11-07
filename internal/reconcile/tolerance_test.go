package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// The four scenarios are FOCUS 1.4's appendix "Rounding Variance Tolerance".
// The bounds around them are this package's: the tolerance is inclusive, and a
// variance counts by its size whichever way it runs.
func TestRoundingTolerance(t *testing.T) {
	cases := []struct {
		name     string
		currency string
		rows     int64
		variance string
		within   bool
	}{
		{"appendix: small invoice, pass", "USD", 5, "0.02", true},
		{"appendix: small invoice, fail", "USD", 5, "5.00", false},
		{"appendix: large invoice, pass", "USD", 1_000_000, "4.50", true},
		{"appendix: large invoice, fail", "USD", 1_000_000, "15.00", false},
		{"the floor is inclusive", "USD", 5, "1.00", true},
		{"one subunit past the floor", "USD", 5, "1.01", false},
		{"the row term is inclusive", "USD", 1_000_000, "5.00", true},
		{"one subunit past the row term", "USD", 1_000_000, "5.01", false},
		{"a row count with no whole root", "USD", 1_000_001, "5.00", true},
		{"a variance below zero", "USD", 5, "-1.01", false},
		{"no rows", "USD", 0, "0.000001", true},
		{"a currency with no subunit", "JPY", 5, "100", true},
		{"a currency with no subunit, past the floor", "JPY", 5, "101", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variance, err := parseAmount(tc.variance)
			if err != nil {
				t.Fatal(err)
			}
			got, err := WithinRounding(tc.currency, tc.rows, variance)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.within {
				t.Fatalf("within = %t, want %t", got, tc.within)
			}
		})
	}
	if _, err := WithinRounding("XAU", 5, decimal.MustParse("0.02")); err == nil {
		t.Error("a currency with no stated subunit was given a tolerance")
	}
}

func TestEffectiveAmount(t *testing.T) {
	contract := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-contract-rate.json"), settledNow))
	dollars := contract.Dollars
	if dollars.ProviderEffective.String() != "0.7695" || dollars.Provider.String() != "0.7695" {
		t.Errorf("effective %s, provider %s, want the amount 0.7695", dollars.ProviderEffective, dollars.Provider)
	}
	if dollars.ProviderList == nil || dollars.ProviderList.String() != "0.81" {
		t.Fatalf("list %v, want the list_amount 0.81", dollars.ProviderList)
	}
	rounding := dollars.Rounding
	if rounding == nil || !rounding.Within || rounding.Rows != 4 || rounding.Variance.String() != "0" || rounding.Currency != "USD" {
		t.Fatalf("rounding %+v, want within over 4 rows", rounding)
	}
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"provider_effective":"0.7695"`, `"provider_list":"0.81"`, `"rounding":{"currency":"USD","rows":4,"variance":"0","within":true}`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("row lacks %s: %s", want, data)
		}
	}

	rows := anthropicRows()
	rows[3].list = "5.5"
	off := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)).Dollars
	if off.Rounding == nil || off.Rounding.Within || off.Rounding.Variance.String() != "-5" {
		t.Errorf("rounding %+v, want a variance of -5 outside the tolerance", off.Rounding)
	}
	if text := rendered(t, Result{Rows: []Row{{Key: Key{SourceAnthropic, anthropicID, day}, State: Matched, Dollars: off}}}); !strings.Contains(text, "outside, 4 rows") {
		t.Errorf("table:\n%s", text)
	}

	openAI := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage.json", "costs-at-list.json"), settledNow))
	if openAI.Dollars.ProviderList != nil || openAI.Dollars.Rounding != nil {
		t.Errorf("OpenAI states one amount, yet list %v, rounding %+v", openAI.Dollars.ProviderList, openAI.Dollars.Rounding)
	}
	data, err = json.Marshal(openAI)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"provider_list":null`) || !strings.Contains(string(data), `"provider_effective":"0.825"`) {
		t.Errorf("row %s", data)
	}
}

func TestRefreshStampKept(t *testing.T) {
	settled := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
	want := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	if settled.DataRefreshedAt == nil || !settled.DataRefreshedAt.Equal(want) {
		t.Fatalf("stamp %v, want %s", settled.DataRefreshedAt, want)
	}
	data, err := json.Marshal(settled)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"data_refreshed_at":"2026-10-15T08:00:00Z"`) {
		t.Errorf("row %s", data)
	}

	older := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-refreshing.json"), settledNow))
	earliest := time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC)
	if older.State != Pending || older.DataRefreshedAt == nil || !older.DataRefreshedAt.Equal(earliest) {
		t.Errorf("state %s, stamp %v, want pending at the older page's %s", older.State, older.DataRefreshedAt, earliest)
	}

	unstamped := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage.json", "costs-at-list.json"), settledNow))
	if unstamped.DataRefreshedAt != nil {
		t.Errorf("OpenAI stamps nothing, yet %v", unstamped.DataRefreshedAt)
	}
}

// openAIUsageOnly answers the usage endpoint with one bucket holding result,
// and every other endpoint with an empty page.
func openAIUsageOnly(result string) Doer {
	return doerFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"object":"page","data":[],"has_more":false,"next_page":null}`
		if strings.HasSuffix(request.URL.Path, "/usage/completions") {
			body = `{"object":"page","data":[{"object":"bucket","start_time":1791936000,"end_time":1792022400,"results":[` +
				result + `]}],"has_more":false,"next_page":null}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

func TestNegativeDerivedClassRefused(t *testing.T) {
	cases := map[string]struct {
		result string
		class  focus.TokenClass
	}{
		"input that excludes its cached tokens": {
			`{"input_tokens":100,"input_cached_tokens":80,"input_cache_write_tokens":30,"output_tokens":5,"api_key_id":"key_ExampleCi","model":"gpt-5.6-sol"}`,
			focus.InputUncached,
		},
		"writes below their twelve-hour part": {
			`{"input_tokens":100,"input_cached_tokens":10,"input_cache_write_tokens":10,"input_cache_write_12h_tokens":20,"output_tokens":5,"api_key_id":"key_ExampleCi","model":"gpt-5.6-sol"}`,
			focus.CacheWrite30m,
		},
	}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewOpenAI(openAIUsageOnly(tc.result), "sk-sample").Fetch(context.Background(), days, []string{openAIID})
			if err == nil {
				t.Fatal("a negative class was accepted")
			}
			for _, want := range []string{day, sol, string(tc.class), "below zero"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
	stated := `{"input_tokens":100,"input_cached_tokens":80,"input_cache_write_tokens":30,"input_uncached_tokens":0,"output_tokens":5,"api_key_id":"key_ExampleCi","model":"gpt-5.6-sol"}`
	if _, err := NewOpenAI(openAIUsageOnly(stated), "sk-sample").Fetch(context.Background(), days, []string{openAIID}); err != nil {
		t.Errorf("a stated class was refused: %v", err)
	}
}

// retag rewrites one ledger line's tags.
func retag(t *testing.T, line string, tags map[string]any) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		t.Fatal(err)
	}
	fields["Tags"] = tags
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNonStringTagReadsAbsent(t *testing.T) {
	rows := anthropicRows()
	model := retag(t, rows[0].line(t), map[string]any{focus.TagCostBasis: "measured", focus.TagModel: 7, focus.TagAssumedTTL: true})
	basis := retag(t, rows[3].line(t), map[string]any{focus.TagCostBasis: map[string]any{"measured": true}, focus.TagModel: sonnet})
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadLedger(strings.NewReader(model+"\n"+basis), days, "")
	if err != nil {
		t.Fatalf("a non-string tag failed the read: %v", err)
	}
	group := ledger.group(Key{SourceAnthropic, anthropicID, day})
	if got := group.tokens[focus.InputUncached].String(); got != "100000" {
		t.Errorf("the row with a numeric model tag reads %s input tokens, want 100000", got)
	}
	if group.assumedLifetime {
		t.Error("a boolean assumed_ttl tag read as a stated lifetime")
	}
	if _, read := group.tokens[focus.Output]; read {
		t.Error("a row whose cost basis is not a string was read as measured")
	}
	if _, found := group.measured[recordKey{"", "standard", "global"}]; !found {
		t.Errorf("the numeric model tag did not read as absent: %+v", group.measured)
	}
}

func TestATruncatedRowsFileNamesTheRow(t *testing.T) {
	rows := anthropicRows()
	whole := rows[0].line(t)
	cut := rows[1].line(t)
	cut = cut[:len(cut)/2]
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadLedger(strings.NewReader(whole+"\n"+cut), days, "")
	if err == nil || !strings.Contains(err.Error(), "ends inside row 2") {
		t.Fatalf("err = %v, want the row named", err)
	}
}

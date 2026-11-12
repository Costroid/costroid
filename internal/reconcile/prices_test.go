package reconcile

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const haiku = "claude-haiku-5-5"

// tinyDayRows is the ledger usage-tiny-day.json matches: 0.000425375 USD at
// list, every request under Haiku 5.5's 100,000-token context tier.
func tinyDayRows() []row {
	rows := []row{
		anthropicRow(focus.InputUncached, 1200, "0.00012"),
		anthropicRow(focus.CacheWrite5m, 203, "0.000025375"),
		anthropicRow(focus.CacheRead, 3000, "0.00003"),
		anthropicRow(focus.Output, 500, "0.00025"),
	}
	for i := range rows {
		rows[i].model = haiku
		rows[i].skuPrice = "anthropic:claude-haiku-5-5:global:standard:to-100000:" + string(rows[i].class)
	}
	return rows
}

func priceOf(t *testing.T, row Row, class focus.TokenClass) PriceCheck {
	t.Helper()
	index := slices.IndexFunc(row.Prices, func(check PriceCheck) bool { return check.Class == class })
	if index < 0 {
		t.Fatalf("no %s price in %+v", class, row.Prices)
	}
	return row.Prices[index]
}

func implied(check PriceCheck) string {
	if check.ImpliedUnitPrice == nil {
		return "null"
	}
	return check.ImpliedUnitPrice.String()
}

func noted(row Row, part string) bool {
	return slices.ContainsFunc(row.Notes, func(note string) bool { return strings.Contains(note, part) })
}

func TestImpliedPriceMatchesTheCatalog(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-by-token-type.json"), settledNow))
	if got.State != Matched {
		t.Fatalf("state %s, notes %q", got.State, got.Notes)
	}
	want := map[focus.TokenClass][3]string{
		focus.InputUncached: {"100000", "0.2", "2"},
		focus.CacheWrite5m:  {"20000", "0.05", "2.5"},
		focus.CacheRead:     {"300000", "0.06", "0.2"},
		focus.Output:        {"50000", "0.5", "10"},
	}
	if len(got.Prices) != len(want) {
		t.Fatalf("%d prices, want %d: %+v", len(got.Prices), len(want), got.Prices)
	}
	for class, figures := range want {
		check := priceOf(t, got, class)
		have := [3]string{check.Tokens.String(), check.ProviderList.String(), implied(check)}
		if have != figures {
			t.Errorf("%s: tokens, provider list, implied = %v, want %v", class, have, figures)
		}
		if !check.Match || check.CatalogUnitPrice.Cmp(*check.ImpliedUnitPrice) != 0 {
			t.Errorf("%s: implied %s against the catalog's %s, match %t", class, implied(check), check.CatalogUnitPrice, check.Match)
		}
		if check.Model != sonnet || check.Tier != catalog.TierStandard || check.Region != catalog.RegionGlobal {
			t.Errorf("%s placed at %s %s %s", class, check.Model, check.Tier, check.Region)
		}
		if check.SkuPriceID != "anthropic:claude-sonnet-5:global:standard:all:"+string(class) {
			t.Errorf("%s held to %s", class, check.SkuPriceID)
		}
	}
	if got.Dollars.OtherCost.String() != "0.01" {
		t.Errorf("the web search line reads %s, want 0.01 outside the token comparison", got.Dollars.OtherCost)
	}
}

func TestOnePriceOffByOneCentPerMillionIsADefect(t *testing.T) {
	result := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-by-token-type-one-cent-over.json"), settledNow)
	got := only(t, result)
	if got.State != PriceMiss {
		t.Fatalf("state %s, want %s: %+v", got.State, PriceMiss, got.Prices)
	}
	output := priceOf(t, got, focus.Output)
	if output.Match || implied(output) != "10.01" || output.CatalogUnitPrice.String() != "10" {
		t.Errorf("output: implied %s, catalog %s, match %t", implied(output), output.CatalogUnitPrice, output.Match)
	}
	if got.Dollars.Verdict != Within {
		t.Errorf("the day's dollars read %s: the 10%% rule alone would have passed this day", got.Dollars.Verdict)
	}
	if result.Misses() != 1 {
		t.Errorf("Misses = %d, want 1, so reconcile exits 1", result.Misses())
	}
	text := rendered(t, result)
	for _, want := range []string{"PRICES", "10.01", "MISS", "Credential days with a price miss: 1. Each is a defect."} {
		if !strings.Contains(text, want) {
			t.Errorf("table lacks %q:\n%s", want, text)
		}
	}
	price := catalog.Price{ListUnitPrice: decimal.MustParse("10")}
	tokens := decimal.FromInt(50000)
	unit := decimal.MustParse("0.00000001")
	for figure, match := range map[string]bool{
		"0.5":          true,
		"0.500000009":  true,
		"0.50000001":   false,
		"0.499999991":  true,
		"0.49999999":   false,
		"0.5005":       false,
		"0.4999999999": true,
	} {
		if got := proves(&lineFigure{list: decimal.MustParse(figure), unit: unit}, tokens, price); got != match {
			t.Errorf("%s USD for 50000 tokens at 10 per 1M with a last digit of 0.00000001: match %t, want %t", figure, got, match)
		}
	}
}

func TestPriceProofHoldsOnATinyDay(t *testing.T) {
	exact := only(t, pass(t, readRows(t, tinyDayRows(), ""), anthropicReplay(t, "usage-tiny-day.json", "cost-tiny-day.json"), settledNow))
	if exact.State != Matched || len(exact.Prices) != 4 {
		t.Fatalf("state %s, prices %+v, notes %q", exact.State, exact.Prices, exact.Notes)
	}
	if writes := priceOf(t, exact, focus.CacheWrite5m); !writes.Match || writes.ProviderList.String() != "0.00002538" {
		t.Errorf("203 writes at 0.125 per 1M, stated to the report's sixth place of a cent: %+v", writes)
	}
	if exact.Dollars.Provider.String() != "0.00042538" {
		t.Errorf("the day bills %s USD, want 0.00042538", exact.Dollars.Provider)
	}

	over := only(t, pass(t, readRows(t, tinyDayRows(), ""), anthropicReplay(t, "usage-tiny-day.json", "cost-tiny-day-output-nine-percent-over.json"), settledNow))
	if over.State != PriceMiss {
		t.Fatalf("an output price 9%% over the catalog on a 0.0004 USD day: state %s", over.State)
	}
	output := priceOf(t, over, focus.Output)
	if output.Match || implied(output) != "0.545" || output.CatalogUnitPrice.String() != "0.5" {
		t.Errorf("output: implied %s, catalog %s, match %t", implied(output), output.CatalogUnitPrice, output.Match)
	}
	if over.Dollars.Verdict != Within || over.Dollars.Rounding == nil || !over.Dollars.Rounding.Within {
		t.Errorf("the 10%% rule and the rounding check both pass this day, and neither can see the price: %+v", over.Dollars)
	}
}

func TestOpenAILineItemSplitsModelAndClass(t *testing.T) {
	line := "gpt-6-astra, input_tokens"
	var result openAICostResult
	result.LineItem = &line
	result.Amount.Value, result.Amount.Currency = "0.000125", "usd"
	cost, err := result.cost(openAIKeyGrain, day)
	if err != nil {
		t.Fatal(err)
	}
	if cost.Model != "gpt-6-astra" || cost.Class != focus.InputUncached || cost.Label != line {
		t.Errorf("model %q, class %q, label %q", cost.Model, cost.Class, cost.Label)
	}
	if cost.ListFigure == nil || cost.ListFigure.Unit.String() != "0.000001" {
		t.Errorf("the last stated digit of 0.000125 reads %+v", cost.ListFigure)
	}
	unknown := "web search"
	result.LineItem = &unknown
	if cost, err := result.cost(openAIKeyGrain, day); err != nil || cost.Class != "" || cost.Model != "" {
		t.Errorf("a line item with no model and kind read as %+v, %v", cost, err)
	}

	got := only(t, pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, "usage.json", "costs-line-items.json"), settledNow))
	if got.State != Matched || len(got.Prices) != 4 {
		t.Fatalf("state %s, prices %+v, notes %q", got.State, got.Prices, got.Notes)
	}
	want := map[focus.TokenClass]string{
		focus.InputUncached: "4",
		focus.CacheRead:     "0.4",
		focus.CacheWrite30m: "5",
		focus.Output:        "20",
	}
	for class, price := range want {
		check := priceOf(t, got, class)
		if !check.Match || implied(check) != price || check.Model != sol || check.Tier != catalog.TierStandard {
			t.Errorf("%s: %+v, implied %s, want %s", class, check, implied(check), price)
		}
	}
}

// An unread kind is a gap in the reader, not a price: the class it bills has
// no line Costroid reads, and is named instead of read as a zero.
func TestALineItemKindNotReadIsNamedNotMissed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(samples, "openai/costs-line-items.json"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(data), "gpt-5.6-sol, output_tokens", "gpt-5.6-sol, output_kind_not_read", 1)
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		body := renamed
		if strings.HasSuffix(request.URL.Path, "/usage/completions") {
			usage, err := os.ReadFile(filepath.Join(samples, "openai/usage.json"))
			if err != nil {
				return nil, err
			}
			body = string(usage)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	got := only(t, pass(t, readRows(t, openAIRows(), ""), NewOpenAI(client, sampleKey), settledNow))
	if got.State != Matched || len(got.Prices) != 3 {
		t.Errorf("state %s, prices %+v", got.State, got.Prices)
	}
	for _, part := range []string{`"gpt-5.6-sol, output_kind_not_read" names no model and class`, "gpt-5.6-sol output has no line of its own"} {
		if !noted(got, part) {
			t.Errorf("no note says %q: %q", part, got.Notes)
		}
	}
}

func TestPriorityTierCarriesNoPrice(t *testing.T) {
	got := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-priority.json", "cost-priority-by-token-type.json"), settledNow))
	if got.State != Matched {
		t.Errorf("state %s: a priority line's zero cost is no price miss", got.State)
	}
	if len(got.Prices) != 0 {
		t.Errorf("priority lines were held to a price: %+v", got.Prices)
	}
	for _, class := range []focus.TokenClass{focus.InputUncached, focus.CacheWrite5m, focus.CacheRead, focus.Output} {
		if !noted(got, "claude-sonnet-5 "+string(class)+" at the priority tier carries no token cost") {
			t.Errorf("no note names the skipped %s line: %q", class, got.Notes)
		}
	}
}

func TestCostQueryGroupsByTokenType(t *testing.T) {
	anthropic := replay(t, map[string]string{"/analytics/usage": "anthropic/empty-refreshed.json", "/analytics/cost": "anthropic/empty-refreshed.json"})
	days, _ := ParseRange(day, day)
	if _, err := NewAnthropic(anthropic, sampleKey).Fetch(context.Background(), days, []string{anthropicID}); err != nil {
		t.Fatal(err)
	}
	usage := anthropic.asked[0].URL.Query()["group_by[]"]
	cost := anthropic.asked[1].URL.Query()["group_by[]"]
	if !slices.Equal(cost[:len(usage)], usage) || !slices.Equal(cost[len(usage):], []string{"cost_type", "token_type"}) {
		t.Errorf("cost group_by %v, want usage's %v then cost_type and token_type", cost, usage)
	}
	openai := replay(t, map[string]string{"/usage/completions": "openai/empty.json", "/organization/costs": "openai/empty.json"})
	if _, err := NewOpenAI(openai, sampleKey).Fetch(context.Background(), days, []string{openAIID}); err != nil {
		t.Fatal(err)
	}
	if got := openai.asked[1].URL.Query()["group_by"]; !slices.Equal(got, []string{"line_item", "api_key_id"}) {
		t.Errorf("costs group_by %v, want line_item and api_key_id", got)
	}

	cases := map[string]focus.TokenClass{
		"uncached_input_tokens":                    focus.InputUncached,
		"cache_read_input_tokens":                  focus.CacheRead,
		"cache_creation.ephemeral_5m_input_tokens": focus.CacheWrite5m,
		"cache_creation.ephemeral_1h_input_tokens": focus.CacheWrite1h,
		"output_tokens":                            focus.Output,
	}
	for tokenType, class := range cases {
		var row anthropicCostRow
		row.Group.TokenType = &tokenType
		row.Cost.Currency, row.Cost.Amount = "USD", "1.000000"
		listAmount := "41280.000000"
		row.Cost.ListAmount = &listAmount
		cost, err := row.cost(anthropicKeyGrain)
		if err != nil {
			t.Fatal(err)
		}
		if cost.Class != class || cost.ListFigure.Value.String() != "412.8" || cost.ListFigure.Unit.String() != "0.00000001" {
			t.Errorf("%s: class %q, figure %+v", tokenType, cost.Class, cost.ListFigure)
		}
	}
}

func TestPerClassDollarsWithinOnePercent(t *testing.T) {
	exact := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-by-token-type.json"), settledNow))
	if len(exact.Dollars.Classes) != 4 {
		t.Fatalf("classes %+v", exact.Dollars.Classes)
	}
	for _, check := range exact.Dollars.Classes {
		if !check.Within || check.Delta.Sign() != 0 {
			t.Errorf("%+v", check)
		}
	}
	cases := []struct {
		list   string
		within bool
	}{
		{"0.505", true},
		{"0.495", true},
		{"0.5051", false},
		{"0.45", false},
	}
	for _, tc := range cases {
		rows := anthropicRows()
		rows[3].list = tc.list
		got := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage.json", "cost-by-token-type.json"), settledNow))
		output := got.Dollars.Classes[slices.IndexFunc(got.Dollars.Classes, func(check ClassDollars) bool { return check.Class == focus.Output })]
		if output.Within != tc.within || output.Ledger.String() != tc.list || output.Provider.String() != "0.5" {
			t.Errorf("ledger output %s USD against 0.5: %+v, want within %t", tc.list, output, tc.within)
		}
		if got.Dollars.Verdict != Within {
			t.Errorf("ledger output %s USD: the day's 10%% line reads %s", tc.list, got.Dollars.Verdict)
		}
		if got.State != Matched {
			t.Errorf("ledger output %s USD: state %s; the provider's prices still match", tc.list, got.State)
		}
	}
	unsplit := only(t, pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow))
	if unsplit.Dollars.Classes != nil || !noted(unsplit, "does not split token cost by class") {
		t.Errorf("a report with no token types: classes %+v, notes %q", unsplit.Dollars.Classes, unsplit.Notes)
	}
}

func TestAPriceHeldToTheWrongContextTierNamesTheTierItMatches(t *testing.T) {
	rows := tinyDayRows()
	rows[3].skuPrice = "anthropic:claude-haiku-5-5:global:standard:over-100000:output"
	rows[3].list = "0.00125"
	got := only(t, pass(t, readRows(t, rows, ""), anthropicReplay(t, "usage-tiny-day.json", "cost-tiny-day.json"), settledNow))
	output := priceOf(t, got, focus.Output)
	if got.State != PriceMiss || output.Match || output.CatalogUnitPrice.String() != "2.5" {
		t.Fatalf("state %s, output %+v", got.State, output)
	}
	if !noted(got, "at the catalog's anthropic:claude-haiku-5-5:global:standard:to-100000:output, not anthropic:claude-haiku-5-5:global:standard:over-100000:output") {
		t.Errorf("notes %q", got.Notes)
	}

	both := append(tinyDayRows(), rows[3])
	both[len(both)-1].tokens, both[len(both)-1].list = 1, "0.0000025"
	spanning := only(t, pass(t, readRows(t, both, ""), anthropicReplay(t, "usage-tiny-day.json", "cost-tiny-day.json"), settledNow))
	if slices.ContainsFunc(spanning.Prices, func(check PriceCheck) bool { return check.Class == focus.Output }) {
		t.Errorf("output spanning two context tiers was held to one price: %+v", spanning.Prices)
	}
	if !noted(spanning, "claude-haiku-5-5 output at 2 context tiers") {
		t.Errorf("notes %q", spanning.Notes)
	}
}

func TestStatedReadsTheLastDigitTheReportWrote(t *testing.T) {
	cases := []struct {
		text  string
		shift int
		value string
		unit  string
	}{
		{"41280.000000", 2, "412.8", "0.00000001"},
		{"0.002538", 2, "0.00002538", "0.00000001"},
		{"0.06", 0, "0.06", "0.01"},
		{"1.25e-5", 0, "0.0000125", "0.0000001"},
		{"3e2", 0, "300", "100"},
		{"-0.5", 0, "-0.5", "0.1"},
	}
	for _, tc := range cases {
		got, err := stated(tc.text, tc.shift)
		if err != nil {
			t.Fatalf("%s: %v", tc.text, err)
		}
		if got.Value.String() != tc.value || got.Unit.String() != tc.unit {
			t.Errorf("%s shifted %d: value %s unit %s, want %s and %s", tc.text, tc.shift, got.Value, got.Unit, tc.value, tc.unit)
		}
	}
}

// tinyDayRowsInUS is the ledger usage-tiny-day-us.json matches: the tiny day
// served in the US, each figure 1.1 times the global one, 0.0004679125 USD
// at list. Each row's price id names the region given.
func tinyDayRowsInUS(region string) []row {
	multiplied := []string{"0.000132", "0.0000279125", "0.000033", "0.000275"}
	rows := tinyDayRows()
	for i := range rows {
		rows[i].list = multiplied[i]
		rows[i].note = "; list price times 1.1 for data residency in us"
		rows[i].skuPrice = "anthropic:claude-haiku-5-5:" + region + ":standard:to-100000:" + string(rows[i].class)
	}
	return rows
}

// wantHeldInUS holds a reconciled tiny day in the US to the catalog's US
// prices below Haiku 5.5's 100,000-token boundary, each proven.
func wantHeldInUS(t *testing.T, got Row) {
	t.Helper()
	if got.State != Matched {
		t.Fatalf("state %s, notes %q", got.State, got.Notes)
	}
	for class, price := range map[focus.TokenClass]string{focus.InputUncached: "0.11", focus.CacheWrite5m: "0.1375", focus.CacheRead: "0.011", focus.Output: "0.55"} {
		check := priceOf(t, got, class)
		if !check.Match || check.CatalogUnitPrice.String() != price || check.SkuPriceID != "anthropic:claude-haiku-5-5:us:standard:to-100000:"+string(class) {
			t.Errorf("%s: %+v, want %s proven", class, check, price)
		}
	}
	if noted(got, "context tiers") {
		t.Errorf("notes %q", got.Notes)
	}
}

// TestOlderRowsWithGlobalIdsReconcileInARegion holds a ledger written up to
// 1.0.0-alpha.23, whose rows carry the global id beside the US price, to the
// catalog's US tier: Haiku 5.5 has two context tiers, and the id still says
// which one the row was priced at.
func TestOlderRowsWithGlobalIdsReconcileInARegion(t *testing.T) {
	got := only(t, pass(t, readRows(t, tinyDayRowsInUS(catalog.RegionGlobal), ""), anthropicReplay(t, "usage-tiny-day-us.json", "cost-tiny-day-us.json"), settledNow))
	wantHeldInUS(t, got)
}

// TestRegionalIdsReconcile holds the same day, written by a release whose
// price ids name their region, to the same prices.
func TestRegionalIdsReconcile(t *testing.T) {
	got := only(t, pass(t, readRows(t, tinyDayRowsInUS("us"), ""), anthropicReplay(t, "usage-tiny-day-us.json", "cost-tiny-day-us.json"), settledNow))
	wantHeldInUS(t, got)
}

// TestAGlobalIdNeverMatchesAnotherTier holds the one way a global id stands
// for a regional one: the same model, service tier, context tier and class,
// in the line's own region. A regional id never stands for a global tier.
func TestAGlobalIdNeverMatchesAnotherTier(t *testing.T) {
	const tier = "anthropic:claude-haiku-5-5:us:standard:to-100000:output"
	for _, tc := range []struct {
		sku, region string
		names       bool
	}{
		{"anthropic:claude-haiku-5-5:global:standard:to-100000:output", "us", true},
		{tier, "us", true},
		{"anthropic:claude-haiku-5-5:global:standard:over-100000:output", "us", false},
		{"anthropic:claude-haiku-5-5:global:batch:to-100000:output", "us", false},
		{"anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached", "us", false},
		{"anthropic:claude-sonnet-5:global:standard:to-100000:output", "us", false},
		{"anthropic:claude-haiku-5-5:global:standard:to-100000:output", "eu", false},
		{"anthropic:claude-haiku-5-5:eu:standard:to-100000:output", "us", false},
		{"anthropic:claude-haiku-5-5:global:standard:to-100000:output", catalog.RegionGlobal, false},
		{"anthropic:fallback:output", "us", false},
	} {
		if got := namesTier(tc.sku, tier, tc.region); got != tc.names {
			t.Errorf("%s in %s names %s: %t", tc.sku, tc.region, tier, got)
		}
	}
	const global = "anthropic:claude-haiku-5-5:global:standard:to-100000:output"
	if namesTier(tier, global, "us") || namesTier(tier, global, catalog.RegionGlobal) {
		t.Errorf("the regional id %s names the global tier %s", tier, global)
	}
	tiers := []catalog.Price{
		{SkuPriceID: tier, ListUnitPrice: decimal.MustParse("0.55")},
		{SkuPriceID: "anthropic:claude-haiku-5-5:us:standard:over-100000:output", ListUnitPrice: decimal.MustParse("2.75")},
	}
	held, ok := heldTo(tiers, []string{"anthropic:claude-haiku-5-5:global:standard:over-100000:output"}, "us")
	if !ok || held.ListUnitPrice.String() != "2.75" {
		t.Errorf("an older id above the boundary was held to %+v, %t", held, ok)
	}
	if _, ok := heldTo(tiers, []string{global, "anthropic:claude-haiku-5-5:global:standard:over-100000:output"}, "us"); ok {
		t.Error("rows at both tiers were held to one price")
	}
}

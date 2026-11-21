package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
)

// integrationContract is integration/v0.5's openapi.json of
// Costroid/costroid-schemas at 4cf76ee, copied. A published version never
// changes in place, so the copy cannot go stale.
var integrationContract = filepath.Join("..", "..", "cmd", "costroid", "testdata", "integration-v0.5.openapi.json")

// rowSchema compiles the contract's ReconciliationRow.
func rowSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(integrationContract)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("openapi.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("openapi.json#/components/schemas/ReconciliationRow")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// validRow reports whether a row, as --out writes it, is a ReconciliationRow.
func validRow(t *testing.T, schema *jsonschema.Schema, row Row) error {
	t.Helper()
	line, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(line)))
	if err != nil {
		t.Fatal(err)
	}
	return schema.Validate(instance)
}

// schemaPasses are the fixtures every row of which the schema holds: each
// sample report beside the ledger it was written for, the days a run crossed
// midnight over, a day still reporting, and a price miss on a later day of a
// span.
func schemaPasses(t *testing.T) map[string]Result {
	t.Helper()
	passes := map[string]Result{}
	for _, pair := range [][2]string{
		{"usage.json", "cost-at-list.json"}, {"usage.json", "cost-shared.json"}, {"usage.json", "cost-contract-rate.json"},
		{"usage.json", "cost-unexplained.json"}, {"usage.json", "cost-by-token-type.json"}, {"usage.json", "cost-by-token-type-one-cent-over.json"},
		{"usage-refreshing.json", "cost-refreshing.json"}, {"usage-us.json", "cost-us.json"}, {"usage-batch.json", "cost-batch.json"},
		{"usage-priority.json", "cost-priority-by-token-type.json"}, {"usage-shared.json", "cost-shared.json"},
	} {
		passes["anthropic "+pair[1]] = pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, pair[0], pair[1]), settledNow)
	}
	for _, pair := range [][2]string{
		{"usage.json", "costs.json"}, {"usage.json", "costs-at-list.json"}, {"usage.json", "costs-line-items.json"},
		{"usage-older-response.json", "costs-scientific.json"}, {"usage-batch.json", "costs-batch.json"},
	} {
		passes["openai "+pair[1]] = pass(t, readRows(t, openAIRows(), ""), openAIReplay(t, pair[0], pair[1]), settledNow)
	}
	crossed := runRows(t, day+"T23:50:00Z", nextDay+"T00:20:00Z")
	passes["a span"] = passOver(t, crossed, day, nextDay, anthropicDays(t, split), settledNow)
	passes["a span by class"] = passOver(t, crossed, day, nextDay, anthropicReport(t, split, splitByClass), settledNow)
	passes["half a span"] = passOver(t, crossed, day, day, anthropicDays(t, split), settledNow)
	over := map[string]map[string]string{day: splitByClass[day], nextDay: {}}
	for tokenType, cents := range splitByClass[nextDay] {
		over[nextDay][tokenType] = cents
	}
	over[nextDay]["output_tokens"] = "20.020000"
	passes["a span with a price miss"] = passOver(t, crossed, day, nextDay, anthropicReport(t, split, over), settledNow)
	return passes
}

// Every row reconcile writes in its fixtures is a ReconciliationRow of
// integration/v0.5. A later day of a span that carried dollars of its own
// would not be: the schema holds it to null tokens and null dollars.
func TestEveryRowKeepsTheReconciliationRowSchema(t *testing.T) {
	schema := rowSchema(t)
	spans := 0
	for name, result := range schemaPasses(t) {
		if len(result.Rows) == 0 {
			t.Fatalf("%s wrote no row", name)
		}
		for _, row := range result.Rows {
			if err := validRow(t, schema, row); err != nil {
				t.Errorf("%s: the row of %s %s breaks the contract: %v", name, row.Credential, row.Day, err)
			}
			if row.laterInSpan() {
				spans++
			}
		}
	}
	if spans == 0 {
		t.Fatal("no fixture wrote a later day of a span")
	}
	result := schemaPasses(t)["a span"]
	later := result.Rows[1]
	later.Dollars = result.Rows[0].Dollars
	if err := validRow(t, schema, later); err == nil {
		t.Error("the schema holds a later day of a span that carries dollars")
	}
}

// Every row carries currency, the currency of every amount in it, and where
// rounding is compared it names the same one. A row or a report line in
// another currency stops the pass, so no row holds two.
func TestRowCarriesTheCurrencyOfEveryAmount(t *testing.T) {
	for name, result := range schemaPasses(t) {
		for _, row := range result.Rows {
			line, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var written map[string]any
			if err := json.Unmarshal(line, &written); err != nil || written["currency"] != "USD" {
				t.Errorf("%s: the row of %s %s writes currency %q: %s", name, row.Credential, row.Day, written["currency"], line)
			}
			if row.Dollars != nil && row.Dollars.Rounding != nil && row.Dollars.Rounding.Currency != row.Currency {
				t.Errorf("%s: rounding is in %s and the row in %s", name, row.Dollars.Rounding.Currency, row.Currency)
			}
		}
	}

	euro := strings.Replace(anthropicRows()[0].line(t), `"BillingCurrency":"USD"`, `"BillingCurrency":"EUR"`, 1)
	if _, err := ReadLedger(strings.NewReader(euro), sampleDay(t), ""); err == nil || !strings.Contains(err.Error(), `currency "EUR"`) {
		t.Errorf("a row in EUR read as %v", err)
	}
	report := anthropicReplay(t, "usage.json", "cost-at-list.json")
	euros := NewAnthropic(doerFunc(func(request *http.Request) (*http.Response, error) {
		response, err := replay(t, map[string]string{"/analytics/usage": "anthropic/usage.json", "/analytics/cost": "anthropic/cost-at-list.json"}).Do(request)
		if err != nil || !strings.HasSuffix(request.URL.Path, "/analytics/cost") {
			return response, err
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(body), `"USD"`, `"EUR"`)))
		return response, nil
	}), sampleKey)
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Range: sampleDay(t), Now: settledNow, Catalog: pinned}
	options.Providers = []Provider{report}
	if _, err := Reconcile(context.Background(), readRows(t, anthropicRows(), ""), options); err != nil {
		t.Fatalf("the report in USD: %v", err)
	}
	options.Providers = []Provider{euros}
	if _, err := Reconcile(context.Background(), readRows(t, anthropicRows(), ""), options); err == nil || !strings.Contains(err.Error(), `currency "EUR"`) {
		t.Errorf("a report in EUR reconciled as %v", err)
	}
}

// Every figure a row writes is a decimal string in the contract's form, and
// nothing is rounded to fit. A provider figure past it stops that credential's
// day with an error that names the day, the key and the figure, and every
// other day is still written.
func TestAmountsKeepTheContractsForm(t *testing.T) {
	for _, check := range []struct {
		name, figure string
		hold         func(*form, string, decimal.Decimal)
		holds        bool
	}{
		{"thirty places", "0." + strings.Repeat("0", 29) + "1", (*form).amount, true},
		{"thirty-one places", "0." + strings.Repeat("0", 30) + "1", (*form).amount, false},
		{"sixty-four characters", strings.Repeat("9", 64), (*form).amount, true},
		{"sixty-five characters", strings.Repeat("9", 65), (*form).amount, false},
		{"a minus", "-0.25", (*form).amount, true},
		{"a minus where none goes", "-0.25", (*form).nonNegative, false},
		{"twenty digits of tokens", strings.Repeat("9", 20), (*form).count, true},
		{"twenty-one digits of tokens", strings.Repeat("9", 21), (*form).count, false},
		{"tokens with a point", "1840.5", (*form).count, false},
		{"a token difference below zero", "-10", (*form).delta, true},
		{"a token count below zero", "-10", (*form).count, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			figure, err := decimal.ParseNumber(check.figure)
			if err != nil {
				// Past what a literal reads, so built from its digits.
				figure = decimal.MustParse(check.figure[:decimal.MaxLength]).Mul(decimal.FromInt(10)).Add(decimal.MustParse(check.figure[decimal.MaxLength:]))
			}
			f := &form{key: Key{SourceAnthropic, anthropicID, day}}
			check.hold(f, "dollars.provider", figure)
			if (f.refused == nil) != check.holds {
				t.Errorf("%s: refused %v, want held %t", check.figure, f.refused, check.holds)
			}
		})
	}

	// Anthropic states cents, so a cost line of 31 places in cents is 33 in
	// dollars. The day's row is refused; the next day's row is still written.
	served := []reportedDay{{day, 100000, 20000, 300000, 50000, "81.000000"}, {nextDay, 0, 0, 0, 0, "0"}}
	cents := "81." + strings.Repeat("0", 30) + "1"
	result := passOver(t, runRows(t, day+"T09:00:00Z", day+"T09:20:00Z"), day, nextDay, anthropicBilled(t, served, nil, map[string]string{day: cents}), settledNow)
	if len(result.Refused) != 1 || len(result.Rows) != 1 || result.Rows[0].Day != nextDay {
		t.Fatalf("refused %v, rows %+v; want the first day refused and the next written", result.Refused, result.Rows)
	}
	want := "anthropic " + anthropicID + " " + day + ": dollars.provider reads 0.81" + strings.Repeat("0", 30) + "1, and an amount is a decimal of at most 30 places and 64 characters, and none is rounded to fit, so the day is not written"
	if got := result.Refused[0].Error(); got != want {
		t.Errorf("the refusal reads %q, want %q", got, want)
	}

	// A token count is a whole number: a ledger row of half a token refuses its day.
	half := strings.Replace(anthropicRows()[0].line(t), `"ConsumedQuantity":"100000"`, `"ConsumedQuantity":"100000.5"`, 1)
	lines := []string{half}
	for _, r := range anthropicRows()[1:] {
		lines = append(lines, r.line(t))
	}
	result = passOver(t, lines, day, day, anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow)
	if len(result.Refused) != 1 || len(result.Rows) != 0 || !strings.Contains(result.Refused[0].Error(), ": tokens[0].ledger reads 100000.5, and a token count is a whole number of at most 20 digits") {
		t.Errorf("refused %v, rows %+v", result.Refused, result.Rows)
	}
}

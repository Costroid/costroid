package reconcile

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/catalog"
)

// ledgerLines are rows as the wrapper writes them, one per provider and
// basis, on the samples' day.
var ledgerLines = []string{
	`{"BillingCurrency":"USD","ChargeDescription":"claude-sonnet-5 tokens","ChargePeriodStart":"2026-10-14T09:31:02Z","ConsumedQuantity":"100000","ConsumedUnit":"Tokens","HostProviderName":"Anthropic","ListCost":"0.2","ServiceProviderName":"Anthropic","SkuMeter":"input_uncached","SkuPriceId":"anthropic:claude-sonnet-5:global:standard:all:input_uncached","SubAccountId":"apikey_01ExampleCi","Tags":{"costroid/cost_basis":"measured","costroid/model":"claude-sonnet-5"},"x_CostroidRowId":"row-1"}`,
	`{"BillingCurrency":"USD","ChargeDescription":"claude-sonnet-5 tokens; cache lifetime assumed","ChargePeriodStart":"2026-10-14T09:31:02Z","ConsumedQuantity":"20000","ConsumedUnit":"Tokens","HostProviderName":"Anthropic","ListCost":"0.05","ServiceProviderName":"Anthropic","SkuMeter":"cache_write_5m","SkuPriceId":"anthropic:claude-sonnet-5:global:standard:all:cache_write_5m","SubAccountId":"apikey_01ExampleCi","Tags":{"costroid/cost_basis":"measured","costroid/model":"claude-sonnet-5"},"x_CostroidRowId":"row-2"}`,
	`{"BillingCurrency":"USD","ChargeDescription":"gpt-5.6-sol tokens","ChargePeriodStart":"2026-10-14T09:31:02Z","ConsumedQuantity":"20000","ConsumedUnit":"Tokens","HostProviderName":"OpenAI","ListCost":"0.4","ServiceProviderName":"OpenAI","SkuMeter":"output","SkuPriceId":"openai:gpt-5.6-sol:global:standard:to-272000:output","SubAccountId":"key_ExampleCi","Tags":{"costroid/cost_basis":"measured","costroid/model":"gpt-5.6-sol"},"x_CostroidRowId":"row-3"}`,
	`{"BillingCurrency":"USD","ConsumedQuantity":"7","ConsumedUnit":"Tokens","ListCost":"0","ServiceProviderName":"OpenAI","SkuMeter":"cache_read","SubAccountId":null,"ChargePeriodStart":"2026-10-14T23:59:59Z","Tags":{"costroid/cost_basis":"list_equivalent"},"x_CostroidRowId":"row-4"}`,
}

var ledgerRowPattern = regexp.MustCompile(`^reconcile: (read row|the rows file ends inside row) ([0-9]+)`)

func sampleDay(t testing.TB) Range {
	t.Helper()
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	return days
}

// assertLedger holds a ledger read to the rows it came from: no count, sum or
// repeat below zero, and every group inside the range read.
func assertLedger(t *testing.T, ledger *Ledger, within Range) {
	t.Helper()
	if ledger.Repeats < 0 || ledger.Uncredentialed < 0 {
		t.Fatalf("%d repeats, %d uncredentialed", ledger.Repeats, ledger.Uncredentialed)
	}
	for key, group := range ledger.groups {
		if !within.holds(key.Day) || key.Credential == "" || !slices.Contains([]string{SourceAnthropic, SourceOpenAI}, key.Source) {
			t.Fatalf("a group outside what was read: %+v", key)
		}
		if group.list.Sign() < 0 || group.rows < 0 {
			t.Fatalf("%+v: list %s over %d rows", key, group.list, group.rows)
		}
		for class, tokens := range group.tokens {
			if tokens.Sign() < 0 || class.Informational() {
				t.Fatalf("%+v: %s %s tokens", key, tokens, class)
			}
		}
	}
}

// FuzzReadLedger reads a rows file for reconciliation. A refusal names a row
// the file has, and a ledger read holds nothing below zero.
func FuzzReadLedger(f *testing.F) {
	f.Add([]byte(strings.Join(ledgerLines, "\n")))
	f.Add([]byte(ledgerLines[0] + ledgerLines[0]))
	for _, line := range ledgerLines {
		f.Add([]byte(line))
		f.Add([]byte(strings.Replace(line, `"ConsumedQuantity":"`, `"ConsumedQuantity":"-`, 1)))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		within := sampleDay(t)
		ledger, err := ReadLedger(bytes.NewReader(data), within, "apikey_fallback")
		if err != nil {
			match := ledgerRowPattern.FindStringSubmatch(err.Error())
			if match == nil {
				t.Fatalf("the refusal names no row: %v", err)
			}
			if row, _ := strconv.Atoi(match[2]); row < 1 || row > len(data)+1 {
				t.Fatalf("the refusal names row %d of %d bytes: %v", row, len(data), err)
			}
			return
		}
		assertLedger(t, ledger, within)
	})
}

// pageServer answers each report's first page with the body given and every
// later page with an empty last page, so a fuzzed has_more never loops.
type pageServer struct {
	usage, cost []byte
}

func (s pageServer) Do(request *http.Request) (*http.Response, error) {
	body := []byte(`{"data":[],"has_more":false}`)
	if request.URL.Query().Get("page") == "" {
		if strings.HasSuffix(request.URL.Path, "/usage") || strings.HasSuffix(request.URL.Path, "/completions") {
			body = s.usage
		} else {
			body = s.cost
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

// reportProviders are the two fetchers, by the byte that picks one.
func reportProvider(which uint8, server pageServer) Provider {
	if which%2 == 0 {
		return NewAnthropic(server, "sk-sample")
	}
	return NewOpenAI(server, "sk-sample")
}

// assertReport holds a report to what a comparison needs: every token count
// is a whole number at or above zero.
func assertReport(t *testing.T, report Report) {
	t.Helper()
	for _, usage := range report.Usage {
		if usage.Credential == "" {
			t.Fatalf("a record no credential owns was kept: %+v", usage)
		}
		for class, tokens := range usage.Tokens {
			if tokens.Sign() < 0 || tokens.Scale() != 0 {
				t.Fatalf("%s on %s: %s %s tokens, from the report as read", usage.Credential, usage.Day, tokens, class)
			}
		}
	}
	for _, cost := range report.Costs {
		if cost.Credential == "" {
			t.Fatalf("a cost no credential owns was kept: %+v", cost)
		}
	}
}

// assertResult holds a pass to its own definitions: a residual is the
// provider's figure less the ledger's, and the causes and the unattributed
// part add up to it.
func assertResult(t *testing.T, result Result) {
	t.Helper()
	for _, row := range result.Rows {
		if !slices.Contains([]State{Matched, TokensMiss, PriceMiss, Pending}, row.State) {
			t.Fatalf("a row in state %q", row.State)
		}
		if row.Dollars == nil {
			continue
		}
		check := row.Dollars
		if check.Provider.Sub(check.Ledger).Cmp(check.Residual) != 0 {
			t.Fatalf("%+v: residual %s is not %s less %s", row.Key, check.Residual, check.Provider, check.Ledger)
		}
		sum := check.Unattributed
		for _, cause := range check.Causes {
			sum = sum.Add(cause.Amount)
		}
		if sum.Cmp(check.Residual) != 0 {
			t.Fatalf("%+v: the causes and the unattributed part come to %s, the residual is %s", row.Key, sum, check.Residual)
		}
	}
}

// FuzzProviderReport answers a fetcher with a fuzzed usage page and cost page,
// then reconciles the samples' ledger with it and renders the table. The
// report holds no negative token count, a residual adds up, and nothing
// panics on a day, an amount or a cursor the provider wrote.
func FuzzProviderReport(f *testing.F) {
	for which, pairs := range [][][2]string{
		{{"usage.json", "cost-shared.json"}, {"usage-refreshing.json", "cost-refreshing.json"}, {"usage-page-1.json", "cost-at-list.json"}, {"usage-us.json", "cost-us.json"}, {"usage-batch.json", "cost-priority.json"}, {"usage-tiny-day.json", "cost-tiny-day-output-nine-percent-over.json"}, {"usage-priority.json", "cost-priority-by-token-type.json"}},
		{{"usage.json", "costs.json"}, {"usage-older-response.json", "costs-scientific.json"}, {"usage-twelve-hour-writes.json", "costs-at-list.json"}, {"usage-priority.json", "costs-batch.json"}, {"empty.json", "empty.json"}, {"usage.json", "costs-line-items.json"}},
	} {
		source := []string{"anthropic", "openai"}[which]
		for _, pair := range pairs {
			usage, err := os.ReadFile(filepath.Join(samples, source, pair[0]))
			if err != nil {
				f.Fatal(err)
			}
			cost, err := os.ReadFile(filepath.Join(samples, source, pair[1]))
			if err != nil {
				f.Fatal(err)
			}
			f.Add(uint8(which), usage, cost)
		}
	}
	f.Add(uint8(1), []byte(`{"data":[{"start_time":1791936000,"results":[{"api_key_id":"key_ExampleCi","model":"gpt-5.6-sol","input_tokens":10,"input_cached_tokens":20}]}]}`), []byte(`{"data":[]}`))
	pinned, err := catalog.Load()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, which uint8, usage, cost []byte) {
		within := sampleDay(t)
		provider := reportProvider(which, pageServer{usage, cost})
		ledger, err := ReadLedger(strings.NewReader(strings.Join(ledgerLines, "\n")), within, "")
		if err != nil {
			t.Fatal(err)
		}
		report, err := provider.Fetch(context.Background(), within, ledger.Credentials(provider.Name()))
		if err != nil {
			return
		}
		assertReport(t, report)
		result, err := Reconcile(context.Background(), ledger, Options{Range: within, Now: settledNow, Catalog: pinned, Providers: []Provider{provider}})
		if err != nil {
			return
		}
		assertResult(t, result)
		if err := Render(io.Discard, result); err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzParseRange reads the two days a pass covers. A range read holds its
// own days in order, and the windows a provider is asked for cover it with
// no gap and no overlap.
func FuzzParseRange(f *testing.F) {
	f.Add("2026-10-14", "2026-10-14")
	f.Add("2026-01-01", "2026-12-31")
	f.Add("2026-02-29", "2026-03-01")
	f.Add("0001-01-01", "9999-12-31")
	f.Fuzz(func(t *testing.T, from, to string) {
		days, err := ParseRange(from, to)
		if err != nil {
			return
		}
		if days.To.Before(days.From) || !days.holds(from) || !days.holds(to) {
			t.Fatalf("%s to %s read as %+v", from, to, days)
		}
		if days.To.Sub(days.From).Hours() > 24*400 {
			return
		}
		next := days.From
		for _, span := range days.windows(anthropicMaxSpan) {
			if !span.start.Equal(next) || !span.end.After(span.start) || span.end.Sub(span.start).Hours() > 24*anthropicMaxSpan {
				t.Fatalf("window %v to %v after %v", span.start, span.end, next)
			}
			next = span.end
		}
		if !next.Equal(days.To.AddDate(0, 0, 1)) {
			t.Fatalf("the windows end at %v, not after %v", next, days.To)
		}
	})
}

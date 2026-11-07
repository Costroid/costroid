package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// splitCostReport is the day's cost report split by model and token type, as
// the cost endpoint answers group_by[]=token_type. Each line bills sonnet's
// list price for the ledger's tokens, except output, which bills outputCents.
func (d day) splitCostReport(outputCents string) []byte {
	cents := []struct{ tokenType, amount string }{
		{"uncached_input_tokens", "20.000000"},
		{"cache_creation.ephemeral_5m_input_tokens", "5.000000"},
		{"cache_read_input_tokens", "6.000000"},
		{"output_tokens", outputCents},
	}
	var lines []string
	for _, line := range cents {
		lines = append(lines, fmt.Sprintf(`    {
      "starting_at": "%sT00:00:00Z",
      "ending_at": "%sT00:00:00Z",
      "group": {"api_key_id": %q, "model": %q, "service_tier": "standard", "inference_geo": "global", "speed": "standard", "cost_type": "tokens", "token_type": %q},
      "cost": {"currency": "USD", "amount": %q, "list_amount": %q}
    }`, d.date, d.next(), credential, model, line.tokenType, line.amount, line.amount))
	}
	return []byte(fmt.Sprintf(`{
  "x_synthetic": %q,
  "data": [
%s
  ],
  "has_more": false,
  "next_page": null,
  "data_refreshed_at": "%sT08:00:00Z"
}
`, syntheticNote, strings.Join(lines, ",\n"), d.next()))
}

// keepSplit saves one day through -save with a cost report split by token
// type.
func keepSplit(t *testing.T, inputs string, d day, outputCents string) {
	t.Helper()
	rows := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(rows, d.rows(t), 0o644); err != nil {
		t.Fatal(err)
	}
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		body := d.usageReport()
		if request.URL.Path == "/v1/organizations/analytics/cost" {
			body = d.splitCostReport(outputCents)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	outside := world{
		getenv: func(name string) string {
			if name == "ANTHROPIC_ADMIN_KEY" {
				return adminKey
			}
			return ""
		},
		client: client,
		now:    d.savedAt,
	}
	if got := runAccuracy(t, outside, "-save", filepath.Join(inputs, d.date), "-day", d.date, "-rows", rows); got.code != 0 {
		t.Fatalf("-save exited %d: %s", got.code, got.stderr)
	}
}

func TestAccuracyPageFailsOnPriceMiss(t *testing.T) {
	exact := t.TempDir()
	keepSplit(t, exact, passingDay(firstDay), "50.000000")
	page, got := writeAndCheck(t, exact, 1)
	if got.code != 0 {
		t.Fatalf("-check exited %d on a day whose prices match: %s", got.code, got.stderr)
	}
	if text := dayLine(t, page, firstDay); verdictOf(t, text) != "pass" {
		t.Errorf("verdict %q, want pass: %s", verdictOf(t, text), text)
	}

	missed := t.TempDir()
	keepSplit(t, missed, passingDay(firstDay), "50.050000")
	page, got = writeAndCheck(t, missed, 1)
	if got.code != exitRefused {
		t.Fatalf("-check exited %d on an output price of 10.01 per 1M against the catalog's 10, want %d", got.code, exitRefused)
	}
	text := dayLine(t, page, firstDay)
	if verdictOf(t, text) != failPrices {
		t.Errorf("verdict %q, want %q: %s", verdictOf(t, text), failPrices, text)
	}
	if !strings.Contains(text, "output 50000 / 50000 (0.00%)") || !strings.Contains(text, "0.81 / 0.8105 (0.06%)") {
		t.Errorf("tokens and the day's dollars should both read within tolerance, so only the price fails the day: %s", text)
	}
	if !strings.Contains(got.stderr, firstDay+" did not pass") {
		t.Errorf("-check did not name the day: %s", got.stderr)
	}
}

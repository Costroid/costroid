package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// servedDay is what the provider served on one day, by token type: the
// tokens and their list cost in cents. refreshed is the report's stamp, 08:00
// UTC the next day when empty.
type servedDay struct {
	date                         string
	input, write5m, read, output int64
	cents                        map[string]string
	refreshed                    string
}

// midnightDays are two kept days. The first day's run started at 23:50 UTC
// and ran past midnight, and the provider served three fifths of each class
// before midnight and two fifths after. The next day's provider report holds
// those two fifths beside the next day's own run.
func midnightDays() (day, day, []servedDay) {
	first, next := passingDay(firstDay), passingDay(passingDay(firstDay).next())
	served := []servedDay{
		{first.date, 60000, 12000, 180000, 30000, map[string]string{
			"uncached_input_tokens": "12.000000", "cache_creation.ephemeral_5m_input_tokens": "3.000000",
			"cache_read_input_tokens": "3.600000", "output_tokens": "30.000000",
		}, ""},
		{next.date, 140000, 28000, 420000, 70000, map[string]string{
			"uncached_input_tokens": "28.000000", "cache_creation.ephemeral_5m_input_tokens": "7.000000",
			"cache_read_input_tokens": "8.400000", "output_tokens": "70.000000",
		}, ""},
	}
	return first, next, served
}

// across moves a day's rows to one run from start to end.
func across(t *testing.T, rows []byte, start, end string) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, text := range strings.Split(strings.TrimSpace(string(rows)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(text), &row); err != nil {
			t.Fatal(err)
		}
		row["ChargePeriodStart"], row["ChargePeriodEnd"] = start, end
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(line, '\n'))
	}
	return out.Bytes()
}

// reports are one day's usage and cost reports, the cost split by token type.
func (s servedDay) reports(t *testing.T, next string) (usage, cost []byte) {
	t.Helper()
	start, end := s.date+"T00:00:00Z", next+"T00:00:00Z"
	refreshed := s.refreshed
	if refreshed == "" {
		refreshed = next + "T08:00:00Z"
	}
	page := func(data []map[string]any) []byte {
		body, err := json.Marshal(map[string]any{"x_synthetic": syntheticNote, "data": data, "has_more": false, "next_page": nil, "data_refreshed_at": refreshed})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	usage = page([]map[string]any{{
		"starting_at": start, "ending_at": end,
		"group": map[string]any{"api_key_id": credential, "model": model, "service_tier": "standard", "inference_geo": "global", "speed": "standard"},
		"usage": map[string]any{
			"uncached_input_tokens":   s.input,
			"cache_creation":          map[string]int64{"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": s.write5m},
			"cache_read_input_tokens": s.read,
			"output_tokens":           s.output,
		},
	}})
	var lines []map[string]any
	for tokenType, cents := range s.cents {
		lines = append(lines, map[string]any{
			"starting_at": start, "ending_at": end,
			"group": map[string]any{"api_key_id": credential, "cost_type": "tokens", "token_type": tokenType},
			"cost":  map[string]any{"currency": "USD", "amount": cents, "list_amount": cents},
		})
	}
	return usage, page(lines)
}

// keepServed saves one day through -save, with the rows given and the
// provider's reports for what it served that day.
func keepServed(t *testing.T, inputs string, d day, rows []byte, served servedDay) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.WriteFile(path, rows, 0o644); err != nil {
		t.Fatal(err)
	}
	usage, cost := served.reports(t, d.next())
	outside := world{
		getenv: func(name string) string {
			if name == "ANTHROPIC_ADMIN_KEY" {
				return adminKey
			}
			return ""
		},
		client: doerFunc(func(request *http.Request) (*http.Response, error) {
			body := map[string][]byte{"/v1/organizations/analytics/usage": usage, "/v1/organizations/analytics/cost": cost}[request.URL.Path]
			if body == nil {
				return nil, fmt.Errorf("no synthetic report for %s", request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
		}),
		now: d.savedAt,
	}
	if got := runAccuracy(t, outside, saveArgs(inputs, d, path)...); got.code != 0 {
		t.Fatalf("-save exited %d: %s", got.code, got.stderr)
	}
}

// A run across midnight is judged across its days: the tokens, and each
// class's dollars, of both days against both days of the provider's report,
// on the first day's line. Each day's dollar residual is the cause
// run_spans_days. Read a day at a time, the first day reads pending and the
// second misses.
func TestAccuracyJudgesAMidnightRunAcrossItsDays(t *testing.T) {
	first, next, served := midnightDays()
	inputs := t.TempDir()
	keepServed(t, inputs, first, across(t, first.rows(t), first.date+"T23:50:00Z", next.date+"T00:20:00Z"), served[0])
	keepServed(t, inputs, next, next.rows(t), served[1])
	page, got := writeAndCheck(t, inputs, 2)
	if got.code != 0 {
		t.Errorf("-check exited %d: %s", got.code, got.stderr)
	}
	for _, date := range []string{first.date, next.date} {
		if text := dayLine(t, page, date); verdictOf(t, text) != "pass after named causes" || !strings.Contains(text, "| run_spans_days ") {
			t.Errorf("%s: verdict %q, want a pass after run_spans_days: %s", date, verdictOf(t, text), text)
		}
	}
	text := dayLine(t, page, first.date)
	for _, want := range []string{string(focus.Output) + " 100000 / 100000 (0.00%)", string(focus.Output) + " 1 / 1 (0.00%)"} {
		if !strings.Contains(text, want) {
			t.Errorf("the first day does not state both days' %q: %s", want, text)
		}
	}
}

// A day whose own saved report the provider stamped before the day ended is
// not settled, and a later day's stamp never settles it: its group is not
// joined, so the first day reads pending, as it does alone.
func TestAnUnsettledDayIsNotJoined(t *testing.T) {
	first, next, served := midnightDays()
	served[0].refreshed = first.date + "T20:00:00Z"
	inputs := t.TempDir()
	keepServed(t, inputs, first, across(t, first.rows(t), first.date+"T23:50:00Z", next.date+"T00:20:00Z"), served[0])
	keepServed(t, inputs, next, next.rows(t), served[1])
	page, _ := writeAndCheck(t, inputs, 2)
	if text := dayLine(t, page, first.date); verdictOf(t, text) != "pending" {
		t.Errorf("verdict %q, want pending: %s", verdictOf(t, text), text)
	}
}

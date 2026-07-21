// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing/fstest"
	"unicode/utf16"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/nlquery"
)

const exportUsage = `usage: costroid export <resource> [flags]

resources:
  costs-daily      GET /api/v1/costs/daily
  costs-summary    GET /api/v1/costs/summary
  anomalies        GET /api/v1/anomalies
  tokens           GET /api/v1/usage/tokens/daily
  usage            GET /api/v1/usage/metrics/daily
  unit-economics   GET /api/v1/unit-economics/daily

flags (common):
  --format csv|json   output format (default csv)
  --out <path>        write to a file instead of stdout
  --db-encryption-key-file <path>
                      at-rest DATABASE-encryption key file (same resolution as serve)
  --allocation-rules <path>
                      allocation rules JSON path (same precedence as serve; used when --group-by allocation)

resource flags:
  costs-daily / costs-summary / anomalies:
    --start --end --group-by --tag-key --currency --provider
  tokens / usage:
    --start --end
  unit-economics:
    --metric --start --end --currency --provider

--group-by accepts service|provider|allocation|subaccount|region|tag
(tag requires --tag-key). Semantic validation is the API's job: bad values
relay as export errors with the response body text.

Offline only: stop 'costroid serve' first (single-writer store). Success is
silent - stdout receives EXACTLY the export bytes (no summary line). With
--out, the file receives the bytes and stdout is empty.

CSV dialect matches the dashboard Download CSV buttons (RFC 4180 quoting,
CRLF rows). stdout carries NO UTF-8 BOM (pipes stay clean for cut/awk/join);
--out prepends the UTF-8 BOM for csv so Excel opens UTF-8 correctly. json
never gets a BOM.

One-shot only: each invocation exports once to stdout or a single --out path.
Automated delivery is deliberately out of scope`

// exportCmd runs a one-shot offline export of one API resource through the
// real serve handler in process (no network, no auth).
func exportCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("missing export resource\n" + exportUsage)
	}
	resource := args[0]
	spec, ok := nlquery.Endpoints[resource]
	if !ok {
		return fmt.Errorf("unknown export resource %q (want %s)", resource, nlquery.EndpointList)
	}

	flags := flag.NewFlagSet("export "+resource, flag.ContinueOnError)
	formatFlag := flags.String("format", "csv", "output format: csv or json (default csv)")
	outFlag := flags.String("out", "", "write export bytes to this path instead of stdout")
	startFlag := flags.String("start", "", "inclusive start date YYYY-MM-DD")
	endFlag := flags.String("end", "", "inclusive end date YYYY-MM-DD")
	groupByFlag := flags.String("group-by", "", "grouping dimension (service|provider|allocation|subaccount|region|tag)")
	tagKeyFlag := flags.String("tag-key", "", "FOCUS Tags key (required when --group-by tag)")
	currencyFlag := flags.String("currency", "", "billing currency filter (three-letter uppercase)")
	providerFlag := flags.String("provider", "", "FOCUS ServiceProviderName filter")
	metricFlag := flags.String("metric", "", "business metric name (unit-economics)")
	allocationRulesFlag := flags.String("allocation-rules", "", allocationRulesFlagUsage)
	dbEncryptionKeyFileFlag := flags.String("db-encryption-key-file", "", dbEncryptionKeyFileUsage)
	if stop, err := parseFlags(flags, args[1:]); stop || err != nil {
		return err
	}

	format := strings.ToLower(*formatFlag)
	if format != "csv" && format != "json" {
		return fmt.Errorf("--format must be csv or json, got %q", *formatFlag)
	}

	q := url.Values{}
	if *startFlag != "" {
		q.Set("start", *startFlag)
	}
	if *endFlag != "" {
		q.Set("end", *endFlag)
	}
	if *groupByFlag != "" {
		q.Set("groupBy", *groupByFlag)
	}
	if *tagKeyFlag != "" {
		q.Set("tagKey", *tagKeyFlag)
	}
	if *currencyFlag != "" {
		q.Set("currency", *currencyFlag)
	}
	if *providerFlag != "" {
		q.Set("provider", *providerFlag)
	}
	if *metricFlag != "" {
		q.Set("metric", *metricFlag)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := openStore(ctx, *dbEncryptionKeyFileFlag)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	rulesPath := resolveAllocationRulesPath(*allocationRulesFlag)
	handler := api.NewHandler(version, fstest.MapFS{}, store, rulesPath)

	reqURL := spec.Path
	if enc := q.Encode(); enc != "" {
		reqURL += "?" + enc
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		body := strings.TrimSpace(rec.Body.String())
		if body == "" {
			body = http.StatusText(rec.Code)
		}
		return fmt.Errorf("export %s: %s", resource, body)
	}

	var payload []byte
	if format == "json" {
		// Verbatim response body bytes - no unmarshal/re-marshal.
		payload = append([]byte(nil), rec.Body.Bytes()...)
	} else {
		payload, err = serializeExportCSV(resource, rec.Body.Bytes())
		if err != nil {
			return fmt.Errorf("export %s: %w", resource, err)
		}
	}

	if *outFlag != "" {
		outBytes := payload
		if format == "csv" {
			outBytes = append(utf8BOM, payload...)
		}
		if err := os.WriteFile(*outFlag, outBytes, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", *outFlag, err)
		}
		return nil
	}
	_, err = os.Stdout.Write(payload)
	return err
}

// utf8BOM is the three-byte UTF-8 BOM prepended only for csv --out files
// (Excel affordance; matches dashboard downloads). stdout never includes it.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// serializeExportCSV unmarshals the wire JSON for resource and returns the
// CSV body WITHOUT a BOM (caller decides BOM for --out).
func serializeExportCSV(resource string, body []byte) ([]byte, error) {
	switch resource {
	case "costs-daily":
		var v dailyCostsWire
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		return dailyCostsToCSV(v), nil
	case "costs-summary":
		var v costsSummaryWire
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		return costsSummaryToCSV(v), nil
	case "anomalies":
		var v anomaliesWire
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		return anomaliesToCSV(v), nil
	case "tokens":
		var rows []dailyTokenUsageWire
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, err
		}
		return dailyTokensToCSV(pivotTokensByDate(rows)), nil
	case "usage":
		var rows []dailyUsageMetricWire
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, err
		}
		return usageMetricsToCSV(rows), nil
	case "unit-economics":
		var v unitEconomicsWire
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		return unitEconomicsToCSV(v), nil
	default:
		return nil, fmt.Errorf("unknown resource %q", resource)
	}
}

// Wire DTOs use string fields for money/quantities and *string for optional
// fields so absence vs present-"0" stays distinguishable. Dates stay strings
// (YYYY-MM-DD on the wire).

type serviceCostWire struct {
	Key  string `json:"key"`
	Cost string `json:"cost"`
}

type dailyCostWire struct {
	Date     string            `json:"date"`
	Total    string            `json:"total"`
	Services []serviceCostWire `json:"services"`
}

type dailyCostsWire struct {
	Days []dailyCostWire `json:"days"`
}

type costSummaryKeyWire struct {
	Key           string  `json:"key"`
	Total         string  `json:"total"`
	PreviousTotal *string `json:"previousTotal"`
	Delta         *string `json:"delta"`
}

type costsSummaryWire struct {
	Keys []costSummaryKeyWire `json:"keys"`
}

type anomalyWire struct {
	Date      string  `json:"date"`
	Scope     string  `json:"scope"`
	Key       *string `json:"key"`
	Direction string  `json:"direction"`
	Observed  string  `json:"observed"`
	Median    string  `json:"median"`
	Mad       string  `json:"mad"`
	ScaledMad string  `json:"scaledMad"`
	Threshold string  `json:"threshold"`
	Deviation string  `json:"deviation"`
}

type anomaliesWire struct {
	Anomalies []anomalyWire `json:"anomalies"`
}

type dailyTokenUsageWire struct {
	Date             string `json:"date"`
	ServiceName      string `json:"serviceName"`
	ConsumedUnit     string `json:"consumedUnit"`
	ConsumedQuantity string `json:"consumedQuantity"`
}

type tokenServiceWire struct {
	ServiceName string `json:"serviceName"`
	Quantity    string `json:"quantity"`
}

// tokenDayGroup is the pivoted form of daily token rows (web DayGroup).
type tokenDayGroup struct {
	Date     string             `json:"date"`
	Services []tokenServiceWire `json:"services"`
	Total    *string            `json:"total"`
}

type dailyUsageMetricWire struct {
	Date        string `json:"date"`
	ServiceName string `json:"serviceName"`
	ServiceTier string `json:"serviceTier"`
	MetricName  string `json:"metricName"`
	Unit        string `json:"unit"`
	Quantity    string `json:"quantity"`
}

type unitEconomicsDayWire struct {
	Date     string  `json:"date"`
	Cost     *string `json:"cost"`
	Quantity *string `json:"quantity"`
	UnitCost *string `json:"unitCost"`
}

type unitEconomicsWire struct {
	Days []unitEconomicsDayWire `json:"days"`
}

// csvField encodes one field per RFC 4180: a field containing a comma, a
// double-quote, CR, or LF is wrapped in double-quotes with every inner
// double-quote doubled.
func csvField(value string) string {
	if strings.ContainsAny(value, ",\"\r\n") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

// joinCSVRows joins rows with CRLF and appends a trailing CRLF. No BOM.
func joinCSVRows(rows [][]string) []byte {
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(csvField(cell))
		}
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// lessByUTF16 reports whether a sorts before b by UTF-16 code units, matching
// JavaScript's default string .sort() (not Go's UTF-8 byte order).
func lessByUTF16(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	n := len(ua)
	if len(ub) < n {
		n = len(ub)
	}
	for i := 0; i < n; i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// sortByUTF16 sorts ss in place by UTF-16 code units.
func sortByUTF16(ss []string) {
	sort.Slice(ss, func(i, j int) bool { return lessByUTF16(ss[i], ss[j]) })
}

// dailyCostsToCSV serializes the wide daily cost grid: Date, one column per
// grouping key (UTF-16 sorted), Total (net). Money cells are verbatim wire
// strings; an absent group is an empty cell.
func dailyCostsToCSV(costs dailyCostsWire) []byte {
	keySet := map[string]struct{}{}
	for _, day := range costs.Days {
		for _, s := range day.Services {
			keySet[s.Key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sortByUTF16(keys)

	header := make([]string, 0, 2+len(keys))
	header = append(header, "Date")
	header = append(header, keys...)
	header = append(header, "Total (net)")

	rows := make([][]string, 0, 1+len(costs.Days))
	rows = append(rows, header)
	for _, day := range costs.Days {
		byKey := make(map[string]string, len(day.Services))
		for _, s := range day.Services {
			byKey[s.Key] = s.Cost
		}
		row := make([]string, 0, 2+len(keys))
		row = append(row, day.Date)
		for _, k := range keys {
			row = append(row, byKey[k]) // absent -> ""
		}
		row = append(row, day.Total)
		rows = append(rows, row)
	}
	return joinCSVRows(rows)
}

// costsSummaryToCSV serializes Key,Total,Previous total,Delta in wire key
// order. Absent previousTotal/delta are empty cells (never fabricated 0).
func costsSummaryToCSV(summary costsSummaryWire) []byte {
	rows := make([][]string, 0, 1+len(summary.Keys))
	rows = append(rows, []string{"Key", "Total", "Previous total", "Delta"})
	for _, k := range summary.Keys {
		prev, delta := "", ""
		if k.PreviousTotal != nil {
			prev = *k.PreviousTotal
		}
		if k.Delta != nil {
			delta = *k.Delta
		}
		rows = append(rows, []string{k.Key, k.Total, prev, delta})
	}
	return joinCSVRows(rows)
}

// anomaliesToCSV serializes anomaly body rows in wire order. Key is empty when
// scope is "total" (so a literal key named "total" stays unambiguous).
func anomaliesToCSV(a anomaliesWire) []byte {
	rows := make([][]string, 0, 1+len(a.Anomalies))
	rows = append(rows, []string{
		"Date", "Scope", "Key", "Direction", "Observed", "Median", "MAD", "Scaled MAD", "Threshold", "Deviation",
	})
	for _, an := range a.Anomalies {
		key := ""
		if an.Scope != "total" && an.Key != nil {
			key = *an.Key
		}
		rows = append(rows, []string{
			an.Date, an.Scope, key, an.Direction,
			an.Observed, an.Median, an.Mad, an.ScaledMad, an.Threshold, an.Deviation,
		})
	}
	return joinCSVRows(rows)
}

// nonNegIntegerRE matches non-negative integer decimal strings only. A leading
// sign (+5/-5) is NOT an integer here - mirrors the web /^\d+$/ rule.
var nonNegIntegerRE = regexp.MustCompile(`^[0-9]+$`)

// sumIntegerStrings returns the exact big-integer sum of quantities when every
// member matches ^[0-9]+$, otherwise nil. An empty list yields "0".
func sumIntegerStrings(quantities []string) *string {
	if len(quantities) == 0 {
		z := "0"
		return &z
	}
	for _, q := range quantities {
		if !nonNegIntegerRE.MatchString(q) {
			return nil
		}
	}
	sum := new(big.Int)
	tmp := new(big.Int)
	for _, q := range quantities {
		if _, ok := tmp.SetString(q, 10); !ok {
			return nil
		}
		sum.Add(sum, tmp)
	}
	s := sum.String()
	return &s
}

// pivotTokensByDate replicates the dashboard groupByDate pivot: sorted dates,
// pairwise incremental fold of duplicate (date, service) quantities, and a
// day Total that is empty when any final cell is non-integer.
func pivotTokensByDate(rows []dailyTokenUsageWire) []tokenDayGroup {
	byDate := map[string]map[string]string{}
	for _, row := range rows {
		services, ok := byDate[row.Date]
		if !ok {
			services = map[string]string{}
			byDate[row.Date] = services
		}
		prev, exists := services[row.ServiceName]
		if !exists {
			services[row.ServiceName] = row.ConsumedQuantity
			continue
		}
		if summed := sumIntegerStrings([]string{prev, row.ConsumedQuantity}); summed != nil {
			services[row.ServiceName] = *summed
		} else {
			// Fall back to the incoming raw wire string.
			services[row.ServiceName] = row.ConsumedQuantity
		}
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates) // YYYY-MM-DD sorts correctly as UTF-8/UTF-16 alike

	out := make([]tokenDayGroup, 0, len(dates))
	for _, date := range dates {
		svcMap := byDate[date]
		names := make([]string, 0, len(svcMap))
		for name := range svcMap {
			names = append(names, name)
		}
		sortByUTF16(names)
		services := make([]tokenServiceWire, 0, len(names))
		qtys := make([]string, 0, len(names))
		for _, name := range names {
			q := svcMap[name]
			services = append(services, tokenServiceWire{ServiceName: name, Quantity: q})
			qtys = append(qtys, q)
		}
		out = append(out, tokenDayGroup{
			Date:     date,
			Services: services,
			Total:    sumIntegerStrings(qtys),
		})
	}
	return out
}

// dailyTokensToCSV serializes the wide token grid from pivoted day groups.
func dailyTokensToCSV(days []tokenDayGroup) []byte {
	svcSet := map[string]struct{}{}
	for _, day := range days {
		for _, s := range day.Services {
			svcSet[s.ServiceName] = struct{}{}
		}
	}
	services := make([]string, 0, len(svcSet))
	for name := range svcSet {
		services = append(services, name)
	}
	sortByUTF16(services)

	header := make([]string, 0, 2+len(services))
	header = append(header, "Date")
	header = append(header, services...)
	header = append(header, "Total")

	rows := make([][]string, 0, 1+len(days))
	rows = append(rows, header)
	for _, day := range days {
		byName := make(map[string]string, len(day.Services))
		for _, s := range day.Services {
			byName[s.ServiceName] = s.Quantity
		}
		row := make([]string, 0, 2+len(services))
		row = append(row, day.Date)
		for _, name := range services {
			row = append(row, byName[name])
		}
		total := ""
		if day.Total != nil {
			total = *day.Total
		}
		row = append(row, total)
		rows = append(rows, row)
	}
	return joinCSVRows(rows)
}

// usageMetricsToCSV serializes the long wire form in wire order.
func usageMetricsToCSV(metrics []dailyUsageMetricWire) []byte {
	rows := make([][]string, 0, 1+len(metrics))
	rows = append(rows, []string{"Date", "Service", "Tier", "Metric", "Unit", "Quantity"})
	for _, m := range metrics {
		rows = append(rows, []string{
			m.Date, m.ServiceName, m.ServiceTier, m.MetricName, m.Unit, m.Quantity,
		})
	}
	return joinCSVRows(rows)
}

// unitEconomicsToCSV serializes Date,Cost,Quantity,Unit cost with optional
// fields empty only when absent (present "0" stays "0").
func unitEconomicsToCSV(economics unitEconomicsWire) []byte {
	rows := make([][]string, 0, 1+len(economics.Days))
	rows = append(rows, []string{"Date", "Cost", "Quantity", "Unit cost"})
	for _, day := range economics.Days {
		cost, qty, uc := "", "", ""
		if day.Cost != nil {
			cost = *day.Cost
		}
		if day.Quantity != nil {
			qty = *day.Quantity
		}
		if day.UnitCost != nil {
			uc = *day.UnitCost
		}
		rows = append(rows, []string{day.Date, cost, qty, uc})
	}
	return joinCSVRows(rows)
}

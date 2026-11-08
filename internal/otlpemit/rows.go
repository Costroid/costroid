// Package otlpemit sends a rows file to an OpenTelemetry backend as traces
// over OTLP/HTTP: one span per model request.
//
// Token counts travel as integers under the OpenTelemetry GenAI attributes. A
// request's cost travels as three strings, costroid.cost.amount,
// costroid.cost.currency and costroid.cost.basis, so no backend reads it as a
// float and rounds it. Amounts are summed only within one request, whose rows
// share one currency.
package otlpemit

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// maxLineBytes bounds one line of a rows file, as the row contract's CSV
// writer does.
const maxLineBytes = 1 << 20

// classOrder is the order in which a request's rows are written, one row per
// class. A row whose class does not come after the previous row's starts the
// next request.
var classOrder = []focus.TokenClass{
	focus.InputUncached, focus.CacheRead,
	focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m, focus.CacheWrite12h,
	focus.Output, focus.ReasoningOutput, focus.CachedInputReported,
}

// row holds the columns of a rows line that a span reads.
type row struct {
	BillingAccountID  string            `json:"BillingAccountId"`
	BillingCurrency   string            `json:"BillingCurrency"`
	ChargePeriodEnd   string            `json:"ChargePeriodEnd"`
	ChargePeriodStart string            `json:"ChargePeriodStart"`
	ConsumedQuantity  string            `json:"ConsumedQuantity"`
	ListCost          string            `json:"ListCost"`
	RegionID          *string           `json:"RegionId"`
	SkuID             string            `json:"SkuId"`
	SkuMeter          focus.TokenClass  `json:"SkuMeter"`
	SubAccountID      *string           `json:"SubAccountId"`
	Tags              map[string]string `json:"Tags"`
	CatalogVersion    *string           `json:"x_CostroidCatalogVersion"`
	RowID             string            `json:"x_CostroidRowId"`
	ServiceTier       *string           `json:"x_CostroidServiceTier"`
}

// request is the rows of one model request.
type request struct {
	provider, model string
	start, end      time.Time
	currency        string
	cost            decimal.Decimal
	tokens          map[focus.TokenClass]int64
	tags            map[string]string
	serviceTier     string
	catalogVersion  string
	rowIDs          []string
	// key is what every row of the request shares.
	key string
	// last is the place in classOrder of the request's last row.
	last int
}

// readRequests reads a rows file into its requests. The file is first held
// to the row contract, so a line that is not a row refuses the whole file.
func readRequests(data []byte) ([]*request, int, error) {
	if _, err := focus.WriteCSV(io.Discard, bytes.NewReader(data)); err != nil {
		return nil, 0, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, maxLineBytes)
	var requests []*request
	rows := 0
	for line := 1; scanner.Scan(); line++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var r row
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return nil, 0, fmt.Errorf("rows line %d: %w", line, err)
		}
		next, err := r.request()
		if err != nil {
			return nil, 0, fmt.Errorf("rows line %d: %w", line, err)
		}
		rows++
		if len(requests) > 0 && requests[len(requests)-1].takes(next) {
			requests[len(requests)-1].add(next)
			continue
		}
		requests = append(requests, next)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read rows: %w", err)
	}
	return requests, rows, nil
}

// request reads one row as a request of that one row.
func (r *row) request() (*request, error) {
	place := slices.Index(classOrder, r.SkuMeter)
	if place < 0 {
		return nil, fmt.Errorf("SkuMeter %q is no token class", r.SkuMeter)
	}
	provider, rest, found := strings.Cut(r.SkuID, "/")
	cut := strings.LastIndex(rest, "/")
	if !found || cut <= 0 || provider == "" {
		return nil, fmt.Errorf("SkuId %q is not provider/model/class", r.SkuID)
	}
	tokens, err := strconv.ParseInt(r.ConsumedQuantity, 10, 64)
	if err != nil || tokens < 0 {
		return nil, fmt.Errorf("ConsumedQuantity %q is not a whole number of tokens", r.ConsumedQuantity)
	}
	cost, err := decimal.Parse(r.ListCost)
	if err != nil {
		return nil, fmt.Errorf("ListCost: %w", err)
	}
	start, err := time.Parse(time.RFC3339, r.ChargePeriodStart)
	if err != nil {
		return nil, fmt.Errorf("ChargePeriodStart: %w", err)
	}
	end, err := time.Parse(time.RFC3339, r.ChargePeriodEnd)
	if err != nil {
		return nil, fmt.Errorf("ChargePeriodEnd: %w", err)
	}
	tags := maps.Clone(r.Tags)
	delete(tags, focus.TagAssumedTTL)
	next := &request{
		provider:       provider,
		model:          rest[:cut],
		start:          start,
		end:            end,
		currency:       r.BillingCurrency,
		cost:           cost,
		tokens:         map[focus.TokenClass]int64{r.SkuMeter: tokens},
		tags:           tags,
		serviceTier:    deref(r.ServiceTier),
		catalogVersion: deref(r.CatalogVersion),
		rowIDs:         []string{r.RowID},
		last:           place,
	}
	next.key = r.key(next)
	return next, nil
}

// key joins every column the rows of one request share. A tag a single class
// carries, costroid/assumed_ttl, is left out.
func (r *row) key(next *request) string {
	parts := []string{
		next.provider, next.model, r.ChargePeriodStart, r.ChargePeriodEnd, r.BillingCurrency,
		r.BillingAccountID, deref(r.SubAccountID), deref(r.RegionID), next.serviceTier, next.catalogVersion,
	}
	for _, name := range slices.Sorted(maps.Keys(next.tags)) {
		parts = append(parts, name, next.tags[name])
	}
	sum := sha256.New()
	for _, part := range parts {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return string(sum.Sum(nil))
}

// takes reports whether next is the following row of this request: it shares
// every column, and its class comes after the last one taken.
func (q *request) takes(next *request) bool {
	return q.key == next.key && next.last > q.last
}

// add takes next's row into the request. Its cost is added exactly: both
// rows are in the request's one currency.
func (q *request) add(next *request) {
	maps.Copy(q.tokens, next.tokens)
	q.cost = q.cost.Add(next.cost)
	q.rowIDs = append(q.rowIDs, next.rowIDs...)
	q.last = next.last
}

func deref(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

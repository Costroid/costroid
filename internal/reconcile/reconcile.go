// Package reconcile compares the ledger with the provider's own reports, per
// credential per day. Tokens must match by class within 1%, and a miss is a
// defect. Each price the cost report implies must equal the catalog's to the
// report's last stated digit, and a miss is a defect. List-price dollars are
// compared per class within 1%, then for the day within 10% of the cost
// report, and the residual is split into named causes.
//
// The fetchers hold a provider admin key, so this package runs only in the
// customer's own process, and a Row carries daily totals and nothing else.
package reconcile

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reconcile/state"
)

// The sources this package reconciles against.
const (
	SourceAnthropic = "anthropic"
	SourceOpenAI    = "openai"
)

// The tolerances of SPEC.md A item 8, as percentages of the provider's figure.
const (
	// TokenTolerancePercent bounds each token class. A miss is a defect.
	TokenTolerancePercent = 1
	// DollarTolerancePercent bounds list-price dollars against the cost report.
	DollarTolerancePercent = 10
)

// RerunDays is how many days after month end a day's figures can still move.
// Providers restate for seven, and the job runs one day past that.
const RerunDays = 8

// State is a row's verdict on its tokens. It lives in a package of its own,
// so the comment renders it without reaching this package's client.
type State = state.State

// The states of a Row.
const (
	Matched    = state.Matched
	TokensMiss = state.TokensMiss
	PriceMiss  = state.PriceMiss
	Pending    = state.Pending
)

// DollarVerdict is a row's verdict on its cost.
type DollarVerdict string

// The verdicts of a DollarCheck.
const (
	Within  DollarVerdict = "within"
	Outside DollarVerdict = "outside"
	// NoBill means every ledger row is list-equivalent. No API bill exists to
	// compare with, so no figure stands against the provider's.
	NoBill DollarVerdict = "no_bill"
)

// CauseName is a named cause of a dollar residual.
type CauseName string

// The causes SPEC.md A item 8 names.
const (
	CauseSharedKey    CauseName = "shared_key"
	CauseTier         CauseName = "tier"
	CauseResidency    CauseName = "residency"
	CauseContractRate CauseName = "contract_rate"
	// CauseRunSpansDays is a run that crossed midnight UTC. The ledger dates a
	// run's tokens by the run's start, and the provider dates each request by
	// when it was served, so the days differ and together they match. It is
	// the cause a Span names. As a residual's cause it would be the dollars a
	// run moved from one day to another, and a span's dollars are compared
	// over all its days, so no residual names it.
	CauseRunSpansDays CauseName = "run_spans_days"
)

// Key is what makes a Row idempotent.
type Key struct {
	Source     string `json:"source"`
	Credential string `json:"credential"`
	Day        string `json:"day"`
}

// Row is one reconciliation of one credential on one UTC day.
// DataRefreshedAt is the provider's stamp on the reports that carried the day,
// the earliest when several did, and null when the provider stamps none.
// Currency is the currency of every amount in the row. Rows and reports in any
// other currency stop the pass before a row is written, so a row never holds
// two.
type Row struct {
	Key
	State           State        `json:"state"`
	Final           bool         `json:"final"`
	DataRefreshedAt *time.Time   `json:"data_refreshed_at"`
	Currency        string       `json:"currency"`
	Tokens          []TokenCheck `json:"tokens"`
	Prices          []PriceCheck `json:"prices"`
	Dollars         *DollarCheck `json:"dollars"`
	Span            *Span        `json:"span"`
	Notes           []string     `json:"notes"`
}

// laterInSpan reports a later day of a span, which holds no comparison of its
// own and reads by the row of the span's first day.
func (r Row) laterInSpan() bool {
	return r.Span != nil && r.Day != r.Span.From
}

// Span names the days a run crossed midnight UTC over, compared as one
// because a day of them missed on its own and together they matched. The row
// of From holds the joined token comparison and the joined dollar comparison,
// and the rows of later days hold neither. Each day keeps its own prices, and
// a price that misses on any day makes every day of the span price_miss. It
// is null on a row compared on its own day.
type Span struct {
	Cause CauseName `json:"cause"`
	From  string    `json:"from"`
	To    string    `json:"to"`
}

// TokenCheck compares one token class. Delta is Provider minus Ledger.
type TokenCheck struct {
	Class    focus.TokenClass `json:"class"`
	Ledger   decimal.Decimal  `json:"ledger"`
	Provider decimal.Decimal  `json:"provider"`
	Delta    decimal.Decimal  `json:"delta"`
	Within   bool             `json:"within"`
}

// DollarCheck compares list-price ledger cost with the cost report, for the
// day or, on a span's first day, for every day of the span together. Residual
// is Provider minus Ledger, and it equals the sum of Causes and Unattributed.
// OtherCost is what the report bills for anything but tokens, which the ledger
// does not hold and the comparison leaves out.
//
// ProviderEffective is what the report bills for tokens, after any discount,
// and equals Provider. ProviderList is the same charges at list price, null
// when the report does not state it on every token line. Rounding compares
// ProviderList with Ledger under FOCUS 1.4's rounding variance tolerance, and
// is null when ProviderList is.
//
// Classes is the first line: each class at list price on both sides, null
// when the report does not split token cost by class. Verdict is the second
// line, the day within 10%.
type DollarCheck struct {
	Classes           []ClassDollars   `json:"classes"`
	Verdict           DollarVerdict    `json:"verdict"`
	Ledger            decimal.Decimal  `json:"ledger"`
	Provider          decimal.Decimal  `json:"provider"`
	ProviderEffective decimal.Decimal  `json:"provider_effective"`
	ProviderList      *decimal.Decimal `json:"provider_list"`
	OtherCost         decimal.Decimal  `json:"other_cost"`
	Residual          decimal.Decimal  `json:"residual"`
	Causes            []Cause          `json:"causes"`
	Unattributed      decimal.Decimal  `json:"unattributed"`
	Rounding          *RoundingCheck   `json:"rounding"`
}

// Cause is the part of a residual one named cause accounts for.
type Cause struct {
	Name   CauseName       `json:"name"`
	Amount decimal.Decimal `json:"amount"`
}

// Unmatched is a credential the provider reports and the ledger never names.
type Unmatched struct {
	Source     string
	Credential string
}

// Result is one reconciliation pass. Refused holds the credential days whose
// row would carry a figure no row can, which are not in Rows.
type Result struct {
	Rows      []Row
	Unmatched []Unmatched
	Refused   []*FigureError
}

// Misses counts the comparisons that hold a defect: a token miss, a price
// miss or both. A span is one comparison, counted on its first day.
func (r Result) Misses() int {
	misses := 0
	for _, row := range r.Rows {
		if !row.laterInSpan() && (row.State == TokensMiss || row.State == PriceMiss) {
			misses++
		}
	}
	return misses
}

// Options configures one pass.
type Options struct {
	Range     Range
	Now       time.Time
	Catalog   *catalog.Catalog
	Providers []Provider
}

// Reconcile fetches each provider's report and compares it with the ledger.
func Reconcile(ctx context.Context, ledger *Ledger, options Options) (Result, error) {
	var result Result
	for _, provider := range options.Providers {
		credentials := ledger.Credentials(provider.Name())
		report, err := provider.Fetch(ctx, options.Range, credentials)
		if err != nil {
			return Result{}, fmt.Errorf("reconcile: %s: %w", provider.Name(), err)
		}
		pass := comparison{ledger: ledger, report: report, options: options, named: credentials}
		rows, err := pass.rows()
		if err != nil {
			return Result{}, fmt.Errorf("reconcile: %s: %w", provider.Name(), err)
		}
		for _, row := range rows {
			if refused := formOf(row); refused != nil {
				result.Refused = append(result.Refused, refused)
				continue
			}
			result.Rows = append(result.Rows, row)
		}
		result.Unmatched = append(result.Unmatched, pass.unmatched()...)
	}
	slices.SortFunc(result.Rows, func(a, b Row) int { return compareKeys(a.Key, b.Key) })
	slices.SortFunc(result.Refused, func(a, b *FigureError) int { return compareKeys(a.Key, b.Key) })
	return result, nil
}

func compareKeys(a, b Key) int {
	return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Credential, b.Credential), cmp.Compare(a.Day, b.Day))
}

type comparison struct {
	ledger  *Ledger
	report  Report
	options Options
	named   []string
}

// keys lists every credential and day with data on either side.
func (c comparison) keys() []Key {
	seen := map[Key]bool{}
	for key := range c.ledger.groups {
		if key.Source == c.report.Source {
			seen[key] = true
		}
	}
	add := func(credential, day string) {
		if slices.Contains(c.named, credential) && c.options.Range.holds(day) {
			seen[Key{c.report.Source, credential, day}] = true
		}
	}
	for _, usage := range c.report.Usage {
		add(usage.Credential, usage.Day)
	}
	for _, cost := range c.report.Costs {
		add(cost.Credential, cost.Day)
	}
	keys := make([]Key, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	return keys
}

func (c comparison) rows() ([]Row, error) {
	var rows []Row
	for _, key := range c.keys() {
		row, err := c.row(key)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	spanStates(rows)
	return rows, nil
}

func (c comparison) row(key Key) (Row, error) {
	row := Row{Key: key, Final: isFinal(key.Day, c.options.Now), DataRefreshedAt: c.report.refreshedAt(key.Day), Currency: currency, Notes: []string{}}
	if !c.settled(key.Day) {
		row.State = Pending
		row.Notes = append(row.Notes, "the provider has not finished reporting this day")
		return row, nil
	}
	group := c.ledger.group(key)
	usage := c.report.usageOf(key)
	row.Tokens = compareTokens(key.Source, group, usage)
	joined, err := c.joinSpan(key, &row)
	if err != nil || row.State == Pending {
		return row, err
	}
	row.State = Matched
	if missed(row.Tokens) {
		row.State = TokensMiss
		row.Notes = append(row.Notes, "tokens missed the 1% tolerance: this is a defect")
	}
	if slices.ContainsFunc(joined, func(check TokenCheck) bool { return check.Class == cacheWrite }) {
		row.Notes = append(row.Notes, "cache writes compared as one figure, because the ledger cannot see their lifetime")
	}
	if group.upstream {
		row.Notes = append(row.Notes, "rows drawn on a gateway's run keys are compared under the provider key the gateway holds, so the provider's figure counts every request the gateway sent with it")
	}
	prices, notes, err := c.prices(key, group, usage)
	if err != nil {
		return Row{}, err
	}
	row.Prices = prices
	row.Notes = append(row.Notes, notes...)
	if priceMissed(prices) {
		if row.State == Matched {
			row.State = PriceMiss
		}
		row.Notes = append(row.Notes, "a price the report implies missed the catalog past the report's last stated digit: this is a defect")
	}
	if row.laterInSpan() {
		return row, nil
	}
	days := []string{key.Day}
	if row.Span != nil {
		days = daysFrom(row.Span.From, row.Span.To)
	}
	dollars, notes, err := c.dollars(key, days, joined)
	if err != nil {
		return Row{}, err
	}
	row.Dollars = &dollars
	row.Notes = append(row.Notes, notes...)
	return row, nil
}

// spanKey is one span of one credential.
type spanKey struct {
	source, credential string
	span               Span
}

// spanStates gives every day of a span the span's state. A price that misses
// on any of its days makes each of them price_miss, and each day whose own
// prices all match names the first day that missed.
func spanStates(rows []Row) {
	missed := map[spanKey]string{}
	for _, row := range rows {
		if row.Span == nil || !priceMissed(row.Prices) {
			continue
		}
		at := spanKey{row.Source, row.Credential, *row.Span}
		if day, seen := missed[at]; !seen || row.Day < day {
			missed[at] = row.Day
		}
	}
	for i, row := range rows {
		if row.Span == nil {
			continue
		}
		day, found := missed[spanKey{row.Source, row.Credential, *row.Span}]
		if !found {
			continue
		}
		if row.State == Matched {
			rows[i].State = PriceMiss
		}
		if !priceMissed(row.Prices) {
			rows[i].Notes = append(rows[i].Notes, fmt.Sprintf("a price the report implies on %s, a day of this span, missed the catalog past the report's last stated digit: this is a defect", day))
		}
	}
}

func missed(checks []TokenCheck) bool {
	return slices.ContainsFunc(checks, func(check TokenCheck) bool { return !check.Within })
}

// joinSpan compares the days a run crossed midnight UTC over as one, when a
// day of them misses on its own. It returns the checks that judge the row's
// tokens: the joined ones when the days together match, the day's own
// otherwise. A day it cannot join, because another day of the run is outside
// the range or not yet reported, reads pending, because half a run is not a
// miss.
func (c comparison) joinSpan(key Key, row *Row) ([]TokenCheck, error) {
	from, to := c.ledger.span(key)
	days := daysFrom(from, to)
	if len(days) == 1 || !slices.ContainsFunc(days, c.missesAlone(key)) {
		return row.Tokens, nil
	}
	if !c.options.Range.holds(from) || !c.options.Range.holds(to) {
		row.State, row.Tokens = Pending, nil
		row.Notes = append(row.Notes, fmt.Sprintf("a run on this credential crossed midnight UTC: reconcile from %s to %s to compare those days as one", from, to))
		return nil, nil
	}
	if waiting := slices.IndexFunc(days, func(day string) bool { return !c.settled(day) }); waiting >= 0 {
		row.State, row.Tokens = Pending, nil
		row.Notes = append(row.Notes, fmt.Sprintf("a run on this credential crossed midnight UTC into %s, which the provider has not finished reporting", days[waiting]))
		return nil, nil
	}
	var usage []Usage
	for _, day := range days {
		usage = append(usage, c.report.usageOf(Key{key.Source, key.Credential, day})...)
	}
	joined := compareTokens(key.Source, c.ledger.joined(key.Source, key.Credential, days), usage)
	if missed(joined) {
		row.Notes = append(row.Notes, fmt.Sprintf("a run on this credential crossed midnight UTC, and the tokens of %s to %s miss together too", from, to))
		return row.Tokens, nil
	}
	row.Span = &Span{Cause: CauseRunSpansDays, From: from, To: to}
	row.Tokens = nil
	if key.Day == from {
		row.Tokens = joined
		row.Notes = append(row.Notes, fmt.Sprintf("a run crossed midnight UTC: the ledger dates its tokens by its start, and the provider by when each request was served, so %s to %s are compared as one, for tokens and dollars, and every class matched within 1%%", from, to))
	} else {
		row.Notes = append(row.Notes, fmt.Sprintf("a run crossed midnight UTC: this day's tokens and dollars are compared with %s's as one, on that day's row", from))
	}
	return joined, nil
}

// missesAlone reports whether a settled day in the range misses on its own.
func (c comparison) missesAlone(key Key) func(string) bool {
	return func(day string) bool {
		at := Key{key.Source, key.Credential, day}
		if !c.options.Range.holds(day) || !c.settled(day) {
			return false
		}
		return missed(compareTokens(key.Source, c.ledger.group(at), c.report.usageOf(at)))
	}
}

func daysFrom(from, to string) []string {
	var days []string
	for at := parseDay(from); at.Format(dayLayout) <= to; at = at.AddDate(0, 0, 1) {
		days = append(days, at.Format(dayLayout))
	}
	return days
}

// settled reports whether the day has ended, the report's lag has passed and
// the provider has not said the day is incomplete.
func (c comparison) settled(day string) bool {
	return !c.options.Now.Before(endOfDay(day).Add(c.report.Lag)) && !c.report.Unsettled[day]
}

func (c comparison) unmatched() []Unmatched {
	found := map[string]bool{}
	for _, usage := range c.report.Usage {
		found[usage.Credential] = true
	}
	for _, cost := range c.report.Costs {
		found[cost.Credential] = true
	}
	var unmatched []Unmatched
	for credential := range found {
		if !slices.Contains(c.named, credential) {
			unmatched = append(unmatched, Unmatched{c.report.Source, credential})
		}
	}
	slices.SortFunc(unmatched, func(a, b Unmatched) int { return cmp.Compare(a.Credential, b.Credential) })
	return unmatched
}

// isFinal reports whether the day's month has closed: it ended, and its rerun
// days have passed. October's last rerun is on November 8.
func isFinal(day string, now time.Time) bool {
	return !now.Before(endOfRerun(day))
}

func endOfRerun(day string) time.Time {
	at := parseDay(day)
	return time.Date(at.Year(), at.Month()+1, 1+RerunDays, 0, 0, 0, 0, time.UTC)
}

// within reports whether two figures differ by at most percent of the
// provider's, inclusive, by exact arithmetic. A provider figure of zero is
// matched only by zero.
func within(ledger, provider decimal.Decimal, percent int64) bool {
	gap := absolute(provider.Sub(ledger))
	return gap.Mul(decimal.FromInt(100)).Cmp(provider.Mul(decimal.FromInt(percent))) <= 0
}

func absolute(d decimal.Decimal) decimal.Decimal {
	if d.Sign() < 0 {
		return decimal.Decimal{}.Sub(d)
	}
	return d
}

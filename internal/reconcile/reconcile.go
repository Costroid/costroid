// Package reconcile compares the ledger with the provider's own reports, per
// credential per day, in two steps. Tokens must match by class within 1%, and
// a miss is a defect. List-price dollars must land within 10% of the cost
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

// State is a row's verdict on its tokens.
type State string

// The states of a Row.
const (
	// Matched means every class landed within tolerance.
	Matched State = "matched"
	// TokensMiss means a class missed the tolerance. It is a defect.
	TokensMiss State = "tokens_miss"
	// Pending means the provider has not finished reporting the day. A
	// pending row carries no figure, because a partial report is not a miss.
	Pending State = "pending"
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
type Row struct {
	Key
	State           State        `json:"state"`
	Final           bool         `json:"final"`
	DataRefreshedAt *time.Time   `json:"data_refreshed_at"`
	Tokens          []TokenCheck `json:"tokens"`
	Dollars         *DollarCheck `json:"dollars"`
	Notes           []string     `json:"notes"`
}

// TokenCheck compares one token class. Delta is Provider minus Ledger.
type TokenCheck struct {
	Class    focus.TokenClass `json:"class"`
	Ledger   decimal.Decimal  `json:"ledger"`
	Provider decimal.Decimal  `json:"provider"`
	Delta    decimal.Decimal  `json:"delta"`
	Within   bool             `json:"within"`
}

// DollarCheck compares list-price ledger cost with the cost report. Residual
// is Provider minus Ledger, and it equals the sum of Causes and Unattributed.
// OtherCost is what the report bills for anything but tokens, which the ledger
// does not hold and the comparison leaves out.
//
// ProviderEffective is what the report bills for tokens, after any discount,
// and equals Provider. ProviderList is the same charges at list price, null
// when the report does not state it on every token line. Rounding compares
// ProviderList with Ledger under FOCUS 1.4's rounding variance tolerance, and
// is null when ProviderList is.
type DollarCheck struct {
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

// Result is one reconciliation pass.
type Result struct {
	Rows      []Row
	Unmatched []Unmatched
}

// Misses counts the rows whose tokens are a defect.
func (r Result) Misses() int {
	misses := 0
	for _, row := range r.Rows {
		if row.State == TokensMiss {
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
		result.Rows = append(result.Rows, rows...)
		result.Unmatched = append(result.Unmatched, pass.unmatched()...)
	}
	slices.SortFunc(result.Rows, func(a, b Row) int { return compareKeys(a.Key, b.Key) })
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
	return rows, nil
}

func (c comparison) row(key Key) (Row, error) {
	row := Row{Key: key, Final: isFinal(key.Day, c.options.Now), DataRefreshedAt: c.report.refreshedAt(key.Day), Notes: []string{}}
	if !c.settled(key.Day) {
		row.State = Pending
		row.Notes = append(row.Notes, "the provider has not finished reporting this day")
		return row, nil
	}
	group := c.ledger.group(key)
	usage := c.report.usageOf(key)
	row.Tokens = compareTokens(key.Source, group, usage)
	row.State = Matched
	if slices.ContainsFunc(row.Tokens, func(check TokenCheck) bool { return !check.Within }) {
		row.State = TokensMiss
		row.Notes = append(row.Notes, "tokens missed the 1% tolerance: this is a defect")
	}
	if slices.ContainsFunc(row.Tokens, func(check TokenCheck) bool { return check.Class == cacheWrite }) {
		row.Notes = append(row.Notes, "cache writes compared as one figure, because the ledger cannot see their lifetime")
	}
	dollars, notes, err := c.dollars(key, group, usage, row.Tokens)
	if err != nil {
		return Row{}, err
	}
	row.Dollars = &dollars
	row.Notes = append(row.Notes, notes...)
	return row, nil
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

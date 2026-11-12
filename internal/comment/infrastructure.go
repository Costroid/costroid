package comment

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
)

// maxInfrastructureRows bounds the rows the folded table names, so a file of
// any size renders to a comment of a few kilobytes.
const maxInfrastructureRows = 20

// Infrastructure is what the rows that are not an agent's say: the figures,
// each the exact sum of ListCost over the rows that share a currency, a basis,
// a source and a charge period, and the first rows themselves. No figure spans
// two currencies or two periods, and none is added to an agent's figure.
type Infrastructure struct {
	Figures        []InfrastructureFigure
	FiguresOmitted int
	Rows           []InfrastructureRow
	RowsOmitted    int
}

// InfrastructureFigure is one currency, basis, source and period's sum.
type InfrastructureFigure struct {
	Currency string
	Basis    string
	Source   Data
	Period   Period
	Cost     decimal.Decimal
	Rows     int
}

// InfrastructureRow is one row: what the figure is for, the period it covers
// and its amount.
type InfrastructureRow struct {
	Resource Data
	Service  Data
	Currency string
	Period   Period
	Cost     decimal.Decimal
}

// Period is a row's charge period, as the row states it. The end is
// exclusive.
type Period struct {
	Start, End time.Time
}

// String names the period as briefly as it can be named exactly: a whole UTC
// month as 2026-12, whole UTC days by their first and last day, and any other
// span by its two instants.
func (p Period) String() string {
	const day = 24 * time.Hour
	switch {
	case p.Start.Day() == 1 && p.Start.Truncate(day).Equal(p.Start) && p.End.Equal(p.Start.AddDate(0, 1, 0)):
		return p.Start.Format("2006-01")
	case !p.Start.Truncate(day).Equal(p.Start) || !p.End.Truncate(day).Equal(p.End):
		return p.Start.Format(focus.InstantLayout) + " to " + p.End.Format(focus.InstantLayout)
	case p.End.Equal(p.Start.Add(day)):
		return p.Start.Format(time.DateOnly)
	}
	return p.Start.Format(time.DateOnly) + " to " + p.End.Add(-day).Format(time.DateOnly)
}

func (p Period) compare(other Period) int {
	return cmp.Or(p.Start.Compare(other.Start), p.End.Compare(other.End))
}

type infrastructureKey struct {
	currency, basis string
	source          Data
	period          Period
}

// infrastructureTally gathers the rows that are not an agent's.
type infrastructureTally struct {
	figures map[infrastructureKey]*InfrastructureFigure
	rows    []InfrastructureRow
	count   int
}

// add reads one row that is not an agent's. A row whose basis, amount or
// charge period does not read refuses the file, as an agent's row does.
func (t *infrastructureTally) add(parsed row) error {
	basis, err := parsed.closedTag(focus.TagCostBasis, costBases)
	if err != nil {
		return err
	}
	source, err := parsed.Tags.Text(focus.TagCostSource)
	if err != nil {
		return err
	}
	cost, err := parseAmount("ListCost", parsed.ListCost)
	if err != nil {
		return err
	}
	period, err := readPeriod(parsed.ChargePeriodStart, parsed.ChargePeriodEnd)
	if err != nil {
		return err
	}
	key := infrastructureKey{parsed.BillingCurrency, basis, Clean(source), period}
	figure, found := t.figures[key]
	if !found {
		figure = &InfrastructureFigure{Currency: key.currency, Basis: basis, Source: key.source, Period: period}
		t.figures[key] = figure
	}
	figure.Cost = figure.Cost.Add(cost)
	figure.Rows++
	t.count++
	if len(t.rows) < maxInfrastructureRows {
		t.rows = append(t.rows, InfrastructureRow{
			Resource: cleanWords(focusfile.Resource(parsed.ChargeDescription)),
			Service:  cleanWords(parsed.ServiceName),
			Currency: parsed.BillingCurrency,
			Period:   period,
			Cost:     cost,
		})
	}
	return nil
}

func readPeriod(start, end string) (Period, error) {
	from, err := time.Parse(focus.InstantLayout, start)
	if err != nil {
		return Period{}, fmt.Errorf("ChargePeriodStart %q is not a UTC instant", string(Clean(start)))
	}
	to, err := time.Parse(focus.InstantLayout, end)
	if err != nil {
		return Period{}, fmt.Errorf("ChargePeriodEnd %q is not a UTC instant", string(Clean(end)))
	}
	if !to.After(from) {
		return Period{}, fmt.Errorf("ChargePeriodEnd %s is not after ChargePeriodStart %s", end, start)
	}
	return Period{Start: from, End: to}, nil
}

// infrastructure orders the figures by currency, then period, basis and
// source, and keeps the first maxFigures of them.
func (t *infrastructureTally) infrastructure() Infrastructure {
	figures := make([]InfrastructureFigure, 0, len(t.figures))
	for _, figure := range t.figures {
		figures = append(figures, *figure)
	}
	slices.SortFunc(figures, func(a, b InfrastructureFigure) int {
		return cmp.Or(
			cmp.Compare(a.Currency, b.Currency),
			a.Period.compare(b.Period),
			cmp.Compare(a.Basis, b.Basis),
			cmp.Compare(a.Source, b.Source),
		)
	})
	shown := min(len(figures), maxFigures)
	return Infrastructure{
		Figures:        figures[:shown],
		FiguresOmitted: len(figures) - shown,
		Rows:           t.rows,
		RowsOmitted:    t.count - len(t.rows),
	}
}

// cleanWords is text from the pull request whose words, apart by single
// spaces, each pass the allowlist, as a service such as Amazon EC2 is named.
// A space cannot close a code span, open a tag or start a mention. Empty text
// stays empty.
func cleanWords(text string) Data {
	if text == "" {
		return ""
	}
	if len(text) > maxDataLength {
		return notShown
	}
	for _, word := range strings.Split(text, " ") {
		if Clean(word) == notShown {
			return notShown
		}
	}
	return Data(text)
}

// The words the comment names each basis by, inside a sentence.
var basisWords = map[string]string{
	"measured":        "measured",
	"list_equivalent": "list-equivalent, not billed",
	"estimate":        "estimated",
	"billed":          "billed",
	"derived":         "derived",
}

// infrastructureView is the section of the comment that states the figures
// that are not an agent's.
type infrastructureView struct {
	Lines        []string
	Apart        string
	ShowAmounts  bool
	Rows         []infrastructureRowView
	RowsOmitted  int
	LinesOmitted int
}

type infrastructureRowView struct {
	Resource, Service, Period, Amount string
}

// apart says, once, that the section's figures are not added to the agent's,
// nor one currency to another.
const (
	apartLine           = "The agent figure and these infrastructure figures are not added together."
	apartCurrenciesLine = "The agent figure and these infrastructure figures are not added together, and no currency is added to another."
	notStatedCell       = "not stated"
	// withheldLine says where withheld infrastructure amounts are kept, as the
	// withheld trace says it of token counts: in the rows, and on GitHub in
	// the check run's machine block. The comment's copy of the block states
	// each estimate without its amount.
	withheldLine          = "The rows carry their amounts."
	withheldLineCheckRuns = "The rows carry their amounts, and so does the check run's machine block."
)

// view lays out the section, nil when no row is outside an agent's. Withheld,
// it names each figure's basis, currency, period and source without its
// amount, and says where the amounts are: checkRuns is false on GitLab, which
// has no check run.
func (i Infrastructure) view(shown, checkRuns bool) *infrastructureView {
	if len(i.Figures) == 0 {
		return nil
	}
	section := &infrastructureView{ShowAmounts: shown, Apart: apartLine, RowsOmitted: i.RowsOmitted, LinesOmitted: i.FiguresOmitted}
	currencies := map[string]bool{}
	for _, figure := range i.Figures {
		currencies[figure.Currency] = true
		line := fmt.Sprintf("Infrastructure, %s, in %s for %s, from %s.", basisWords[figure.Basis], figure.Currency, figure.Period, figure.Source.Code())
		if shown {
			line = fmt.Sprintf("Infrastructure, %s: %s %s for %s, from %s.", basisWords[figure.Basis], figure.Currency, figure.Cost, figure.Period, figure.Source.Code())
		}
		section.Lines = append(section.Lines, line)
	}
	if len(currencies) > 1 || i.FiguresOmitted > 0 {
		section.Apart = apartCurrenciesLine
	}
	if !shown {
		withheld := withheldLine
		if checkRuns {
			withheld = withheldLineCheckRuns
		}
		section.Apart += " " + withheld
	}
	for _, row := range i.Rows {
		cells := infrastructureRowView{Resource: notStatedCell, Service: notStatedCell, Period: row.Period.String()}
		if row.Resource != "" {
			cells.Resource = row.Resource.Code()
		}
		if row.Service != "" {
			cells.Service = row.Service.Code()
		}
		if shown {
			cells.Amount = row.Currency + " " + row.Cost.String()
		}
		section.Rows = append(section.Rows, cells)
	}
	return section
}

// estimateUnavailable is the line that says an estimate the workflow named was
// not there, and why, from the note's closed list. A note outside it is
// dropped.
func (i Input) estimateUnavailable() string {
	if i.EstimateUnavailable == nil || i.EstimateUnavailable.Sentence() == "" {
		return ""
	}
	return "Infrastructure estimate: not available (" + i.EstimateUnavailable.Sentence() + ")."
}

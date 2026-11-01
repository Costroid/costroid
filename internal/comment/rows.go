package comment

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	maxRowBytes     = 1 << 20
	maxAmountLength = 64
	// maxFigures and maxNames bound the table and the block, so a rows file of
	// any size renders to a comment of a few kilobytes.
	maxFigures = 20
	maxNames   = 10
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

var (
	costBases = []string{string(focus.Measured), string(focus.ListEquivalent), "estimate", "billed", "derived"}
	grades    = []string{"exact", "windowed", "unattributed"}
)

// Figure is the exact sum of ListCost over rows that share a currency, a basis
// and both grades. No figure spans two currencies.
type Figure struct {
	Currency    string
	Basis       string
	Attribution string
	Enforcement string
	Cost        decimal.Decimal
	Rows        int
}

// Ledger is what a rows file says, reduced to the fields the comment shows.
type Ledger struct {
	Rows           int
	Figures        []Figure
	FiguresOmitted int
	Agents         []Data
	Models         []Data
	ModelsOmitted  int
	Catalogs       []Data
}

type row struct {
	BillingCurrency string                     `json:"BillingCurrency"`
	ListCost        string                     `json:"ListCost"`
	Tags            map[string]json.RawMessage `json:"Tags"`
	CatalogVersion  *string                    `json:"x_CostroidCatalogVersion"`
}

// tag reads a string tag. A user tag may hold another type, and the costroid/
// tags read here are strings by contract.
func (r row) tag(key string) (string, error) {
	var value string
	if err := json.Unmarshal(r.Tags[key], &value); err != nil {
		return "", fmt.Errorf("tag %s: %w", key, err)
	}
	return value, nil
}

type figureKey struct {
	currency, basis, attribution, enforcement string
}

type tally struct {
	rows     int
	figures  map[figureKey]*Figure
	agents   map[Data]struct{}
	models   map[Data]struct{}
	catalogs map[Data]struct{}
}

// ReadRows reads FOCUS rows as JSON lines. One bad row refuses the file: a
// partial total would read as a smaller cost.
func ReadRows(source io.Reader) (Ledger, error) {
	total := tally{
		figures:  map[figureKey]*Figure{},
		agents:   map[Data]struct{}{},
		models:   map[Data]struct{}{},
		catalogs: map[Data]struct{}{},
	}
	scanner := bufio.NewScanner(source)
	scanner.Buffer(nil, maxRowBytes)
	for line := 1; scanner.Scan(); line++ {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		if err := total.add(scanner.Bytes()); err != nil {
			return Ledger{}, fmt.Errorf("comment: rows line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return Ledger{}, fmt.Errorf("comment: read rows: %w", err)
	}
	return total.ledger(), nil
}

func (t *tally) add(line []byte) error {
	var parsed row
	if err := json.Unmarshal(line, &parsed); err != nil {
		return err
	}
	if !currencyPattern.MatchString(parsed.BillingCurrency) {
		return fmt.Errorf("BillingCurrency %q is not an ISO code", string(Clean(parsed.BillingCurrency)))
	}
	cost, err := parseAmount(parsed.ListCost)
	if err != nil {
		return err
	}
	key, err := parsed.key()
	if err != nil {
		return err
	}
	figure, found := t.figures[key]
	if !found {
		figure = &Figure{Currency: key.currency, Basis: key.basis, Attribution: key.attribution, Enforcement: key.enforcement}
		t.figures[key] = figure
	}
	figure.Cost = figure.Cost.Add(cost)
	figure.Rows++
	t.rows++
	t.note(parsed)
	return nil
}

func parseAmount(text string) (decimal.Decimal, error) {
	if len(text) > maxAmountLength {
		return decimal.Decimal{}, fmt.Errorf("ListCost is past %d characters", maxAmountLength)
	}
	amount, err := decimal.Parse(text)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("ListCost: %w", err)
	}
	if amount.Scale() > focus.MaxScale {
		return decimal.Decimal{}, fmt.Errorf("ListCost has more than %d fractional digits", focus.MaxScale)
	}
	return amount, nil
}

// artifactAttribution is the only grade a comment states.
const artifactAttribution = "unattributed"

func (r row) key() (figureKey, error) {
	basis, err := r.closedTag(focus.TagCostBasis, costBases)
	if err != nil {
		return figureKey{}, err
	}
	if _, err := r.closedTag(focus.TagAttributionGrade, grades); err != nil {
		return figureKey{}, err
	}
	enforcement, err := r.closedTag(focus.TagEnforcementGrade, enforcementOrder)
	if err != nil {
		return figureKey{}, err
	}
	// The agent's job can write this file, and only the service can prove a
	// grade, so a grade read here is never shown as one.
	return figureKey{r.BillingCurrency, basis, artifactAttribution, enforcement}, nil
}

func (r row) closedTag(key string, vocabulary []string) (string, error) {
	value, err := r.tag(key)
	if err != nil {
		return "", err
	}
	if !slices.Contains(vocabulary, value) {
		return "", fmt.Errorf("tag %s reads %q, outside its closed vocabulary", key, string(Clean(value)))
	}
	return value, nil
}

func (t *tally) note(parsed row) {
	if agent, err := parsed.tag(focus.TagAgent); err == nil {
		t.agents[Clean(agent)] = struct{}{}
	}
	if model, err := parsed.tag(focus.TagModel); err == nil {
		t.models[Clean(model)] = struct{}{}
	}
	if parsed.CatalogVersion != nil {
		t.catalogs[Clean(*parsed.CatalogVersion)] = struct{}{}
	}
}

func (t *tally) ledger() Ledger {
	figures := make([]Figure, 0, len(t.figures))
	for _, figure := range t.figures {
		figures = append(figures, *figure)
	}
	slices.SortFunc(figures, func(a, b Figure) int {
		return cmp.Or(
			cmp.Compare(a.Currency, b.Currency),
			cmp.Compare(a.Basis, b.Basis),
			cmp.Compare(a.Attribution, b.Attribution),
			cmp.Compare(slices.Index(enforcementOrder, a.Enforcement), slices.Index(enforcementOrder, b.Enforcement)),
		)
	})
	shownFigures := min(len(figures), maxFigures)
	models, modelsOmitted := capped(t.models)
	agents, _ := capped(t.agents)
	catalogs, _ := capped(t.catalogs)
	return Ledger{
		Rows:           t.rows,
		Figures:        figures[:shownFigures],
		FiguresOmitted: len(figures) - shownFigures,
		Agents:         agents,
		Models:         models,
		ModelsOmitted:  modelsOmitted,
		Catalogs:       catalogs,
	}
}

func capped(names map[Data]struct{}) ([]Data, int) {
	sorted := make([]Data, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	shown := min(len(sorted), maxNames)
	return sorted[:shown], len(sorted) - shown
}

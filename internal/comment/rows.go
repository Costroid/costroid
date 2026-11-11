package comment

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	maxAmountLength = 64
	// maxFigures and maxNames bound the table and the block, so a rows file of
	// any size renders to a comment of a few kilobytes.
	maxFigures = 20
	maxNames   = 10
	// maxTraceLines bounds one figure's trace and maxTraceTotal every trace
	// together, so the traces stay well inside the comment's limit.
	maxTraceLines = 8
	maxTraceTotal = 40
	// informationalUnit is the PricingUnit of a class that repeats tokens a
	// billed class counts.
	informationalUnit = "Informational Tokens"
	// maxRequests bounds the requests kept per currency and basis for the
	// overshoot. Past it the overshoot in tokens is not stated.
	maxRequests = 100000
)

var (
	currencyPattern    = regexp.MustCompile(`^[A-Z]{3}$`)
	pricingUnitPattern = regexp.MustCompile(`^([1-9][0-9]{0,11}) Tokens$`)
)

var (
	costBases    = []string{string(focus.Measured), string(focus.ListEquivalent), "estimate", "billed", "derived"}
	grades       = []string{"exact", "windowed", "unattributed"}
	tokenClasses = []focus.TokenClass{
		focus.InputUncached, focus.CacheRead, focus.CacheWrite5m, focus.CacheWrite30m,
		focus.CacheWrite1h, focus.CacheWrite12h, focus.Output, focus.ReasoningOutput, focus.CachedInputReported,
	}
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
	// FallbackRows counts the rows the catalog priced at its fallback tier.
	FallbackRows int
	// Trace is the arithmetic behind Cost, one line per catalog, class and
	// price. TraceOmitted counts the lines left out of it.
	Trace        []Trace
	TraceOmitted int
	// Tokens is every token count of the figure by class, with no cap.
	Tokens map[focus.TokenClass]decimal.Decimal
	// Catalogs are the catalog versions of the figure's rows.
	Catalogs []Data
	// ProxyCap is the AI-credit cap gh-aw's proxy held the run to, as every
	// row of the figure states it, empty when one states none or another.
	ProxyCap string
	// Subscription is true when every row of the figure notes a run that
	// proved a subscription login.
	Subscription bool
}

// Trace is the rows of one Figure that share a catalog, a token class and a
// price. Cost is UnitPrice times Tokens over the size of Unit, exactly.
type Trace struct {
	Catalog   Data
	Class     focus.TokenClass
	PriceID   Data
	UnitPrice decimal.Decimal
	// Unit is a count of tokens, or informationalUnit for a class that carries
	// no price. Both forms are checked, so Unit is never free text.
	Unit   string
	Tokens decimal.Decimal
	Cost   decimal.Decimal
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
	// FallbackModels are the models of the rows priced at the fallback tier,
	// FallbackPrices the price ids that priced them and FallbackCatalogs the
	// catalogs those rows name.
	FallbackModels        []Data
	FallbackModelsOmitted int
	FallbackPrices        []Data
	FallbackPricesOmitted int
	FallbackCatalogs      []Data
	// requests holds each currency and basis's requests, for the overshoot.
	requests map[spendKey]*requestLog
}

// RowError names the first line of a rows file that is not a valid row.
type RowError struct {
	Line int
	Err  error
}

func (e *RowError) Error() string {
	return fmt.Sprintf("comment: rows line %d: %v", e.Line, e.Err)
}

func (e *RowError) Unwrap() error {
	return e.Err
}

type row struct {
	BillingCurrency   string     `json:"BillingCurrency"`
	ChargePeriodStart string     `json:"ChargePeriodStart"`
	ChargePeriodEnd   string     `json:"ChargePeriodEnd"`
	ListCost          string     `json:"ListCost"`
	ListUnitPrice     string     `json:"ListUnitPrice"`
	PricingQuantity   string     `json:"PricingQuantity"`
	PricingUnit       string     `json:"PricingUnit"`
	ConsumedQuantity  string     `json:"ConsumedQuantity"`
	SkuMeter          string     `json:"SkuMeter"`
	SkuPriceID        string     `json:"SkuPriceId"`
	ChargeDescription string     `json:"ChargeDescription"`
	Tags              focus.Tags `json:"Tags"`
	CatalogVersion    *string    `json:"x_CostroidCatalogVersion"`
}

// proxyCap is the cap a gh-aw row's description states its proxy held the run
// to, empty for any other row. The note is matched, and only its digits are
// kept.
func (r row) proxyCap() string {
	if agent, err := r.Tags.Text(focus.TagAgent); err != nil || agent != ghaw.Name {
		return ""
	}
	limit, _ := ghaw.ProxyCap(r.ChargeDescription)
	return limit
}

// fallback reports whether the writer noted the fallback tier. The note is
// matched, never shown.
func (r row) fallback() bool {
	return strings.Contains(r.ChargeDescription, focus.NoteFallback)
}

// subscription reports whether the writer noted a run that proved a
// subscription login. The note must be one whole note of the description.
func (r row) subscription() bool {
	return slices.Contains(strings.Split(r.ChargeDescription, "; "), focus.NoteSubscription)
}

func (r row) catalog() Data {
	if r.CatalogVersion == nil {
		return ""
	}
	return Clean(*r.CatalogVersion)
}

type figureKey struct {
	currency, basis, attribution, enforcement string
}

type traceKey struct {
	catalog, priceID Data
	class            focus.TokenClass
	price, unit      string
}

type figureTally struct {
	figure   Figure
	traces   map[traceKey]*Trace
	catalogs map[Data]struct{}
	// proxyCaps are the gh-aw caps the rows state, an empty one for a row
	// that states none.
	proxyCaps map[string]struct{}
	// subscriptionRows counts the rows that note a subscription login.
	subscriptionRows int
}

// spendKey is what a budget is compared within: one currency, one basis.
type spendKey struct {
	currency, basis string
}

// requestSpend is one request's cost and its billed tokens. A class that
// repeats another's tokens is left out of the count.
type requestSpend struct {
	end    string
	cost   decimal.Decimal
	tokens decimal.Decimal
}

// requestKey is what the rows of one request share. A row writer emits each
// class of a request once, so a class seen again opens the next request.
type requestKey struct {
	start, end, session, model string
}

// requestLog keeps one currency and basis's requests in the order the rows
// name them.
type requestLog struct {
	requests []requestSpend
	current  requestKey
	classes  map[focus.TokenClass]bool
	// overflow says the log passed maxRequests and was dropped.
	overflow bool
}

type tally struct {
	rows             int
	figures          map[figureKey]*figureTally
	requests         map[spendKey]*requestLog
	agents           map[Data]struct{}
	models           map[Data]struct{}
	catalogs         map[Data]struct{}
	fallbackModels   map[Data]struct{}
	fallbackPrices   map[Data]struct{}
	fallbackCatalogs map[Data]struct{}
}

// ReadRows reads FOCUS rows as JSON lines. One bad row refuses the file: a
// partial total would read as a smaller cost. A line at fault comes back as a
// RowError.
func ReadRows(source io.Reader) (Ledger, error) {
	total := tally{
		figures:          map[figureKey]*figureTally{},
		requests:         map[spendKey]*requestLog{},
		agents:           map[Data]struct{}{},
		models:           map[Data]struct{}{},
		catalogs:         map[Data]struct{}{},
		fallbackModels:   map[Data]struct{}{},
		fallbackPrices:   map[Data]struct{}{},
		fallbackCatalogs: map[Data]struct{}{},
	}
	err := focus.EachLine(source, total.add)
	var fault *focus.LineError
	if errors.As(err, &fault) {
		return Ledger{}, &RowError{Line: fault.Line, Err: fault.Err}
	}
	if err != nil {
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
	priced, err := parsed.arithmetic()
	if err != nil {
		return err
	}
	key, err := parsed.key()
	if err != nil {
		return err
	}
	entry, found := t.figures[key]
	if !found {
		entry = &figureTally{
			figure: Figure{
				Currency: key.currency, Basis: key.basis, Attribution: key.attribution, Enforcement: key.enforcement,
				Tokens: map[focus.TokenClass]decimal.Decimal{},
			},
			traces:    map[traceKey]*Trace{},
			catalogs:  map[Data]struct{}{},
			proxyCaps: map[string]struct{}{},
		}
		t.figures[key] = entry
	}
	entry.figure.Cost = entry.figure.Cost.Add(priced.Cost)
	entry.figure.Rows++
	entry.add(priced)
	entry.proxyCaps[parsed.proxyCap()] = struct{}{}
	if parsed.subscription() {
		entry.subscriptionRows++
	}
	t.logRequest(spendKey{key.currency, key.basis}, parsed, priced)
	if parsed.fallback() {
		entry.figure.FallbackRows++
		t.noteFallback(parsed)
	}
	t.rows++
	t.note(parsed)
	return nil
}

func (f *figureTally) add(priced Trace) {
	key := traceKey{priced.Catalog, priced.PriceID, priced.Class, priced.UnitPrice.String(), priced.Unit}
	line, found := f.traces[key]
	if !found {
		line = &Trace{Catalog: priced.Catalog, Class: priced.Class, PriceID: priced.PriceID, UnitPrice: priced.UnitPrice, Unit: priced.Unit}
		f.traces[key] = line
	}
	line.Tokens = line.Tokens.Add(priced.Tokens)
	line.Cost = line.Cost.Add(priced.Cost)
	f.figure.Tokens[priced.Class] = f.figure.Tokens[priced.Class].Add(priced.Tokens)
	if priced.Catalog != "" {
		f.catalogs[priced.Catalog] = struct{}{}
	}
}

// logRequest adds a row to its request, and opens the next request when the
// row belongs to another one.
func (t *tally) logRequest(key spendKey, parsed row, priced Trace) {
	log, found := t.requests[key]
	if !found {
		log = &requestLog{}
		t.requests[key] = log
	}
	if log.overflow {
		return
	}
	session, _ := parsed.Tags.Text(focus.TagSessionID)
	model, _ := parsed.Tags.Text(focus.TagModel)
	request := requestKey{parsed.ChargePeriodStart, parsed.ChargePeriodEnd, session, model}
	if len(log.requests) == 0 || request != log.current || log.classes[priced.Class] {
		if len(log.requests) == maxRequests {
			log.requests, log.overflow = nil, true
			return
		}
		log.requests = append(log.requests, requestSpend{end: parsed.ChargePeriodEnd})
		log.current, log.classes = request, map[focus.TokenClass]bool{}
	}
	log.classes[priced.Class] = true
	last := &log.requests[len(log.requests)-1]
	last.cost = last.cost.Add(priced.Cost)
	if !priced.Class.Informational() {
		last.tokens = last.tokens.Add(priced.Tokens)
	}
}

// overshootTokens counts the billed tokens of the request that took one
// currency and basis's spend past the cap, and of every request that ended
// after it. Requests are taken in the order they ended. The answer is false
// when the rows hold too many requests to keep.
func (l Ledger) overshootTokens(currency, basis string, cap decimal.Decimal) (decimal.Decimal, bool) {
	log, found := l.requests[spendKey{currency, basis}]
	if !found || log.overflow {
		return decimal.Decimal{}, false
	}
	requests := slices.Clone(log.requests)
	slices.SortStableFunc(requests, func(a, b requestSpend) int { return cmp.Compare(a.end, b.end) })
	var spent, tokens decimal.Decimal
	over := false
	for _, request := range requests {
		spent = spent.Add(request.cost)
		over = over || spent.Cmp(cap) > 0
		if over {
			tokens = tokens.Add(request.tokens)
		}
	}
	return tokens, true
}

// arithmetic reads the columns a trace shows. A row whose own arithmetic does
// not hold is refused, because its trace would not recompute to its cost.
func (r row) arithmetic() (Trace, error) {
	cost, err := parseAmount("ListCost", r.ListCost)
	if err != nil {
		return Trace{}, err
	}
	price, err := parseAmount("ListUnitPrice", r.ListUnitPrice)
	if err != nil {
		return Trace{}, err
	}
	quantity, err := parseAmount("PricingQuantity", r.PricingQuantity)
	if err != nil {
		return Trace{}, err
	}
	tokens, err := parseAmount("ConsumedQuantity", r.ConsumedQuantity)
	if err != nil {
		return Trace{}, err
	}
	class := focus.TokenClass(r.SkuMeter)
	if !slices.Contains(tokenClasses, class) {
		return Trace{}, fmt.Errorf("SkuMeter %q is not a token class", string(Clean(r.SkuMeter)))
	}
	size, err := unitSize(class, r.PricingUnit)
	if err != nil {
		return Trace{}, err
	}
	if tokens.Cmp(quantity.Mul(size)) != 0 {
		return Trace{}, fmt.Errorf("ConsumedQuantity %s is not PricingQuantity %s times %s", tokens, quantity, size)
	}
	if cost.Cmp(price.Mul(quantity)) != 0 {
		return Trace{}, fmt.Errorf("ListCost %s is not ListUnitPrice %s times PricingQuantity %s", cost, price, quantity)
	}
	if class.Informational() && cost.Sign() != 0 {
		return Trace{}, fmt.Errorf("the informational class %s carries ListCost %s", class, cost)
	}
	return Trace{Catalog: r.catalog(), Class: class, PriceID: Clean(r.SkuPriceID), UnitPrice: price, Unit: r.PricingUnit, Tokens: tokens, Cost: cost}, nil
}

// unitSize is the number of tokens one PricingUnit holds.
func unitSize(class focus.TokenClass, unit string) (decimal.Decimal, error) {
	if class.Informational() {
		if unit != informationalUnit {
			return decimal.Decimal{}, fmt.Errorf("PricingUnit of the informational class %s is not %q", class, informationalUnit)
		}
		return decimal.FromInt(1), nil
	}
	match := pricingUnitPattern.FindStringSubmatch(unit)
	if match == nil {
		return decimal.Decimal{}, fmt.Errorf("PricingUnit %q is not a count of tokens", string(Clean(unit)))
	}
	return decimal.Parse(match[1])
}

func parseAmount(column, text string) (decimal.Decimal, error) {
	if len(text) > maxAmountLength {
		return decimal.Decimal{}, fmt.Errorf("%s is past %d characters", column, maxAmountLength)
	}
	amount, err := decimal.Parse(text)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%s: %w", column, err)
	}
	if amount.Scale() > focus.MaxScale {
		return decimal.Decimal{}, fmt.Errorf("%s has more than %d fractional digits", column, focus.MaxScale)
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
	value, err := r.Tags.Text(key)
	if err != nil {
		return "", err
	}
	if !slices.Contains(vocabulary, value) {
		return "", fmt.Errorf("tag %s reads %q, outside its closed vocabulary", key, string(Clean(value)))
	}
	return value, nil
}

func (t *tally) note(parsed row) {
	if agent, err := parsed.Tags.Text(focus.TagAgent); err == nil {
		t.agents[Clean(agent)] = struct{}{}
	}
	if model, err := parsed.Tags.Text(focus.TagModel); err == nil {
		t.models[Clean(model)] = struct{}{}
	}
	if parsed.CatalogVersion != nil {
		t.catalogs[Clean(*parsed.CatalogVersion)] = struct{}{}
	}
}

// noteFallback keeps what the fallback sentence names. A row without a model
// tag still counts, as a model not shown.
func (t *tally) noteFallback(parsed row) {
	model, err := parsed.Tags.Text(focus.TagModel)
	if err != nil {
		model = string(notShown)
	}
	t.fallbackModels[Clean(model)] = struct{}{}
	t.fallbackPrices[Clean(parsed.SkuPriceID)] = struct{}{}
	if catalog := parsed.catalog(); catalog != "" {
		t.fallbackCatalogs[catalog] = struct{}{}
	}
}

func (t *tally) ledger() Ledger {
	figures := make([]Figure, 0, len(t.figures))
	for _, entry := range t.figures {
		figures = append(figures, entry.withTrace())
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
	figures = figures[:shownFigures]
	capTraces(figures)
	models, modelsOmitted := capped(t.models)
	agents, _ := capped(t.agents)
	catalogs, _ := capped(t.catalogs)
	fallbackModels, fallbackModelsOmitted := capped(t.fallbackModels)
	fallbackPrices, fallbackPricesOmitted := capped(t.fallbackPrices)
	fallbackCatalogs, _ := capped(t.fallbackCatalogs)
	return Ledger{
		Rows:                  t.rows,
		Figures:               figures,
		FiguresOmitted:        len(t.figures) - shownFigures,
		Agents:                agents,
		Models:                models,
		ModelsOmitted:         modelsOmitted,
		Catalogs:              catalogs,
		FallbackModels:        fallbackModels,
		FallbackModelsOmitted: fallbackModelsOmitted,
		FallbackPrices:        fallbackPrices,
		FallbackPricesOmitted: fallbackPricesOmitted,
		FallbackCatalogs:      fallbackCatalogs,
		requests:              t.requests,
	}
}

// withTrace orders the trace by catalog, then class in the contract's order,
// then price id and price.
func (f *figureTally) withTrace() Figure {
	figure := f.figure
	figure.Catalogs, _ = capped(f.catalogs)
	figure.Subscription = f.subscriptionRows == figure.Rows
	if len(f.proxyCaps) == 1 {
		for limit := range f.proxyCaps {
			figure.ProxyCap = limit
		}
	}
	figure.Trace = make([]Trace, 0, len(f.traces))
	for _, line := range f.traces {
		figure.Trace = append(figure.Trace, *line)
	}
	slices.SortFunc(figure.Trace, func(a, b Trace) int {
		return cmp.Or(
			cmp.Compare(a.Catalog, b.Catalog),
			cmp.Compare(slices.Index(tokenClasses, a.Class), slices.Index(tokenClasses, b.Class)),
			cmp.Compare(a.PriceID, b.PriceID),
			a.UnitPrice.Cmp(b.UnitPrice),
		)
	})
	return figure
}

// capTraces keeps each figure's first lines, inside the per-figure and the
// shared cap, and counts the lines it leaves out.
func capTraces(figures []Figure) {
	left := maxTraceTotal
	for index := range figures {
		figure := &figures[index]
		shown := min(len(figure.Trace), maxTraceLines, left)
		figure.TraceOmitted = len(figure.Trace) - shown
		figure.Trace = figure.Trace[:shown]
		left -= shown
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

package reconcile

import (
	"cmp"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	// ClassDollarTolerancePercent bounds each class's list-price dollars
	// against the cost report's, as a percentage of the report's figure.
	ClassDollarTolerancePercent = 1

	// impliedPlaces is the decimal places an implied unit price is rounded
	// to, in USD per million tokens. No verdict reads it.
	impliedPlaces = 6

	// maxContextTiers bounds the walk through one class's context tiers.
	maxContextTiers = 16
)

// PriceCheck proves one price. ProviderList is the cost report's figure for
// one model's token class on the day, and Tokens is the usage report's count
// for the same model and class. ImpliedUnitPrice is the first over the second,
// per million tokens, and null when Tokens is zero. CatalogUnitPrice is the
// catalog's ListUnitPrice for the row SkuPriceID names. Match holds when
// Tokens at CatalogUnitPrice lands within one unit of the last digit the
// report states.
type PriceCheck struct {
	Model            string           `json:"model"`
	Tier             string           `json:"tier"`
	Region           string           `json:"region"`
	Class            focus.TokenClass `json:"class"`
	Tokens           decimal.Decimal  `json:"tokens"`
	ProviderList     decimal.Decimal  `json:"provider_list"`
	ImpliedUnitPrice *decimal.Decimal `json:"implied_unit_price"`
	CatalogUnitPrice decimal.Decimal  `json:"catalog_unit_price"`
	SkuPriceID       string           `json:"sku_price_id"`
	Match            bool             `json:"match"`
}

// ClassDollars compares one token class at list price. Delta is Provider
// minus Ledger.
type ClassDollars struct {
	Class    focus.TokenClass `json:"class"`
	Ledger   decimal.Decimal  `json:"ledger"`
	Provider decimal.Decimal  `json:"provider"`
	Delta    decimal.Decimal  `json:"delta"`
	Within   bool             `json:"within"`
}

// Stated is a figure as a report wrote it, in USD. Unit is one unit of the
// last digit the report stated.
type Stated struct {
	Value decimal.Decimal
	Unit  decimal.Decimal
}

// stated reads a figure and the last digit it states. shift is how many
// places the report's unit sits below the dollar: 2 for cents.
func stated(text string, shift int) (Stated, error) {
	value, err := parseAmount(text)
	if err != nil {
		return Stated{}, err
	}
	digits, _ := strings.CutPrefix(text, "-")
	mantissa, exponent, scientific := strings.Cut(strings.ToLower(digits), "e")
	places := shift
	if _, fraction, found := strings.Cut(mantissa, "."); found {
		places += len(fraction)
	}
	if scientific {
		power, err := strconv.Atoi(exponent)
		if err != nil {
			return Stated{}, fmt.Errorf("the exponent of %q: %w", text, err)
		}
		places -= power
	}
	return Stated{Value: value.DivPow10(shift), Unit: unitAt(places)}, nil
}

// unitAt is one unit of a decimal place: 0.01 at 2, 100 at -2.
func unitAt(places int) decimal.Decimal {
	if places >= 0 {
		return decimal.FromInt(1).DivPow10(places)
	}
	return decimal.MustParse("1" + strings.Repeat("0", -places))
}

// priceKey places one model's token class. A report that does not state the
// tier on its cost lines leaves tier and region empty until usage names them.
type priceKey struct {
	model, tier, region string
	class               focus.TokenClass
}

// lineFigure is the report's list figure for one priceKey: the sum of its
// lines, and the sum of their last digits, which bounds how far the sum can
// sit from the exact amount. unstated marks a line with no list figure.
type lineFigure struct {
	list, unit decimal.Decimal
	unstated   bool
}

// prices proves each price the day's cost report implies. The proof reads
// the provider's two reports and the catalog. The ledger only names which
// context tier a price is held to, where the catalog prices more than one.
func (c comparison) prices(key Key, group *ledgerGroup, usage []Usage) ([]PriceCheck, []string, error) {
	figures, tiered, unread, notes := splitLines(c.report.costLines(key))
	if figures == nil {
		return []PriceCheck{}, notes, nil
	}
	for _, record := range usage {
		for class, tokens := range record.Tokens {
			line := priceKey{model: record.Model, class: class}
			if tiered {
				line.tier, line.region = record.Tier, record.Region
			}
			switch {
			case tokens.Sign() == 0 || figures[line] != nil:
			case unread[record.Model]:
				notes = appendNote(notes, fmt.Sprintf("%s %s has no line of its own, and the model has a line Costroid does not read, so its price is not proven", record.Model, class))
			default:
				figures[line] = &lineFigure{}
			}
		}
	}
	walk := walker{catalog: c.options.Catalog, source: c.report.Source, at: parseDay(key.Day)}
	checks := []PriceCheck{}
	for _, line := range sortedLines(figures) {
		check, note, err := walk.prove(line, figures[line], group, usage)
		if err != nil {
			return nil, nil, err
		}
		if check != nil {
			checks = append(checks, *check)
		}
		notes = appendNote(notes, note)
	}
	return checks, notes, nil
}

// splitLines totals the token lines by model and class. It returns no figures
// when the report does not split token cost by class. tiered says whether
// the lines state their tier and region. unread names the models with a line
// whose token type or line item names no class Costroid reads.
func splitLines(lines []Cost) (map[priceKey]*lineFigure, bool, map[string]bool, []string) {
	var figures map[priceKey]*lineFigure
	var notes []string
	unread := map[string]bool{}
	tiered, whole := true, false
	for _, line := range lines {
		switch {
		case !line.Tokens:
			continue
		case line.Label == "":
			whole = true
			continue
		case line.Class == "" || line.Model == "":
			unread[line.Model] = true
			notes = appendNote(notes, fmt.Sprintf("the report's token line %q names no model and class Costroid reads, so its price is not proven", line.Label))
			continue
		}
		if figures == nil {
			figures = map[priceKey]*lineFigure{}
		}
		tiered = tiered && line.Tier != ""
		key := priceKey{line.Model, line.Tier, line.Region, line.Class}
		figure := figures[key]
		if figure == nil {
			figure = &lineFigure{}
			figures[key] = figure
		}
		if line.ListFigure == nil {
			figure.unstated = true
			continue
		}
		figure.list = figure.list.Add(line.ListFigure.Value)
		figure.unit = figure.unit.Add(line.ListFigure.Unit)
	}
	if figures == nil && whole {
		notes = appendNote(notes, "the cost report does not split token cost by class, so no price is proven")
	}
	return figures, tiered, unread, notes
}

func sortedLines(figures map[priceKey]*lineFigure) []priceKey {
	lines := make([]priceKey, 0, len(figures))
	for line := range figures {
		lines = append(lines, line)
	}
	slices.SortFunc(lines, func(a, b priceKey) int {
		return cmp.Or(cmp.Compare(a.model, b.model), cmp.Compare(a.tier, b.tier), cmp.Compare(a.region, b.region),
			cmp.Compare(slices.Index(compared, a.class), slices.Index(compared, b.class)))
	})
	return lines
}

// prove holds one line to the catalog. It returns no check, and a note that
// says why, for a line no single list price can be proven on.
func (w walker) prove(line priceKey, figure *lineFigure, group *ledgerGroup, usage []Usage) (*PriceCheck, string, error) {
	tokens, served := servedTokens(usage, line)
	name := line.model + " " + string(line.class)
	if line.tier == "" {
		switch len(served) {
		case 0:
			line.tier, line.region = catalog.TierStandard, catalog.RegionGlobal
		case 1:
			line.tier, line.region = served[0].tier, served[0].region
		default:
			return nil, fmt.Sprintf("%s was served at %d tiers the cost report does not split, so its price is not proven", name, len(served)), nil
		}
	}
	switch {
	case w.source == SourceAnthropic && line.tier == tierPriority:
		return nil, fmt.Sprintf("%s at the priority tier carries no token cost on the report, because the commitment is billed elsewhere, so its price is not proven", name), nil
	case figure.unstated:
		return nil, fmt.Sprintf("the report states no list figure for %s, so its price is not proven", name), nil
	case tokens.Sign() == 0 && figure.list.Sign() == 0:
		return nil, "", nil
	}
	tiers, err := w.contextTiers(line)
	if err != nil {
		return nil, "", err
	}
	price, held := heldTo(tiers, group.skus[line])
	if !held {
		return nil, fmt.Sprintf("the catalog prices %s at %d context tiers, and the report does not say which the tokens fell in, so its price is not proven", name, len(tiers)), nil
	}
	implied, err := impliedPrice(figure.list, tokens)
	if err != nil {
		return nil, "", err
	}
	check := &PriceCheck{
		Model:            line.model,
		Tier:             line.tier,
		Region:           line.region,
		Class:            line.class,
		Tokens:           tokens,
		ProviderList:     figure.list,
		ImpliedUnitPrice: implied,
		CatalogUnitPrice: price.ListUnitPrice,
		SkuPriceID:       price.SkuPriceID,
		Match:            proves(figure, tokens, price),
	}
	return check, why(check, price, tiers, figure), nil
}

// proves reports whether tokens at the price land within the report's last
// stated digit of its figure.
func proves(figure *lineFigure, tokens decimal.Decimal, price catalog.Price) bool {
	expected := tokens.Mul(price.ListUnitPrice).DivPow10(tokensPerMillion)
	return absolute(figure.list.Sub(expected)).Cmp(figure.unit) < 0
}

// why names what a reader needs beside a check: a fallback price, or another
// context tier the report's figure matches.
func why(check *PriceCheck, price catalog.Price, tiers []catalog.Price, figure *lineFigure) string {
	if price.Fallback {
		return fmt.Sprintf("the catalog has no price for %s %s at the %s tier, so it is held to the fallback %s", check.Model, check.Class, check.Tier, price.SkuPriceID)
	}
	if check.Match {
		return ""
	}
	for _, other := range tiers {
		if other.SkuPriceID != price.SkuPriceID && proves(figure, check.Tokens, other) {
			return fmt.Sprintf("the report prices %s %s at the catalog's %s, not %s, which the ledger priced it at", check.Model, check.Class, other.SkuPriceID, price.SkuPriceID)
		}
	}
	return ""
}

type servedAt struct {
	tier, region string
}

// servedTokens counts the usage report's tokens for a line, and lists the
// tiers and regions they were served at.
func servedTokens(usage []Usage, line priceKey) (decimal.Decimal, []servedAt) {
	var tokens decimal.Decimal
	var served []servedAt
	for _, record := range usage {
		count := record.Tokens[line.class]
		if record.Model != line.model || count.Sign() == 0 {
			continue
		}
		if line.tier != "" && (record.Tier != line.tier || record.Region != line.region) {
			continue
		}
		tokens = tokens.Add(count)
		if at := (servedAt{record.Tier, record.Region}); !slices.Contains(served, at) {
			served = append(served, at)
		}
	}
	return tokens, served
}

// contextTiers lists the catalog's prices for a line, one per context tier,
// lowest first. A model the sheet does not name has the fallback alone.
func (w walker) contextTiers(line priceKey) ([]catalog.Price, error) {
	var tiers []catalog.Price
	var context int64
	for range maxContextTiers {
		price, err := w.catalog.Lookup(catalog.Query{
			Provider:           w.source,
			Model:              line.model,
			Class:              line.class,
			ServiceTier:        line.tier,
			Region:             line.region,
			ContextInputTokens: context,
			At:                 w.at,
		})
		if err != nil {
			return nil, err
		}
		tiers = append(tiers, price)
		if price.Fallback || price.ContextTierMax == nil {
			return tiers, nil
		}
		context = *price.ContextTierMax + 1
	}
	return nil, fmt.Errorf("catalog: %s %s has more than %d context tiers", line.model, line.class, maxContextTiers)
}

// heldTo picks the one price a line is held to: the only tier, or the one
// tier the ledger priced the class at.
func heldTo(tiers []catalog.Price, skus []string) (catalog.Price, bool) {
	if len(tiers) == 1 {
		return tiers[0], true
	}
	var named []catalog.Price
	for _, tier := range tiers {
		if slices.Contains(skus, tier.SkuPriceID) {
			named = append(named, tier)
		}
	}
	if len(named) != 1 {
		return catalog.Price{}, false
	}
	return named[0], true
}

// impliedPrice is list over tokens per million tokens, rounded to
// impliedPlaces, halves away from zero. It is null when tokens is zero.
func impliedPrice(list, tokens decimal.Decimal) (*decimal.Decimal, error) {
	if tokens.Sign() == 0 {
		return nil, nil
	}
	amount, ok := new(big.Rat).SetString(list.String())
	if !ok {
		return nil, fmt.Errorf("implied price: %s is not a decimal", list)
	}
	count, ok := new(big.Rat).SetString(tokens.String())
	if !ok {
		return nil, fmt.Errorf("implied price: %s is not a decimal", tokens)
	}
	ratio := amount.Quo(amount, count)
	ratio.Mul(ratio, big.NewRat(pow10(tokensPerMillion), 1))
	price, err := decimal.ParseNumber(ratio.FloatString(impliedPlaces))
	if err != nil {
		return nil, fmt.Errorf("implied price: %w", err)
	}
	return &price, nil
}

func priceMissed(checks []PriceCheck) bool {
	return slices.ContainsFunc(checks, func(check PriceCheck) bool { return !check.Match })
}

// classDollars is the first line of the dollar comparison: each class at list
// price, the ledger's against the cost report's. It is null when the report
// does not split token cost by class, or a split line states no list figure.
func (c comparison) classDollars(key Key, group *ledgerGroup) []ClassDollars {
	return c.compareClasses(group, c.report.costLines(key))
}

// compareClasses sets a group's list cost by class beside the cost lines'.
func (c comparison) compareClasses(group *ledgerGroup, lines []Cost) []ClassDollars {
	var provider map[focus.TokenClass]decimal.Decimal
	for _, line := range lines {
		if !line.Tokens || line.Class == "" {
			continue
		}
		if line.ListFigure == nil {
			return nil
		}
		if provider == nil {
			provider = map[focus.TokenClass]decimal.Decimal{}
		}
		provider[line.Class] = provider[line.Class].Add(line.ListFigure.Value)
	}
	if provider == nil {
		return nil
	}
	ledger := group.classList
	if c.report.Source == SourceOpenAI || group.assumedLifetime {
		ledger, provider = joinWrites(ledger), joinWrites(provider)
	}
	classes := []ClassDollars{}
	for _, class := range compared {
		mine, theirs := ledger[class], provider[class]
		if mine.Sign() == 0 && theirs.Sign() == 0 {
			continue
		}
		classes = append(classes, ClassDollars{
			Class:    class,
			Ledger:   mine,
			Provider: theirs,
			Delta:    theirs.Sub(mine),
			Within:   within(mine, theirs, ClassDollarTolerancePercent),
		})
	}
	return classes
}

func appendNote(notes []string, note string) []string {
	if note == "" || slices.Contains(notes, note) {
		return notes
	}
	return append(notes, note)
}

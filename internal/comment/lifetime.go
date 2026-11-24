package comment

import (
	"fmt"

	"github.com/Costroid/costroid/internal/decimal"
)

// Lifetime is what the reports on a pull request's commits add up to, this
// run's included: one figure per currency and basis, never a sum across them.
type Lifetime struct {
	Figures []LifetimeFigure
	// Capped says the host listed as many commits as it lists, so a later
	// commit can be missing from the figures.
	Capped bool
	// Unread counts the check runs left out because their result object
	// could not be read or added up.
	Unread int
}

// LifetimeFigure is the sum of one currency and basis over the reports that
// carry it.
type LifetimeFigure struct {
	Currency string
	Basis    string
	Cost     decimal.Decimal
	// Reports counts the reports in the sum.
	Reports int
	// Undigested says a report in the sum states no digest of its rows, so a
	// second report over the same rows cannot be told apart from a new run.
	Undigested bool
}

// maxLifetimeFigures bounds the lines a lifetime adds to the comment.
const maxLifetimeFigures = 10

// FigureCost checks one figure of a result object read back from a check run
// and returns its amount. Any job of the repository can post such a check
// run, so the currency, the basis and the amount are each checked before they
// are added up or shown.
func FigureCost(figure BlockFigure) (decimal.Decimal, error) {
	if !currencyPattern.MatchString(figure.Currency) {
		return decimal.Decimal{}, fmt.Errorf("comment: currency %q is not an ISO code", string(Clean(figure.Currency)))
	}
	if _, known := basisLabels[figure.CostBasis]; !known {
		return decimal.Decimal{}, fmt.Errorf("comment: cost basis %q is not one this release knows", string(Clean(figure.CostBasis)))
	}
	cost, err := decimal.Parse(figure.ListCost)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("comment: list cost: %w", err)
	}
	return cost, nil
}

// commitCap says why a change's totals can be short. Only GitHub keeps the
// check runs the totals are read from, so the sentence names GitHub's limit
// in GitHub's words on every host.
const commitCap = "GitHub lists at most 250 commits of a pull request, so these totals can leave out later commits."

// sentences states each figure, then what the figures leave out. request is
// the host's word for a change.
func (l Lifetime) sentences(request string) []string {
	shown := l.Figures[:min(len(l.Figures), maxLifetimeFigures)]
	var lines []string
	for _, figure := range shown {
		line := fmt.Sprintf("Across this %s: %s at list (%s), from %d %s",
			request, Money(figure.Currency, figure.Cost.String()), basisLabels[figure.Basis], figure.Reports, plural(figure.Reports, "report", "reports"))
		if figure.Undigested && figure.Reports > 1 {
			line += ", which may include a repeated report"
		}
		lines = append(lines, line+".")
	}
	if omitted := len(l.Figures) - len(shown); omitted > 0 {
		lines = append(lines, fmt.Sprintf("%d more %s in other currencies or bases are left out.", omitted, plural(omitted, "total", "totals")))
	}
	if l.Capped {
		lines = append(lines, commitCap)
	}
	if l.Unread > 0 {
		lines = append(lines, fmt.Sprintf("%d %s left out of these totals: this release cannot read or add up %s result %s.",
			l.Unread, plural(l.Unread, "check run is", "check runs are"), plural(l.Unread, "its", "their"), plural(l.Unread, "object", "objects")))
	}
	return lines
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// CommentWithLifetime renders the sticky comment with the pull request's
// lifetime, which closes the folded section of figures that are not this
// run's. The lifetime is withheld with the amounts, and a nil lifetime
// renders the comment Comment does.
func CommentWithLifetime(input Input, lifetime *Lifetime) (string, error) {
	block, err := input.block()
	if err != nil {
		return "", err
	}
	data := input.view()
	data.Block = fenced(block)
	if lifetime != nil && input.Amounts == AmountsShown {
		totals := plural(min(len(lifetime.Figures), maxLifetimeFigures), "total", "totals")
		data.setBeside("this "+input.request()+"'s "+totals, lifetime.sentences(input.request())...)
	}
	return render("comment", data)
}

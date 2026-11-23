package comment

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/Costroid/costroid/internal/accuracy"
	"github.com/Costroid/costroid/internal/focus"
)

// accuracyURL is the page that lists every day the accuracy line counts.
const accuracyURL = "https://docs.costroid.com/accuracy"

// notCompared closes every accuracy line. It is true of every run a report
// shows: no report compares its run with a provider's report.
const notCompared = "Nothing has compared this run with a provider's report."

// accuracyLine says what Costroid's own test in this release found, for the
// agent and the provider the run's rows name, and that nothing compared this
// run. It names no amount, so it reads the same with amounts withheld. It is
// empty on a report with no figures, which it would speak for, and without a
// record.
func (i Input) accuracyLine() string {
	if i.Accuracy == nil || i.State != Reported {
		return ""
	}
	record := *i.Accuracy
	tested := record.Tested()
	if len(tested) == 0 {
		return "Costroid's own test, not this run: this build of Costroid holds no day of it. [The accuracy page](" + accuracyURL + ") lists the days. " + notCompared
	}
	if ran, one := i.Ledger.ranOne(); one && record.Covers(ran.agent, ran.provider) {
		return coveredLine(record.Count(ran.agent, ran.provider))
	}
	return notCoveredLine(tested)
}

// ranOne is the one agent and provider the run's agent rows name, false when
// they name none or more than one, a row names no agent or no provider, or a
// row is not on the measured basis. The test spends on an API key, and the
// usage report it compares with holds no subscription's use.
func (l Ledger) ranOne() (agentProvider, bool) {
	if len(l.ran) != 1 || l.ran[0].agent == "" || l.ran[0].provider == "" || l.ran[0].basis != string(focus.Measured) {
		return agentProvider{}, false
	}
	return l.ran[0], true
}

// notCoveredLine says what the test runs, each agent alone on its
// provider's API key, so that it reads true of every run it does not cover:
// another agent or provider, a fallback price, a subscription, or several
// agents or providers at once.
func notCoveredLine(tested []accuracy.Tested) string {
	runs := make([]string, len(tested))
	for index, pair := range tested {
		provider, _ := accuracy.ProviderName(pair.Source)
		runs[index] = Clean(pair.Agent).Code() + " alone on " + article(provider) + " " + provider + " API key"
	}
	other := "not that"
	switch {
	case len(tested) == 2:
		other = "neither"
	case len(tested) > 2:
		other = "none of them"
	}
	return fmt.Sprintf("Costroid's own test, not this run: it runs %s, and this run is %s, so no day on [the accuracy page](%s) speaks for these figures. %s",
		listed(runs, 0), other, accuracyURL, notCompared)
}

// article is the indefinite article a name takes, by its first letter.
func article(name string) string {
	if strings.ContainsAny(name[:1], "AEIOU") {
		return "an"
	}
	return "a"
}

// coveredLine counts the days of the record's window the run's agent ran on
// its provider, the days that failed included, and how close the token
// counts came on the days that passed.
func coveredLine(count accuracy.Count) string {
	provider, _ := accuracy.ProviderName(count.Source)
	agent := Clean(count.Agent).Code()
	if len(count.Versions) > 0 {
		agent += " " + listed(count.Versions, 0)
	}
	window := count.First + " to " + count.Last
	if count.First == count.Last {
		window = count.First
	}
	line := fmt.Sprintf("Costroid's own test, not this run: %s on %s passed %d of the %d %s it ran, %s (UTC)",
		agent, provider, count.Passed, count.Ran, plural(count.Ran, "day", "days"), window)
	switch {
	case count.Passed == 0 || count.TokenError == nil:
	case count.TokenError.Sign() == 0:
		line += ", and on each day that passed every token class matched " + provider + "'s usage report count for count"
	default:
		line += ", and on each day that passed every token class was within " + percentUp(count.TokenError) + " of " + provider + "'s usage report"
	}
	return line + ". [The accuracy page](" + accuracyURL + ") lists every day. " + notCompared
}

// percentUp writes a percentage to two places, rounded up, so the figure
// bounds the error: 0.001 reads 0.01%.
func percentUp(percent *big.Rat) string {
	scaled := new(big.Rat).Mul(percent, big.NewRat(100, 1))
	hundredths, remainder := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		hundredths.Add(hundredths, big.NewInt(1))
	}
	whole, fraction := new(big.Int).QuoRem(hundredths, big.NewInt(100), new(big.Int))
	return fmt.Sprintf("%s.%02d%%", whole, fraction.Int64())
}

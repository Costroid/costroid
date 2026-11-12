package policy

import (
	"strings"
	"testing"
)

// amountOf reads one amount key the way costroid.yml states it.
func amountOf(t *testing.T, literal string) (string, error) {
	t.Helper()
	settings, err := ParseSettings([]byte("policy:\n  run_budget_usd: " + literal + "\n"))
	if err != nil {
		return "", err
	}
	return settings.Amounts[keyRunBudget].String(), nil
}

// TestUnquotedDecimalAmountIsRead holds that an amount written as a plain
// decimal is read from its text, quoted or not, at up to twelve fractional
// digits, with no float between the text and the figure.
func TestUnquotedDecimalAmountIsRead(t *testing.T) {
	for literal, want := range map[string]string{
		"2.50":             "2.5",
		`"2.50"`:           "2.5",
		"0.1":              "0.1",
		"0":                "0",
		"0.5":              "0.5",
		"5":                "5",
		"5.0":              "5",
		"0.000000000001":   "0.000000000001",
		"9007199254740993": "9007199254740993",
		"2.000000000001":   "2.000000000001",
	} {
		got, err := amountOf(t, literal)
		if err != nil || got != want {
			t.Errorf("%s read as %q, %v, want %s", literal, got, err, want)
		}
	}
	if got, err := amountOf(t, "0.1234567890123"); err == nil {
		t.Errorf("thirteen fractional digits read as %s", got)
	}
}

// TestExponentAmountIsStillRefused holds that an amount in scientific
// notation, infinity or not-a-number is refused, quoted or not, in the
// sentence that shows the accepted form.
func TestExponentAmountIsStillRefused(t *testing.T) {
	for _, literal := range []string{"1e3", "1E3", "1.5e2", "1e-3", `"1e3"`, ".inf", "-.inf", ".nan", ".NaN", "1.", ".5"} {
		got, err := amountOf(t, literal)
		if err == nil || !strings.Contains(err.Error(), `policy.run_budget_usd must be a plain decimal of at most 12 fractional digits, such as 5, 2.50 or "2.50"`) {
			t.Errorf("%s read as %q, %v", literal, got, err)
		}
	}
}

// TestNonDecimalIntegerFormsAreRefused holds that YAML's other ways to write
// a whole number are refused: hex, octal in either spelling, underscores and
// a sign. A leading zero is octal, so 017 would read as 15.
func TestNonDecimalIntegerFormsAreRefused(t *testing.T) {
	for _, literal := range []string{"0x10", "0X10", "0o17", "017", "00", "1_000", "+5", "-5", "-0", "+0", `"017"`, `"+5"`} {
		if got, err := amountOf(t, literal); err == nil || !strings.Contains(err.Error(), "must be a plain decimal") {
			t.Errorf("%s read as %q, %v", literal, got, err)
		}
	}
}

package comment

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
)

// recordedRowsFiles are the rows files costroid run wrote from recordings.
var recordedRowsFiles = []string{goldenRowsFile, filepath.Join("testdata", "codex-stopped.jsonl")}

// seedRowsFiles adds each recorded rows file whole and each of its lines
// alone, then the synthetic rows the comment's tests build.
func seedRowsFiles(f *testing.F) {
	f.Helper()
	for _, path := range recordedRowsFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		for _, line := range bytes.Split(data, []byte("\n")) {
			f.Add(line)
		}
	}
	euro := sampleFields()
	euro.currency = "EUR"
	f.Add([]byte(sampleFields().line() + "\n" + euro.line() + "\n" + fallbackFields().line()))
	for _, hostile := range hostileStrings() {
		fields := sampleFields()
		fields.model = hostile
		f.Add([]byte(fields.line()))
	}
}

// FuzzReadRows reads a rows file the agent's job wrote, then renders every
// text a report writes from it, with amounts shown and withheld. A refusal
// names a line the file has. A file read holds no negative figure, its sums
// per currency are the policy's, and every rendering keeps GC1's invariants:
// one marker, inside GitHub's limit, no line break in an output, and no
// amount in a withheld text.
func FuzzReadRows(f *testing.F) {
	seedRowsFiles(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		readAndRender(t, data)
	})
}

// FuzzOneRowColumn sets one column of one recorded row, or one of its tags,
// to a fuzzed value, and holds the result to FuzzReadRows' properties. Blind
// bytes rarely reach a row the reader accepts; this starts from rows it does.
func FuzzOneRowColumn(f *testing.F) {
	data, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		f.Fatal(err)
	}
	line := uint8(0)
	for _, column := range []string{"ListCost", "ListUnitPrice", "PricingQuantity", "PricingUnit", "ConsumedQuantity", "BillingCurrency", "SkuMeter", "SkuPriceId", "ChargeDescription", "x_CostroidCatalogVersion", "Tags.costroid/model", "Tags.costroid/cost_basis", "Tags.costroid/attribution_grade", "Tags"} {
		for _, value := range []string{`"-1"`, `"2.0"`, `null`, `7`, `"9223372036854775807"`, `{}`, `"USD 5"`} {
			f.Add(line, column, []byte(value))
			line++
		}
	}
	f.Fuzz(func(t *testing.T, line uint8, column string, value []byte) {
		edited, ok := withColumn(data, int(line), column, value)
		if !ok {
			t.Skip("the edit names no column of a row")
		}
		readAndRender(t, edited)
	})
}

// withColumn sets column of the file's line-th row, counted from 0 and
// wrapped, to value: raw when it is JSON, else as a string. A column
// Tags.<key> sets one tag.
func withColumn(data []byte, line int, column string, value []byte) ([]byte, bool) {
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	index := line % len(lines)
	var row map[string]json.RawMessage
	if json.Unmarshal(lines[index], &row) != nil || column == "" {
		return nil, false
	}
	if !json.Valid(value) {
		quoted, err := json.Marshal(string(value))
		if err != nil {
			return nil, false
		}
		value = quoted
	}
	if key, ok := strings.CutPrefix(column, "Tags."); ok {
		var tags map[string]json.RawMessage
		if json.Unmarshal(row["Tags"], &tags) != nil || tags == nil {
			return nil, false
		}
		tags[key] = value
		encoded, err := json.Marshal(tags)
		if err != nil {
			return nil, false
		}
		value = encoded
		column = "Tags"
	}
	row[column] = value
	encoded, err := json.Marshal(row)
	if err != nil {
		return nil, false
	}
	lines[index] = encoded
	return append(bytes.Join(lines, []byte("\n")), '\n'), true
}

// readAndRender is FuzzReadRows' property over one rows file.
func readAndRender(t *testing.T, data []byte) {
	t.Helper()
	ledger, err := ReadRows(bytes.NewReader(data))
	if err != nil {
		var rowErr *RowError
		if !errors.As(err, &rowErr) {
			t.Fatalf("the refusal names no line: %v", err)
		}
		if lines := bytes.Count(data, []byte("\n")) + 1; rowErr.Line < 1 || rowErr.Line > lines {
			t.Fatalf("the refusal names line %d of a file of %d lines: %v", rowErr.Line, lines, err)
		}
		return
	}
	assertLedgerAgreesWithThePolicy(t, data, ledger)
	input := realInput(t)
	input.Ledger = ledger
	for _, amounts := range []Amounts{AmountsShown, AmountsOff, AmountsUnsettled} {
		input.Amounts = amounts
		for _, problem := range goldenProblems(input, renderState(t, input)) {
			t.Error(problem)
		}
	}
}

// assertLedgerAgreesWithThePolicy holds the comment's reading of a rows file
// to the policy's: the same rows, the same exact sum per currency, and no
// figure, trace or token count below zero.
func assertLedgerAgreesWithThePolicy(t *testing.T, data []byte, ledger Ledger) {
	t.Helper()
	usage, err := policy.ReadUsage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the comment read the rows and the policy refused them: %v", err)
	}
	if usage.Rows != ledger.Rows {
		t.Fatalf("the comment read %d rows and the policy %d", ledger.Rows, usage.Rows)
	}
	sums := map[string]decimal.Decimal{}
	for _, figure := range ledger.Figures {
		if figure.Cost.Sign() < 0 || figure.Rows < 1 || figure.FallbackRows < 0 || figure.FallbackRows > figure.Rows || figure.TraceOmitted < 0 {
			t.Fatalf("figure %+v", figure)
		}
		for class, tokens := range figure.Tokens {
			if tokens.Sign() < 0 {
				t.Fatalf("%s %s tokens", tokens, class)
			}
		}
		for _, trace := range figure.Trace {
			if trace.Cost.Sign() < 0 || trace.Tokens.Sign() < 0 || trace.UnitPrice.Sign() < 0 {
				t.Fatalf("trace %+v", trace)
			}
		}
		sums[figure.Currency] = sums[figure.Currency].Add(figure.Cost)
	}
	if ledger.FiguresOmitted > 0 {
		return
	}
	for _, entry := range usage.Currencies {
		if sums[entry.Currency].Cmp(entry.ListCost) != 0 {
			t.Fatalf("%s: the comment sums %s and the policy %s", entry.Currency, sums[entry.Currency], entry.ListCost)
		}
	}
	if len(sums) != len(usage.Currencies) {
		t.Fatalf("the comment holds %d currencies and the policy %d", len(sums), len(usage.Currencies))
	}
}

// warningLine is the only line ParseConfig writes to the job log.
var warningLine = regexp.MustCompile(`^costroid: warning: costroid\.yml: [ -~]+$`)

// FuzzParseConfig reads a costroid.yml. Every line it writes to the job log is
// one warning of printable text that carries no workflow command, whatever
// the file's keys hold.
func FuzzParseConfig(f *testing.F) {
	for _, config := range []string{
		"comment:\n  show_cost: false\n", "comment:\n  show_cost: yes\n", "comment: [1]\n",
		"policy_pack: x\n\"::set-output name=x::y\": 1\ncomment:\n  \"a\\nb\": 1\n", "- 1\n", "a: &x [*x]\n", "",
	} {
		f.Add([]byte(config))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var log bytes.Buffer
		previous := configWarnings
		configWarnings = &log
		defer func() { configWarnings = previous }()
		if _, err := ParseConfig(data); err != nil && strings.Contains(err.Error(), "\n") {
			t.Fatalf("the error spans lines: %q", err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(log.String(), "\n"), "\n") {
			if line != "" && (!warningLine.MatchString(line) || strings.Contains(line, "::")) {
				t.Fatalf("the log holds %q", line)
			}
		}
	})
}

package comment

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/policy"
)

// The FOCUS cost files the tests ingest, as a customer's estimator step would
// write them. Every name and figure is synthetic.
const (
	focusHeader     = "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName,SubAccountId\n"
	twoCurrencies   = focusHeader + "EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2,example-account-a\n" + "USD,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,1000,google_compute_instance.batch,Compute Engine,example-project-b\n"
	twoSubAccounts  = focusHeader + "EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2,example-account-a\n" + "EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,9.52,aws_ebs_volume.data,Amazon EBS,example-account-b\n"
	twoPeriods      = focusHeader + "EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2,example-account-a\n" + "EUR,2027-01-01T00:00:00Z,2027-02-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2,example-account-a\n"
	estimateSource  = "infrastructure"
	estimatedLineEU = "Infrastructure, estimated: EUR 25.488 for 2026-12, from `infrastructure`."
)

// estimateLines are the rows costroid ingest-run writes for a FOCUS cost file.
func estimateLines(t testing.TB, file string) string {
	t.Helper()
	read, err := focusfile.Read(strings.NewReader(file), focusfile.Options{Tags: map[string]string{focus.TagPR: "7", focus.TagCommitSHA: sampleHead}})
	if err != nil {
		t.Fatal(err)
	}
	var rows bytes.Buffer
	writer := focus.NewWriter(&rows)
	for _, row := range read.Rows {
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	return rows.String()
}

// withEstimate is the report on the golden rows with a FOCUS cost file's rows
// added after them, as ingest-run --from focus adds them.
func withEstimate(t testing.TB, input Input, file string) Input {
	t.Helper()
	agent, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadRows(strings.NewReader(string(agent) + estimateLines(t, file)))
	if err != nil {
		t.Fatal(err)
	}
	input.Ledger = ledger
	return input
}

func TestInfrastructureFiguresStandByCurrency(t *testing.T) {
	input := withEstimate(t, realInput(t), twoCurrencies)
	figures := input.Ledger.Infrastructure.Figures
	if len(figures) != 2 || figures[0].Currency != "EUR" || figures[0].Cost.String() != "25.488" || figures[1].Currency != "USD" || figures[1].Cost.String() != "1000" {
		t.Fatalf("the figures read %+v", figures)
	}
	if !reflect.DeepEqual(input.Ledger.Figures, realLedger(t).Figures) {
		t.Errorf("the agent's figures moved: %+v", input.Ledger.Figures)
	}
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		estimatedLineEU,
		"Infrastructure, estimated: USD 1000 for 2026-12, from `infrastructure`.",
		"The agent figure and these infrastructure figures are not added together, and no currency is added to another.",
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("the comment states %q %d times:\n%s", want, strings.Count(body, want), body)
		}
	}
	for _, sum := range []string{"1025.488", "1004.998380275", "30.486380275"} {
		if strings.Contains(body, sum) {
			t.Errorf("the comment adds figures into %s", sum)
		}
	}
}

func TestEveryProjectInTheFileIsCounted(t *testing.T) {
	input := withEstimate(t, realInput(t), twoSubAccounts)
	figures := input.Ledger.Infrastructure.Figures
	if len(figures) != 1 || figures[0].Rows != 2 || figures[0].Cost.String() != "35.008" {
		t.Fatalf("two sub-accounts in one currency and period read %+v", figures)
	}
	rows := input.Ledger.Infrastructure.Rows
	if len(rows) != 2 || rows[0].Resource != "aws_instance.web" || rows[1].Resource != "aws_ebs_volume.data" {
		t.Errorf("the rows read %+v", rows)
	}
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Infrastructure, estimated: EUR 35.008 for 2026-12, from `infrastructure`.",
		"| `aws_instance.web` | `Amazon EC2` | 2026-12 | EUR 25.488 |",
		"| `aws_ebs_volume.data` | `Amazon EBS` | 2026-12 | EUR 9.52 |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment lacks %q:\n%s", want, body)
		}
	}
	periods := withEstimate(t, realInput(t), twoPeriods).Ledger.Infrastructure.Figures
	if len(periods) != 2 || periods[0].Cost.String() != "25.488" || periods[1].Cost.String() != "25.488" {
		t.Errorf("two months read %+v: a figure is never restated to another period", periods)
	}
}

// TestCommentWithoutInfrastructureRowsIsUnchanged renders every golden state
// with no row outside an agent's and no note of a missing estimate, and holds
// its comment to the golden's bytes,
// with no word of the section, so the section appears only beside its rows.
func TestCommentWithoutInfrastructureRowsIsUnchanged(t *testing.T) {
	compared := 0
	for name, input := range goldenStates(t) {
		if len(input.Ledger.Infrastructure.Figures) > 0 || input.EstimateUnavailable != nil {
			continue
		}
		body, err := CommentWithLifetime(input, input.Lifetime)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(filepath.Join(goldenDir, name, goldenComment))
		if err != nil {
			t.Fatal(err)
		}
		if body != string(saved) {
			t.Errorf("%s: the comment is not its golden", name)
		}
		if strings.Contains(body, "Infrastructure") || strings.Contains(body, "not added together") {
			t.Errorf("%s: a comment with no such rows names the section", name)
		}
		compared++
	}
	if compared < 50 {
		t.Errorf("compared %d goldens, fewer than the 50 the states held before the section", compared)
	}
	if ledger := realLedger(t); len(ledger.Infrastructure.Figures) != 0 || ledger.Infrastructure.view(true) != nil {
		t.Error("the agent's rows read as infrastructure")
	}
}

func TestTheMachineBlockCarriesNoInfrastructureFigure(t *testing.T) {
	plain := realInput(t)
	estimated := withEstimate(t, plain, twoCurrencies)
	for _, amounts := range []Amounts{AmountsShown, AmountsOff} {
		plain.Amounts, estimated.Amounts = amounts, amounts
		want, err := Result(plain)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Result(estimated)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("the rows outside an agent's changed the result object:\n%s", got)
		}
	}
}

func TestWithheldTheSectionNamesNoAmount(t *testing.T) {
	input := withEstimate(t, realInput(t), twoCurrencies)
	input.Amounts = AmountsOff
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Infrastructure, estimated, in EUR for 2026-12, from `infrastructure`.") || strings.Contains(body, "25.488") || strings.Contains(body, "1000") {
		t.Errorf("the withheld section reads:\n%s", readable(body))
	}
}

func TestEstimatesAloneSayTheReportShowsNoAgentFigures(t *testing.T) {
	ledger, err := ReadRows(strings.NewReader(estimateLines(t, twoCurrencies)))
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Rows != 0 || len(ledger.Figures) != 0 || len(ledger.Infrastructure.Figures) != 2 {
		t.Fatalf("estimates alone read %d agent rows and %d figures", ledger.Rows, len(ledger.Figures))
	}
	input := noFigures(NoRows, EmptyRowsFile)
	input.Ledger = ledger
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"The rows file holds no agent rows, so this report shows no agent figures.", estimatedLineEU} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment lacks %q:\n%s", want, readable(body))
		}
	}
}

func TestARowOutsideAnAgentsIsCheckedAsEveryRowIs(t *testing.T) {
	good := strings.TrimSuffix(estimateLines(t, twoCurrencies), "\n")
	first, _, _ := strings.Cut(good, "\n")
	for name, edit := range map[string][2]string{
		"an open basis":       {`"costroid/cost_basis":"estimate"`, `"costroid/cost_basis":"guess"`},
		"a period backwards":  {`"ChargePeriodEnd":"2027-01-01T00:00:00Z"`, `"ChargePeriodEnd":"2026-11-01T00:00:00Z"`},
		"a period in a day":   {`"ChargePeriodStart":"2026-12-01T00:00:00Z"`, `"ChargePeriodStart":"2026-12-01"`},
		"a negative amount":   {`"ListCost":"25.488"`, `"ListCost":"-25.488"`},
		"a lower-case code":   {`"BillingCurrency":"EUR"`, `"BillingCurrency":"eur"`},
		"a source not a text": {`"costroid/cost_source":"infrastructure"`, `"costroid/cost_source":7`},
	} {
		line := strings.Replace(first, edit[0], edit[1], 1)
		if line == first {
			t.Fatalf("%s: the edit changed nothing", name)
		}
		var invalid *RowError
		if _, err := ReadRows(strings.NewReader(line)); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAPeriodIsNamedExactly(t *testing.T) {
	at := func(text string) time.Time {
		instant, err := time.Parse(focus.InstantLayout, text)
		if err != nil {
			t.Fatal(err)
		}
		return instant
	}
	for _, tc := range []struct{ start, end, want string }{
		{"2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z", "2026-12"},
		{"2026-12-01T00:00:00Z", "2026-12-02T00:00:00Z", "2026-12-01"},
		{"2026-12-01T00:00:00Z", "2026-12-15T00:00:00Z", "2026-12-01 to 2026-12-14"},
		{"2026-12-01T00:00:00Z", "2027-01-02T00:00:00Z", "2026-12-01 to 2027-01-01"},
		{"2026-12-15T00:00:00Z", "2027-01-15T00:00:00Z", "2026-12-15 to 2027-01-14"},
		{"2026-12-01T06:00:00Z", "2026-12-01T07:00:00Z", "2026-12-01T06:00:00Z to 2026-12-01T07:00:00Z"},
	} {
		if got := (Period{Start: at(tc.start), End: at(tc.end)}).String(); got != tc.want {
			t.Errorf("%s to %s reads %q, want %q", tc.start, tc.end, got, tc.want)
		}
	}
}

func TestANameFromTheRowsIsShownOnlyWhenEachWordIsClean(t *testing.T) {
	for text, want := range map[string]Data{
		"Amazon EC2":                         "Amazon EC2",
		"aws_instance.web":                   "aws_instance.web",
		"":                                   "",
		"two  spaces":                        notShown,
		" leading":                           notShown,
		"a `tick`":                           notShown,
		"@mention here":                      notShown,
		"<b>bold</b>":                        notShown,
		strings.Repeat("a", maxDataLength+1): notShown,
	} {
		if got := cleanWords(text); got != want {
			t.Errorf("%q reads %q, want %q", text, got, want)
		}
	}
}

func TestTheVerdictReadsTheSameWithOrWithoutEstimates(t *testing.T) {
	agent, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		t.Fatal(err)
	}
	alone, err := policy.ReadUsage(bytes.NewReader(agent))
	if err != nil {
		t.Fatal(err)
	}
	both, err := policy.ReadUsage(strings.NewReader(string(agent) + estimateLines(t, twoCurrencies)))
	if err != nil {
		t.Fatal(err)
	}
	if alone.Rows != both.Rows || len(alone.Currencies) != len(both.Currencies) || alone.Currencies[0].ListCost.Cmp(both.Currencies[0].ListCost) != 0 {
		t.Errorf("the estimate reached the usage: %+v, against %+v", both, alone)
	}
}

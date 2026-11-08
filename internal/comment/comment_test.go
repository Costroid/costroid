package comment

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/policy"
)

const (
	realTotal  = "0.001619725"
	sampleHead = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	// exampleCheckRun and exampleInstallation are the synthetic ids in
	// docs/comment-example.md.
	exampleCheckRun     = int64(16873409921)
	exampleInstallation = "58120394"
	exampleRun          = "https://github.com/example/widgets/actions/runs/19203384756"
	exampleBase         = "1111111111111111111111111111111111111111"
	// githubLimit is the longest comment or check text GitHub takes.
	githubLimit = 65535
)

var update = flag.Bool("update", false, "rewrite docs/comment-example.md and the golden comments")

// rowFields is one row of one million output tokens, priced at cost per
// million, so its own arithmetic holds whatever cost says.
type rowFields struct {
	currency, cost, basis, attribution, enforcement, agent, model, priceID, description string
}

func sampleFields() rowFields {
	return rowFields{"USD", "0.1", "measured", "exact", "wrapper-watchdog", "codex", "gpt-5.5", "openai:gpt-5.5:global:standard:output", "gpt-5.5 output tokens"}
}

func (f rowFields) line() string {
	return fmt.Sprintf(`{"BillingCurrency":%q,"ListCost":%q,"ListUnitPrice":%q,"PricingQuantity":"1","PricingUnit":"1000000 Tokens","ConsumedQuantity":"1000000","SkuMeter":"output","SkuPriceId":%q,"ChargeDescription":%q,"x_CostroidCatalogVersion":"0.1.0","Tags":{"costroid/cost_basis":%q,"costroid/attribution_grade":%q,"costroid/enforcement_grade":%q,"costroid/agent":%q,"costroid/model":%q,"team":7}}`,
		f.currency, f.cost, f.cost, f.priceID, f.description, f.basis, f.attribution, f.enforcement, f.agent, f.model)
}

// fallbackFields is a row of a model the catalog does not name, priced by the
// fallback row the way the run writes it.
func fallbackFields() rowFields {
	fields := sampleFields()
	fields.model, fields.cost = "gpt-9-preview", "180"
	fields.priceID = "openai:fallback:output"
	fields.description = "gpt-9-preview output tokens; " + focus.NoteFallback
	return fields
}

func ledgerOf(t testing.TB, rows ...rowFields) Ledger {
	t.Helper()
	lines := make([]string, len(rows))
	for index, row := range rows {
		lines[index] = row.line()
	}
	ledger, err := ReadRows(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func realLedger(t testing.TB) Ledger {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "rows.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ledger, err := ReadRows(file)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

// artifactLabel marks figures that the agent's own job wrote.
const artifactLabel = "Unverified: the agent's job wrote these figures."

func realInput(t testing.TB) Input {
	t.Helper()
	id := exampleCheckRun
	budget := decimal.MustParse("5")
	return Input{
		Ledger:       realLedger(t),
		State:        Reported,
		HeadSHA:      sampleHead,
		Budget:       &budget,
		Installation: exampleInstallation,
		CheckRunID:   &id,
		Amounts:      AmountsShown,
		RunURL:       exampleRun,
	}
}

// exampleInput is realInput with the verdict the report gives it under a USD 5
// budget set at the base commit, at a fixed time.
func exampleInput(t *testing.T) Input {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "rows.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	usage, err := policy.ReadUsage(file)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage:  usage,
		Trust:  policy.Unverified,
		Config: policy.ConfigFile{Data: []byte("policy:\n  run_budget_usd: \"5\"\n"), Found: true, Ref: exampleBase, FromBase: true},
		Start:  policy.Start{At: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
		At:     time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := realInput(t)
	input.Verdict = &verdict
	return input
}

// demoRows are the rows costroid demo embeds, read from a recorded
// claude-code-action run, and demoTotal is what that run reported.
const (
	demoRows  = "../../cmd/costroid/demo/rows.jsonl"
	demoTotal = "0.0391529"
)

// docsInput is the comment costroid demo prints and docs/comment-example.md
// shows: the recorded run on a pull request whose costroid.yml at the base
// commit sets a USD 5 run budget, reported without the run's cap.
func docsInput(t *testing.T) Input {
	t.Helper()
	data, err := os.ReadFile(demoRows)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadRows(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	usage, err := policy.ReadUsage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage:  usage,
		Trust:  policy.Unverified,
		Config: policy.ConfigFile{Data: []byte("policy:\n  run_budget_usd: \"5\"\n"), Found: true, Ref: exampleBase, FromBase: true},
		Start:  policy.Start{At: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
		At:     time.Date(2026, 11, 7, 11, 40, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := exampleCheckRun
	return Input{
		Ledger:       ledger,
		State:        Reported,
		HeadSHA:      sampleHead,
		Installation: exampleInstallation,
		CheckRunID:   &id,
		Amounts:      AmountsShown,
		RunURL:       exampleRun,
		Verdict:      &verdict,
	}
}

func commentOf(t *testing.T, input Input) string {
	t.Helper()
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

var collapsed = regexp.MustCompile(`(?s)<details>\s*<summary>(.*?)</summary>.*?</details>`)

// visiblePart is what a reader sees before opening anything: every collapsed
// block is cut to its summary.
func visiblePart(body string) string {
	return collapsed.ReplaceAllString(body, "$1")
}

func TestDocsExampleIsTheRenderedCommentForARealRowsFile(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "comment-example.md")
	body := commentOf(t, docsInput(t))
	if *update {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != body {
		t.Errorf("%s is stale, rerun with -update", path)
	}
}

func TestRealRowsReadAsTheTotalTheRunPrinted(t *testing.T) {
	ledger := realLedger(t)
	if ledger.Rows != 19 || len(ledger.Figures) != 1 {
		t.Fatalf("ledger = %+v", ledger)
	}
	figure := ledger.Figures[0]
	if figure.Cost.String() != realTotal || figure.Basis != "list_equivalent" || figure.Attribution != "unattributed" || figure.Enforcement != "agent-flag" {
		t.Errorf("figure = %+v", figure)
	}
}

func TestSumsAreExactAndNeverCrossACurrency(t *testing.T) {
	usd, eur := sampleFields(), sampleFields()
	eur.currency = "EUR"
	second := usd
	second.cost = "0.2"
	ledger := ledgerOf(t, usd, second, eur)
	if len(ledger.Figures) != 2 {
		t.Fatalf("figures = %+v", ledger.Figures)
	}
	body := commentOf(t, Input{Ledger: ledger, State: Reported, Amounts: AmountsShown})
	for _, want := range []string{"| USD 0.3 |", "| EUR 0.1 |"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "| USD 0.4") || strings.Contains(body, `"0.4"`) {
		t.Errorf("a figure spans two currencies:\n%s", body)
	}
}

func TestEveryAmountSitsBesideItsCurrency(t *testing.T) {
	body := visiblePart(commentOf(t, realInput(t)))
	amounts := regexp.MustCompile(`.{0,4}[0-9]+\.[0-9]{3,}\b`)
	for _, found := range amounts.FindAllString(body, -1) {
		if !strings.HasPrefix(found, "USD ") {
			t.Errorf("bare amount %q", found)
		}
	}
	for _, want := range []string{"USD " + realTotal, "Cap: USD 5 ("} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
}

func TestAWeakerGradeReadsBeforeAStrongerOne(t *testing.T) {
	strong, weak := sampleFields(), sampleFields()
	weak.enforcement = "none"
	body := commentOf(t, Input{Ledger: ledgerOf(t, strong, weak), State: Reported, Amounts: AmountsShown})
	if strings.Index(body, "none:") > strings.Index(body, "wrapper-watchdog:") {
		t.Errorf("the stronger grade reads first:\n%s", body)
	}
}

func TestListEquivalentIsNeverCalledBilled(t *testing.T) {
	body := commentOf(t, realInput(t))
	if !strings.Contains(body, "List-equivalent, not billed") {
		t.Fatalf("no label in\n%s", body)
	}
	if strings.Contains(strings.ToLower(strings.ReplaceAll(body, "not billed", "")), "billed") {
		t.Errorf("a list-equivalent figure reads as billed:\n%s", body)
	}
}

func TestEveryBasisCarriesItsSpecLabel(t *testing.T) {
	labels := map[string]string{"measured": "Measured", "estimate": "Estimate", "billed": "Billed", "derived": "Derived"}
	for basis, label := range labels {
		fields := sampleFields()
		fields.basis = basis
		body := commentOf(t, Input{Ledger: ledgerOf(t, fields), State: Reported, Amounts: AmountsShown})
		if !strings.Contains(body, "| "+label+" |") {
			t.Errorf("%s has no label %s:\n%s", basis, label, body)
		}
	}
}

func TestUnattributedIsShownAsSuch(t *testing.T) {
	fields := sampleFields()
	fields.attribution = "unattributed"
	body := commentOf(t, Input{Ledger: ledgerOf(t, fields), State: Reported, Amounts: AmountsShown})
	if !strings.Contains(visiblePart(body), "| unattributed |") {
		t.Errorf("no unattributed cell:\n%s", body)
	}
}

func TestABadRowRefusesTheWholeFile(t *testing.T) {
	cases := map[string]string{
		"cost as a JSON number":                 strings.Replace(sampleFields().line(), `"ListCost":"0.1"`, `"ListCost":0.1`, 1),
		"negative cost":                         strings.Replace(sampleFields().line(), `"0.1"`, `"-0.1"`, 1),
		"exponent":                              strings.Replace(sampleFields().line(), `"0.1"`, `"1e3"`, 1),
		"past the scale limit":                  strings.Replace(sampleFields().line(), `"0.1"`, `"0.1234567890123"`, 1),
		"very long cost":                        strings.Replace(sampleFields().line(), `"0.1"`, `"`+strings.Repeat("9", 70)+`"`, 1),
		"not a currency":                        strings.Replace(sampleFields().line(), `"USD"`, `"usd"`, 1),
		"unknown basis":                         strings.Replace(sampleFields().line(), `"measured"`, `"guessed"`, 1),
		"unknown grade":                         strings.Replace(sampleFields().line(), `"exact"`, `"perfect"`, 1),
		"unknown enforcement":                   strings.Replace(sampleFields().line(), `"wrapper-watchdog"`, `"gateway-precheck-plus"`, 1),
		"no grade":                              strings.Replace(sampleFields().line(), `"costroid/attribution_grade"`, `"other"`, 1),
		"not JSON":                              "{",
		"a row past the cap":                    `{"x":"` + strings.Repeat("a", maxRowBytes) + `"}`,
		"cost is not price times quantity":      strings.Replace(sampleFields().line(), `"ListUnitPrice":"0.1"`, `"ListUnitPrice":"0.2"`, 1),
		"tokens are not quantity times unit":    strings.Replace(sampleFields().line(), `"ConsumedQuantity":"1000000"`, `"ConsumedQuantity":"999999"`, 1),
		"no price":                              strings.Replace(sampleFields().line(), `"ListUnitPrice":"0.1",`, ``, 1),
		"unknown token class":                   strings.Replace(sampleFields().line(), `"SkuMeter":"output"`, `"SkuMeter":"thinking"`, 1),
		"a unit that is no count of tokens":     strings.Replace(sampleFields().line(), `"1000000 Tokens"`, `"1 Million Tokens"`, 1),
		"an informational class with a cost":    strings.Replace(sampleFields().line(), `"SkuMeter":"output"`, `"SkuMeter":"reasoning_output"`, 1),
		"an informational class priced by unit": strings.NewReplacer(`"SkuMeter":"output"`, `"SkuMeter":"reasoning_output"`, `"ListCost":"0.1","ListUnitPrice":"0.1"`, `"ListCost":"0","ListUnitPrice":"0"`).Replace(sampleFields().line()),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadRows(strings.NewReader(sampleFields().line() + "\n" + line)); err == nil {
				t.Error("the file was read")
			}
		})
	}
}

func TestEveryReasonForNoFiguresIsStatedAndNoneShowsAFigure(t *testing.T) {
	cases := []struct {
		state    State
		reason   Reason
		line     int
		sentence string
	}{
		{NoRows, EmptyRowsFile, 0, "The rows file holds no rows, so this report shows no figures. Either the agent printed no usage or its job stopped before it wrote a row, and that job's log says which."},
		{Unreadable, NoRowsFile, 0, "No rows file reached this report, so it shows no figures. The agent's job log says why it wrote none."},
		{Unreadable, NotAFile, 0, "The rows path is not a regular file, so this report did not read it and shows no figures."},
		{Unreadable, UnreadRowsFile, 0, "The rows file could not be read, so this report shows no figures."},
		{Unreadable, InvalidRow, 3, "Line 3 of the rows file is not a valid row, so this report shows no figures. A partial total would read as a smaller cost."},
		{NoRows, "", 0, "The rows file holds no rows, so this report shows no figures."},
		{Unreadable, "", 0, "The rows file could not be read, so this report shows no figures."},
	}
	for _, test := range cases {
		input := Input{State: test.state, Reason: test.reason, ReasonLine: test.line, Amounts: AmountsShown}
		body := commentOf(t, input)
		output, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		for where, text := range map[string]string{"comment": visiblePart(body), "summary": output.Summary} {
			if !strings.Contains(text, test.sentence) {
				t.Errorf("%s %s: no sentence in the %s:\n%s", test.state, test.reason, where, text)
			}
			if strings.Contains(text, "No cost rows") || strings.Contains(text, "| Cost |") || strings.Contains(text, "USD") {
				t.Errorf("%s %s: the %s shows a figure or no reason:\n%s", test.state, test.reason, where, text)
			}
		}
		if !strings.Contains(body, `"state": "`+string(test.state)+`"`) {
			t.Errorf("%s: the block does not name the state", test.state)
		}
		if test.reason != "" && !strings.Contains(output.Text, `"reason": "`+string(test.reason)+`"`) {
			t.Errorf("%s: the block does not name the reason:\n%s", test.reason, output.Text)
		}
	}
}

func TestAReportWithFiguresStatesNoReason(t *testing.T) {
	output, err := Check(realInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.Text, `"reason"`) {
		t.Errorf("a report with figures names a reason:\n%s", output.Text)
	}
}

func TestReadRowsNamesTheLineAtFault(t *testing.T) {
	lines := []string{sampleFields().line(), "", sampleFields().line(), "{"}
	_, err := ReadRows(strings.NewReader(strings.Join(lines, "\n")))
	var invalid *RowError
	if !errors.As(err, &invalid) || invalid.Line != 4 {
		t.Fatalf("error %v", err)
	}
	_, err = ReadRows(strings.NewReader(sampleFields().line() + "\n" + strings.Repeat("a", maxRowBytes+1)))
	if !errors.As(err, &invalid) || invalid.Line != 2 {
		t.Fatalf("a line past the cap: error %v", err)
	}
}

func TestFiguresReadFromTheArtifactAreLabelledUnverifiedInWords(t *testing.T) {
	for name, amounts := range map[string]Amounts{"shown": AmountsShown, "off": AmountsOff, "unsettled": AmountsUnsettled} {
		input := realInput(t)
		input.Amounts = amounts
		body := commentOf(t, input)
		if !strings.Contains(visiblePart(body), artifactLabel) {
			t.Errorf("%s: no label in\n%s", name, visiblePart(body))
		}
		if !strings.Contains(body, `"run_identity": "unverified"`) {
			t.Errorf("%s: the block does not say so", name)
		}
		output, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.Summary, artifactLabel) || !strings.Contains(output.Text, `"run_identity": "unverified"`) {
			t.Errorf("%s: the check does not say so:\n%s", name, output.Summary)
		}
	}
}

// TestARowThatClaimsVerificationStillReadsUnverified holds the point of the
// label: the agent's job writes the file, so the tag inside it proves nothing.
func TestARowThatClaimsVerificationStillReadsUnverified(t *testing.T) {
	raw, err := os.ReadFile("testdata/rows.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	forged := strings.NewReplacer(`"costroid/run_identity":"unverified"`, `"costroid/run_identity":"token_verified"`,
		`"costroid/attribution_grade":"unattributed"`, `"costroid/attribution_grade":"exact"`).Replace(string(raw))
	if forged == string(raw) {
		t.Fatal("the fixture changed shape, so this test forged nothing")
	}
	ledger, err := ReadRows(strings.NewReader(forged))
	if err != nil {
		t.Fatal(err)
	}
	input := realInput(t)
	input.Ledger = ledger
	body := commentOf(t, input)
	if !strings.Contains(visiblePart(body), artifactLabel) {
		t.Errorf("a forged tag removed the label:\n%s", visiblePart(body))
	}
	if strings.Contains(body, "exact") {
		t.Errorf("a forged grade reached the comment:\n%s", body)
	}
}

func TestReportsWithNoFiguresCarryNoLabelAndNoTrace(t *testing.T) {
	for _, state := range []State{NoRows, Unreadable} {
		body := commentOf(t, Input{State: state, Amounts: AmountsShown})
		if strings.Contains(body, artifactLabel) {
			t.Errorf("%s labels a figure it does not show", state)
		}
		if strings.Contains(body, "Tokens behind") || strings.Contains(body, "was computed") {
			t.Errorf("%s traces a figure it does not show", state)
		}
	}
}

func hostileStrings() map[string]string {
	return map[string]string{
		"markdown image":    "![x](https://evil.example/p.png)",
		"markdown link":     "[click](https://evil.example)",
		"heading":           "# heading",
		"table cell":        "a|b",
		"html tag":          "<img src=x onerror=alert(1)>",
		"html comment":      "--><script>alert(1)</script>",
		"mention":           "@octocat",
		"team mention":      "@costroid/owners",
		"backticks":         "`code`",
		"fence":             "```\n# escaped",
		"newline":           "model\n\n# Heading",
		"carriage return":   "model\r\n@octocat",
		"nul":               "model\x00",
		"unicode lookalike": "сlaude-opus",
		"right-to-left":     "model\u202egpj.exe",
		"long":              strings.Repeat("a", 100_000),
		"entity":            "&lt;img&gt;",
		"issue link":        "#1234",
	}
}

func TestHostileStringsNeverRenderAsMarkdownOrHTML(t *testing.T) {
	for name, hostile := range hostileStrings() {
		t.Run(name, func(t *testing.T) {
			fields := fallbackFields()
			fields.model = hostile
			fields.agent = hostile
			fields.priceID = hostile
			clean := sampleFields()
			ledger, err := ReadRows(strings.NewReader(strings.ReplaceAll(jsonLine(t, fields), "\n", "") + "\n" + clean.line()))
			if err != nil {
				t.Fatal(err)
			}
			for _, amounts := range []Amounts{AmountsShown, AmountsOff} {
				input := Input{Ledger: ledger, State: Reported, HeadSHA: sampleHead, Amounts: amounts, RunURL: hostile}
				output, err := Check(input)
				if err != nil {
					t.Fatal(err)
				}
				for where, text := range map[string]string{"comment": commentOf(t, input), "summary": output.Summary, "text": output.Text} {
					assertInert(t, where, text, hostile)
				}
			}
		})
	}
}

func jsonLine(t *testing.T, fields rowFields) string {
	t.Helper()
	model, err := json.Marshal(fields.model)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := json.Marshal(fields.agent)
	if err != nil {
		t.Fatal(err)
	}
	priceID, err := json.Marshal(fields.priceID)
	if err != nil {
		t.Fatal(err)
	}
	line := fields
	line.model = "MODEL"
	line.agent = "AGENT"
	line.priceID = "PRICE"
	return strings.NewReplacer(`"MODEL"`, string(model), `"AGENT"`, string(agent), `"PRICE"`, string(priceID)).Replace(line.line())
}

func assertInert(t *testing.T, where, text, hostile string) {
	t.Helper()
	if strings.Contains(text, hostile) {
		t.Errorf("%s holds the hostile string", where)
	}
	if !strings.Contains(text, string(notShown)) {
		t.Errorf("%s does not say a value was not shown:\n%s", where, text)
	}
	stripped := strings.NewReplacer("<!-- costroid:comment -->", "", "<details>", "", "</details>", "", "<summary>", "", "</summary>", "").Replace(text)
	for _, forbidden := range []string{"<", "@", "![", "\x00", "\u202e", "evil.example", "&lt;"} {
		if strings.Contains(stripped, forbidden) {
			t.Errorf("%s holds %q:\n%s", where, forbidden, text)
		}
	}
	if count := strings.Count(text, "```"); count%2 != 0 {
		t.Errorf("%s leaves a code fence open:\n%s", where, text)
	}
}

func TestAllowlistKeepsRealIdentifiers(t *testing.T) {
	for _, id := range []string{"claude-haiku-5-5", "gpt-5.5", "anthropic.claude-sonnet-4-5:0", "models/gemini_3", "1.2.3+build"} {
		if string(Clean(id)) != id {
			t.Errorf("Clean(%q) = %q", id, Clean(id))
		}
	}
	if Clean(strings.Repeat("a", maxDataLength)) == notShown {
		t.Error("a string at the cap was refused")
	}
	if Clean(strings.Repeat("a", maxDataLength+1)) != notShown {
		t.Error("a string past the cap was kept")
	}
}

func FuzzCleanYieldsOnlyAllowedCharacters(f *testing.F) {
	for _, hostile := range hostileStrings() {
		f.Add(hostile)
	}
	allowed := regexp.MustCompile(`^[A-Za-z0-9._:/+_-]+$`)
	f.Fuzz(func(t *testing.T, text string) {
		clean := string(Clean(text))
		if len(clean) > maxDataLength || !allowed.MatchString(clean) {
			t.Errorf("Clean(%q) = %q", text, clean)
		}
		if clean != string(notShown) && clean != text {
			t.Errorf("Clean(%q) rewrote it to %q", text, clean)
		}
	})
}

func TestThousandsOfDistinctStringsStayInsideGitHubsLimit(t *testing.T) {
	rows := make([]rowFields, 0, 30_000)
	currencies := []string{"AAA", "BBB", "CCC", "DDD", "EEE", "FFF", "GGG"}
	for index := range 30_000 {
		fields := sampleFields()
		fields.model = strings.Repeat("m", maxDataLength-8) + fmt.Sprintf("%08d", index)
		fields.currency = currencies[index%len(currencies)]
		fields.enforcement = enforcementOrder[index%len(enforcementOrder)]
		rows = append(rows, fields)
	}
	ledger := ledgerOf(t, rows...)
	if ledger.FiguresOmitted == 0 || ledger.ModelsOmitted == 0 {
		t.Fatalf("nothing was capped: %d figures, %d models omitted", ledger.FiguresOmitted, ledger.ModelsOmitted)
	}
	input := Input{Ledger: ledger, State: Reported, HeadSHA: sampleHead, Amounts: AmountsShown}
	body := commentOf(t, input)
	if len(body) > githubLimit {
		t.Errorf("comment is %d bytes", len(body))
	}
	if !strings.Contains(body, "more lines are left out of this table") {
		t.Errorf("the cut is not stated:\n%s", visiblePart(body))
	}
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Summary) > githubLimit || len(output.Text) > githubLimit {
		t.Errorf("check output is %d and %d bytes", len(output.Summary), len(output.Text))
	}
}

// TestVisiblePartStaysShort counts every word a reader sees before opening a
// block, summaries included. The comment held 67 such words before it named
// its run and traced its figure, 86 before its budget line stated the cap,
// the grade, the cost and what is left or over, and 106 before its first line
// stated the figure.
func TestVisiblePartStaysShort(t *testing.T) {
	const maxVisibleWords = 125
	table := strings.NewReplacer("|", " ", "---", " ")
	words := strings.Fields(table.Replace(visiblePart(commentOf(t, realInput(t)))))
	t.Logf("%d words", len(words))
	if len(words) > maxVisibleWords {
		t.Errorf("%d words", len(words))
	}
}

func TestCommentNeverScolds(t *testing.T) {
	input := realInput(t)
	cases := []Input{input, {State: NoRows}, {State: Unreadable}}
	withheld := input
	withheld.Amounts = AmountsOff
	cases = append(cases, withheld)
	scolding := regexp.MustCompile(`(?i)\b(must|should|violat\w*|exceed\w*|fail\w*|error|warning|please|sorry|careful)\b|!`)
	for _, candidate := range cases {
		text := strings.ReplaceAll(visiblePart(commentOf(t, candidate)), "<!--", "")
		if found := scolding.FindString(text); found != "" {
			t.Errorf("%q in\n%s", found, text)
		}
	}
}

func TestFirstLineStatesTheFigureWithItsBasis(t *testing.T) {
	first := func(body string) string {
		line, _, _ := strings.Cut(strings.TrimPrefix(body, Marker+"\n### Costroid\n\n"), "\n")
		return line
	}
	if got, want := first(commentOf(t, docsInput(t))), "This run cost USD "+demoTotal+" at list (Measured). Nothing held it to a budget."; got != want {
		t.Errorf("first line %q, want %q", got, want)
	}
	if got, want := first(commentOf(t, realInput(t))), "This run cost USD "+realTotal+" at list (List-equivalent, not billed). Held by agent-flag: client-side estimate, can pass the cap."; got != want {
		t.Errorf("first line %q, want %q", got, want)
	}
	if !strings.Contains(commentOf(t, realInput(t)), "can pass the cap.\n\n**Verdict: neutral.**") {
		t.Error("the verdict does not follow the first line")
	}
	output, err := Check(realInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.Summary, "This run cost USD "+realTotal+" at list") {
		t.Errorf("the check's summary does not open on the figure:\n%s", output.Summary)
	}
	euro, measured := sampleFields(), sampleFields()
	euro.currency, euro.cost = "EUR", "0.2"
	measured.enforcement = "none"
	input := realInput(t)
	input.Budget = nil
	input.Ledger = ledgerOf(t, sampleFields(), euro, measured)
	want := "This run cost EUR 0.2 at list (Measured), USD 0.1 at list (Measured) and USD 0.1 at list (Measured). Nothing held it to a budget."
	if got := first(commentOf(t, input)); got != want {
		t.Errorf("first line %q, want %q", got, want)
	}
	for _, empty := range []Input{{State: NoRows, Amounts: AmountsShown}, {State: Unreadable, Reason: InvalidRow, Amounts: AmountsShown}} {
		if body := commentOf(t, empty); !strings.HasPrefix(first(body), "**Verdict: ") {
			t.Errorf("a report with no figures opens on %q", first(body))
		}
	}
}

func TestFirstLineWithheldWhenShowCostFalse(t *testing.T) {
	for _, amounts := range []Amounts{AmountsOff, AmountsUnsettled} {
		input := docsInput(t)
		input.Amounts = amounts
		body := commentOf(t, input)
		want := Marker + "\n### Costroid\n\nThis run was priced at list (Measured). Nothing held it to a budget.\n\n**Verdict: neutral.**"
		if !strings.HasPrefix(body, want) {
			t.Errorf("amounts %d:\n%s", amounts, body)
		}
		if strings.Contains(body, demoTotal) {
			t.Errorf("amounts %d: the comment shows the amount:\n%s", amounts, body)
		}
	}
	input := realInput(t)
	input.Amounts = AmountsOff
	euro := sampleFields()
	euro.currency = "EUR"
	input.Ledger = ledgerOf(t, sampleFields(), euro, fallbackFields())
	body := commentOf(t, input)
	assertNoAmount(t, body)
	if !strings.Contains(body, "This run was priced at list (Measured). Held by wrapper-watchdog: the wrapper stops the process at the cap.\n") {
		t.Errorf("one basis is not named once:\n%s", body)
	}
}

func TestAccountLinkCarriesInstallation(t *testing.T) {
	for _, input := range []Input{realInput(t), docsInput(t)} {
		body := commentOf(t, input)
		if !strings.Contains(body, "[Your account on costroid.com](https://app.costroid.com/?installation=github%3A"+exampleInstallation+")") {
			t.Errorf("the account link does not carry the installation:\n%s", body)
		}
	}
	input := realInput(t)
	input.AccountHost = AccountGitHub
	if body := commentOf(t, input); !strings.Contains(body, "(https://app.costroid.com/?installation=github%3A"+exampleInstallation+")") {
		t.Errorf("a named GitHub host drops the installation:\n%s", body)
	}
}

func TestUnitPricesReadPerMillionTokens(t *testing.T) {
	body := commentOf(t, docsInput(t))
	if !strings.Contains(body, " per 1,000,000 tokens |") || strings.Contains(body, "000 Tokens") {
		t.Errorf("unit prices are not per 1,000,000 tokens:\n%s", body)
	}
	for unit, want := range map[string]string{"1 Tokens": "1 tokens", "1000 Tokens": "1,000 tokens", "100000 Tokens": "100,000 tokens", "1000000 Tokens": "1,000,000 tokens", "999999999999 Tokens": "999,999,999,999 tokens"} {
		if got := unitWords(unit); got != want {
			t.Errorf("unitWords(%q) = %q, want %q", unit, got, want)
		}
	}
}

func TestGitLabLinkNamesTheNamespaceKind(t *testing.T) {
	input := realInput(t)
	input.Installation, input.AccountHost = "4410287", AccountGitLab
	body := commentOf(t, input)
	if !strings.Contains(body, "(https://app.costroid.com/?installation=gitlab-namespace%3A4410287)") {
		t.Errorf("the link does not name the namespace kind:\n%s", body)
	}
}

func TestAccountLinkPointsAtTheDocsPageWithoutAnInstallation(t *testing.T) {
	line := "[Your account on costroid.com](https://docs.costroid.com/account) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)\n"
	unknownHost := realInput(t)
	unknownHost.AccountHost = "bitbucket"
	none := realInput(t)
	none.Installation = ""
	for name, input := range map[string]Input{"no installation": none, "a host with no kind": unknownHost} {
		body := commentOf(t, input)
		if !strings.Contains(body, line) || strings.Contains(body, "app.costroid.com") {
			t.Errorf("%s: the account link is not the docs page:\n%s", name, body)
		}
	}
}

func TestPlatformURLLinksThePullRequest(t *testing.T) {
	const link = "(https://costroid.example/#/pulls?repo=github.com/example/app&pr=42)"
	pull := func(platform, repository string, number int) Input {
		input := realInput(t)
		input.Platform, input.Repository, input.PullRequest = platform, repository, number
		return input
	}
	for name, input := range map[string]Input{
		"with an installation": pull("https://costroid.example", "github.com/example/app", 42),
		"a trailing slash":     pull("https://costroid.example/", "github.com/example/app", 42),
	} {
		if body := commentOf(t, input); !strings.Contains(body, link) || strings.Contains(body, "installation=") {
			t.Errorf("%s: the link is not the pull request's page:\n%s", name, body)
		}
	}
	installationLink := "(https://app.costroid.com/?installation=github%3A" + exampleInstallation + ")"
	for name, input := range map[string]Input{
		"plain http":          pull("http://costroid.example", "github.com/example/app", 42),
		"a path":              pull("https://costroid.example/app", "github.com/example/app", 42),
		"a query":             pull("https://costroid.example?x=1", "github.com/example/app", 42),
		"a link's end":        pull("https://costroid.example)", "github.com/example/app", 42),
		"a long origin":       pull("https://"+strings.Repeat("a", maxRunURLLength), "github.com/example/app", 42),
		"no repository":       pull("https://costroid.example", "", 42),
		"an escape":           pull("https://costroid.example", "github.com/example/a%26pr%3D1", 42),
		"a second value":      pull("https://costroid.example", "github.com/example/app&pr=1", 42),
		"upper case":          pull("https://costroid.example", "github.com/Example/app", 42),
		"no pull request":     pull("https://costroid.example", "github.com/example/app", 0),
		"a negative number":   pull("https://costroid.example", "github.com/example/app", -42),
		"no platform":         pull("", "github.com/example/app", 42),
		"a repository alone":  pull("https://costroid.example", "github.com", 42),
		"a markdown link end": pull("https://costroid.example", "github.com/example/app)", 42),
	} {
		if body := commentOf(t, input); !strings.Contains(body, installationLink) || strings.Contains(body, "costroid.example") {
			t.Errorf("%s: the link is not the installation's:\n%s", name, body)
		}
	}
}

func TestTwoLinksCarryEqualWeightAndTheAccountLinkNamesTheInstallation(t *testing.T) {
	body := commentOf(t, realInput(t))
	line := regexp.MustCompile(`(?m)^\[[^\]]+\]\(https://app\.costroid\.com/\?installation=github%3A58120394\) · \[[^\]]+\]\(https://docs\.costroid\.com/export\)$`)
	if !line.MatchString(body) {
		t.Errorf("no plain link pair on one line:\n%s", body)
	}
	for _, installation := range []string{"", "12x", "1&evil=2", "<script>"} {
		input := realInput(t)
		input.Installation = installation
		if body := commentOf(t, input); !strings.Contains(body, "(https://docs.costroid.com/account)") {
			t.Errorf("installation %q:\n%s", installation, body)
		}
	}
}

func TestCommentBlockIsTheCheckBlockNamingTheCheckRun(t *testing.T) {
	input := realInput(t)
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.Text, "check_run_id") {
		t.Errorf("the authoritative block names an id it cannot know yet:\n%s", output.Text)
	}
	body := commentOf(t, input)
	idLine := fmt.Sprintf("  \"check_run_id\": %d,\n", exampleCheckRun)
	if !strings.Contains(body, strings.ReplaceAll(idLine, "\n", "")) {
		t.Fatalf("the copy does not name the check run:\n%s", body)
	}
	if !strings.Contains(strings.ReplaceAll(body, idLine, ""), output.Text) {
		t.Errorf("the copy differs from the check's block:\n%s\n%s", body, output.Text)
	}
	input.IDInCheck = true
	named, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, named.Text) {
		t.Errorf("the copy differs from the block of a check that names itself:\n%s\n%s", body, named.Text)
	}
	input.CheckRunID = nil
	if !strings.Contains(commentOf(t, input), "the check run was not posted") {
		t.Error("a comment without a check run does not say so")
	}
}

func TestPullRequestStringsAreLabelledAsDataInTheBlock(t *testing.T) {
	output, err := Check(realInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`"data": \{\s+"agents": \[\s+"claude-code"`).MatchString(output.Text) {
		t.Errorf("no data section:\n%s", output.Text)
	}
}

func assertNoAmount(t *testing.T, text string) {
	t.Helper()
	for _, amount := range []string{realTotal, "USD 5", `"list_cost"`, `"amount":`, "| Cost |"} {
		if strings.Contains(text, amount) {
			t.Errorf("%q is in\n%s", amount, text)
		}
	}
}

func TestShowCostOffWithholdsEveryAmountFromTheCommentAndItsBlockCopy(t *testing.T) {
	for _, amounts := range []Amounts{AmountsOff, AmountsUnsettled} {
		input := realInput(t)
		input.Amounts = amounts
		body := commentOf(t, input)
		assertNoAmount(t, body)
		for _, want := range []string{"**Verdict: neutral.**", "| unattributed |", "agent-flag", `"amounts": "withheld"`, `"attribution_grade": "unattributed"`, `"enforcement_grade": "agent-flag"`} {
			if !strings.Contains(body, want) {
				t.Errorf("no %q in\n%s", want, body)
			}
		}
	}
}

func TestShowCostOffLeavesTheAmountsInTheChecksBlock(t *testing.T) {
	input := realInput(t)
	input.Amounts = AmountsOff
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	assertNoAmount(t, output.Summary)
	for _, want := range []string{`"list_cost": "` + realTotal + `"`, `"amount": "5"`, `"amounts": "shown"`} {
		if !strings.Contains(output.Text, want) {
			t.Errorf("no %q in\n%s", want, output.Text)
		}
	}
}

func TestAnInputThatNeverSetAmountsWithholdsThem(t *testing.T) {
	input := realInput(t)
	input.Amounts = 0
	assertNoAmount(t, commentOf(t, input))
}

func TestConfigReadsShowCost(t *testing.T) {
	cases := []struct {
		name, document string
		want           bool
	}{
		{"an empty file keeps the default", "", true},
		{"another key keeps the default", "policy_pack: default\nbudget_owner: finance\n", true},
		{"a comment block without the key keeps the default", "comment:\n  other: 1\n", true},
		{"true", "comment:\n  show_cost: true\n", true},
		{"false", "comment:\n  show_cost: false\n", false},
		{"flow style", "comment: {show_cost: false}\n", false},
		{"yaml 1.1 no", "comment:\n  show_cost: no\n", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config, err := ParseConfig([]byte(test.document))
			if err != nil {
				t.Fatal(err)
			}
			if config.ShowCost != test.want {
				t.Errorf("ShowCost = %v", config.ShowCost)
			}
		})
	}
}

func TestConfigRefusesAShowCostThatIsNotABoolean(t *testing.T) {
	for _, document := range []string{"comment:\n  show_cost: \"false\"\n", "comment:\n  show_cost: 0\n", "comment: off\n", "comment: [", "- a\n"} {
		if _, err := ParseConfig([]byte(document)); err == nil {
			t.Errorf("%q was read", document)
		}
	}
}

func BenchmarkCommentForARealRowsFile(b *testing.B) {
	input := realInput(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Comment(input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadRowsAndRender(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "rows.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	input := realInput(b)
	b.ReportAllocs()
	for b.Loop() {
		ledger, err := ReadRows(strings.NewReader(string(data)))
		if err != nil {
			b.Fatal(err)
		}
		input.Ledger = ledger
		if _, err := Comment(input); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGitLabCopyNeverNamesACheckRun(t *testing.T) {
	input := realInput(t)
	input.AccountHost, input.CheckRunID = AccountGitLab, nil
	for _, amounts := range []Amounts{AmountsOff, AmountsUnsettled} {
		input.Amounts = amounts
		if body := commentOf(t, input); strings.Contains(body, "check run") {
			t.Fatalf("a GitLab note names a check run:\n%s", body)
		}
	}
}

func TestTheCommentNamesOneRunAtTheShortCommitAndLinksIt(t *testing.T) {
	cases := map[string]string{
		"GitHub":                   exampleRun,
		"GitHub Enterprise Server": "https://github.corp.example/platform/widgets/actions/runs/7",
		"GitLab":                   "https://gitlab.corp.example/platform/widgets/-/pipelines/901",
		"GitLab on a port":         "http://gitlab.internal:8080/group/sub/project/-/pipelines/12",
	}
	for name, link := range cases {
		input := realInput(t)
		input.RunURL = link
		want := "This is one run at commit `9f2c6e1`, not a total across runs. [CI run](" + link + ")"
		if body := visiblePart(commentOf(t, input)); !strings.Contains(body, want) {
			t.Errorf("%s: no %q in\n%s", name, want, body)
		}
	}
	input := realInput(t)
	input.RunURL = ""
	body := commentOf(t, input)
	if !strings.Contains(body, "This is one run at commit `9f2c6e1`, not a total across runs.\n") || strings.Contains(body, "[CI run]") {
		t.Errorf("a run the environment does not name is linked:\n%s", body)
	}
	input.HeadSHA = "not-a-commit"
	if body := commentOf(t, input); !strings.Contains(body, "This is one run, not a total across runs.") || strings.Contains(body, "commit `") {
		t.Errorf("a malformed head is named:\n%s", body)
	}
}

func TestARunLinkOutsideItsFormIsDropped(t *testing.T) {
	for _, link := range []string{
		"javascript:alert(1)",
		"https://github.com/a/b/actions/runs/1) [x](https://evil.example",
		"https://github.com/a/b/actions/runs/1?token=x",
		"https://github.com/a/b/actions/runs/1#x",
		"https://github.com/a b/actions/runs/1",
		"https://user@github.com/a/b/actions/runs/1",
		"https://github.com",
		"ftp://github.com/a/b",
		"https://github.com/" + strings.Repeat("a", maxRunURLLength),
	} {
		input := realInput(t)
		input.RunURL = link
		if body := commentOf(t, input); strings.Contains(body, "[CI run]") {
			t.Errorf("%q was linked:\n%s", link, visiblePart(body))
		}
	}
}

func TestAFallbackPricedFigureIsMarkedAndExplainedInTheOpen(t *testing.T) {
	ledger := ledgerOf(t, fallbackFields(), sampleFields())
	if len(ledger.Figures) != 1 || ledger.Figures[0].FallbackRows != 1 {
		t.Fatalf("figures = %+v", ledger.Figures)
	}
	sentence := "Fallback price: `gpt-9-preview` is not in catalog `0.1.0` for the tier and token class used, so `openai:fallback:output` priced it."
	marks := map[Amounts]string{AmountsShown: "| USD 180.1, fallback price |", AmountsOff: "| Measured, fallback price |"}
	for amounts, mark := range marks {
		input := Input{Ledger: ledger, State: Reported, HeadSHA: sampleHead, Amounts: amounts}
		output, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		for where, text := range map[string]string{"comment": visiblePart(commentOf(t, input)), "summary": output.Summary} {
			if !strings.Contains(text, mark) || !strings.Contains(text, sentence) {
				t.Errorf("%d: the %s has no mark or no sentence:\n%s", amounts, where, text)
			}
		}
		for _, want := range []string{`"fallback_rows": 1`, `"fallback_models": [` + "\n" + `      "gpt-9-preview"`, `"fallback_price_ids": [` + "\n" + `      "openai:fallback:output"`} {
			if !strings.Contains(output.Text, want) {
				t.Errorf("no %q in\n%s", want, output.Text)
			}
		}
	}
	plain := commentOf(t, Input{Ledger: ledgerOf(t, sampleFields()), State: Reported, Amounts: AmountsShown})
	if strings.Contains(plain, "allback") && !strings.Contains(plain, `"fallback_rows": 0`) {
		t.Errorf("a figure the catalog priced reads as fallback:\n%s", plain)
	}
	if strings.Contains(visiblePart(plain), "allback") {
		t.Errorf("a figure the catalog priced reads as fallback:\n%s", visiblePart(plain))
	}
}

func TestTheFallbackSentenceCountsEveryModel(t *testing.T) {
	rows := []rowFields{}
	for index := range maxNames + 2 {
		fields := fallbackFields()
		fields.model = fmt.Sprintf("model-%02d", index)
		rows = append(rows, fields)
	}
	body := visiblePart(commentOf(t, Input{Ledger: ledgerOf(t, rows...), State: Reported, Amounts: AmountsShown}))
	if !strings.Contains(body, "`model-09` and 2 more are not in catalog `0.1.0`") || !strings.Contains(body, "priced them.") {
		t.Errorf("the sentence drops a count:\n%s", body)
	}
}

var traceRow = regexp.MustCompile("(?m)^\\| `[^`]+` \\| `([a-z0-9_]+)` \\| `[^`]+` \\| (.+?) \\| ([0-9.]+) \\| ([A-Z]{3}) ([0-9.]+) \\|$")

// TestTheTraceRecomputesTheFigureByHand redoes the reader's arithmetic from
// the rendered text alone: each line's price times its tokens over the unit,
// and the lines' sum, against the figure the table shows.
func TestTheTraceRecomputesTheFigureByHand(t *testing.T) {
	body := commentOf(t, realInput(t))
	lines := traceRow.FindAllStringSubmatch(body, -1)
	if len(lines) != 7 {
		t.Fatalf("%d trace lines in\n%s", len(lines), body)
	}
	unit := regexp.MustCompile(`^USD ([0-9.]+) per ([0-9]{1,3}(?:,[0-9]{3})*) tokens$`)
	var total decimal.Decimal
	for _, line := range lines {
		class, price, tokens, amount := line[1], line[2], decimal.MustParse(line[3]), decimal.MustParse(line[5])
		if focus.TokenClass(class).Informational() {
			if price != "informational, not priced" || amount.Sign() != 0 {
				t.Errorf("an informational line reads %q", line[0])
			}
			continue
		}
		parts := unit.FindStringSubmatch(price)
		if parts == nil {
			t.Fatalf("unit price %q", price)
		}
		size := decimal.MustParse(strings.ReplaceAll(parts[2], ",", ""))
		if got := decimal.MustParse(parts[1]).Mul(tokens); got.Cmp(amount.Mul(size)) != 0 {
			t.Errorf("%s: %s times %s over %s is not %s", class, parts[1], tokens, size, amount)
		}
		total = total.Add(amount)
	}
	if total.String() != realTotal || !strings.Contains(body, "<summary>How USD "+realTotal+" was computed</summary>") {
		t.Errorf("the trace sums to %s", total)
	}
}

func TestAWithheldTraceShowsClassesAndSaysWhereTheCountsAre(t *testing.T) {
	sentences := map[string]string{
		AccountGitHub: "Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts.",
		AccountGitLab: "Token counts are withheld with the amounts. The rows carry them.",
	}
	for host, sentence := range sentences {
		for _, amounts := range []Amounts{AmountsOff, AmountsUnsettled} {
			input := realInput(t)
			input.Amounts, input.AccountHost = amounts, host
			output, err := Check(input)
			if err != nil {
				t.Fatal(err)
			}
			for where, text := range map[string]string{"comment": commentOf(t, input), "summary": output.Summary} {
				assertNoAmount(t, text)
				if found := regexp.MustCompile(`[A-Z]{3} [0-9]`).FindString(text); found != "" || strings.Contains(text, " per ") || strings.Contains(text, "| Tokens |") {
					t.Errorf("%s %s: a price, an amount or a count %q in\n%s", host, where, found, text)
				}
				for _, want := range []string{"<summary>Token classes behind line 1</summary>", "| `0.3.0` | `cache_read` |\n", sentence} {
					if !strings.Contains(text, want) {
						t.Errorf("%s %s: no %q in\n%s", host, where, want, text)
					}
				}
			}
		}
	}
}

// TestAWithheldCommentHoldsNoNumberAReaderCouldRecomputeFrom cuts a withheld
// comment for the recorded run into words, and refuses any word that is one of
// its token counts, unit prices or amounts, the budget included. A version
// such as 0.1.0 or a model such as claude-haiku-5-5 is one word, not a number.
func TestAWithheldCommentHoldsNoNumberAReaderCouldRecomputeFrom(t *testing.T) {
	input := realInput(t)
	secret := map[string]bool{input.Budget.String(): true}
	for _, figure := range input.Ledger.Figures {
		secret[figure.Cost.String()] = true
		for _, trace := range figure.Trace {
			secret[trace.Tokens.String()] = true
			secret[trace.Cost.String()] = true
			secret[trace.UnitPrice.String()] = true
		}
	}
	delete(secret, "0")
	if len(secret) < 12 {
		t.Fatalf("only %d numbers to hide: %v", len(secret), secret)
	}
	word := regexp.MustCompile(`[A-Za-z0-9._:/+-]+`)
	number := regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	for _, amounts := range []Amounts{AmountsOff, AmountsUnsettled} {
		input.Amounts = amounts
		output, err := Check(input)
		if err != nil {
			t.Fatal(err)
		}
		for where, text := range map[string]string{"comment": commentOf(t, input), "summary": output.Summary} {
			for _, found := range word.FindAllString(text, -1) {
				found = strings.TrimRight(found, ".")
				if number.MatchString(found) && secret[found] {
					t.Errorf("%d: the %s holds %s", amounts, where, found)
				}
			}
		}
	}
}

// TestTracesStayInsideTheirCapsAndSayWhatTheyLeaveOut fills every cap at once:
// twenty figures, each string at its length cap and every amount near its own.
func TestTracesStayInsideTheirCapsAndSayWhatTheyLeaveOut(t *testing.T) {
	currencies := []string{"AAA", "BBB", "CCC", "DDD"}
	catalog := strings.Repeat("c", maxDataLength)
	lines := make([]string, 0, 2000)
	for index := range 2000 {
		fields := fallbackFields()
		fields.currency = currencies[index%len(currencies)]
		fields.enforcement = enforcementOrder[index/len(currencies)%len(enforcementOrder)]
		fields.model = strings.Repeat("m", maxDataLength-8) + fmt.Sprintf("%08d", index)
		fields.priceID = strings.Repeat("p", maxDataLength-8) + fmt.Sprintf("%08d", index)
		fields.cost = strings.Repeat("9", 40) + ".000000000001"
		lines = append(lines, strings.Replace(fields.line(), `"0.1.0"`, `"`+catalog+`"`, 1))
	}
	ledger, err := ReadRows(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Figures) != maxFigures || ledger.FallbackModelsOmitted == 0 || ledger.FallbackPricesOmitted == 0 {
		t.Fatalf("%d figures, %d models and %d prices left out", len(ledger.Figures), ledger.FallbackModelsOmitted, ledger.FallbackPricesOmitted)
	}
	traced := 0
	for _, figure := range ledger.Figures {
		if len(figure.Trace) > maxTraceLines {
			t.Errorf("a trace holds %d lines", len(figure.Trace))
		}
		traced += len(figure.Trace)
	}
	if traced != maxTraceTotal {
		t.Errorf("the traces hold %d lines", traced)
	}
	input := Input{Ledger: ledger, State: Reported, HeadSHA: sampleHead, Amounts: AmountsShown, RunURL: exampleRun}
	body := commentOf(t, input)
	if !strings.Contains(body, "92 more lines are left out of this trace.") || !strings.Contains(body, "100 more lines are left out of this trace.") {
		t.Errorf("the cut is not stated:\n%s", body)
	}
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("comment %d bytes, summary %d, text %d", len(body), len(output.Summary), len(output.Text))
	if len(body) > githubLimit || len(output.Summary) > githubLimit || len(output.Text) > githubLimit {
		t.Errorf("comment %d bytes, summary %d, text %d", len(body), len(output.Summary), len(output.Text))
	}
}

package comment

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	realTotal  = "0.001619725"
	sampleHead = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	// exampleCheckRun and exampleInstallation are the synthetic ids in
	// docs/comment-example.md.
	exampleCheckRun     = int64(16873409921)
	exampleInstallation = "58120394"
	// githubLimit is the longest comment or check text GitHub takes.
	githubLimit = 65535
)

var update = flag.Bool("update", false, "rewrite docs/comment-example.md")

type rowFields struct {
	currency, cost, basis, attribution, enforcement, agent, model string
}

func sampleFields() rowFields {
	return rowFields{"USD", "0.1", "measured", "exact", "wrapper-watchdog", "codex", "gpt-5.5"}
}

func (f rowFields) line() string {
	return fmt.Sprintf(`{"BillingCurrency":%q,"ListCost":%q,"x_CostroidCatalogVersion":"0.1.0","Tags":{"costroid/cost_basis":%q,"costroid/attribution_grade":%q,"costroid/enforcement_grade":%q,"costroid/agent":%q,"costroid/model":%q,"team":7}}`,
		f.currency, f.cost, f.basis, f.attribution, f.enforcement, f.agent, f.model)
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

// artifactLabel is the sentence the brief's rule asks for.
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

func visiblePart(body string) string {
	head, _, _ := strings.Cut(body, "<details>")
	return head
}

func TestDocsExampleIsTheRenderedCommentForARealRowsFile(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "comment-example.md")
	body := commentOf(t, realInput(t))
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
	if strings.Contains(body, "0.4") {
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
	for _, want := range []string{"USD " + realTotal, "Budget: USD 5."} {
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
		"cost as a JSON number": strings.Replace(sampleFields().line(), `"ListCost":"0.1"`, `"ListCost":0.1`, 1),
		"negative cost":         strings.Replace(sampleFields().line(), `"0.1"`, `"-0.1"`, 1),
		"exponent":              strings.Replace(sampleFields().line(), `"0.1"`, `"1e3"`, 1),
		"past the scale limit":  strings.Replace(sampleFields().line(), `"0.1"`, `"0.1234567890123"`, 1),
		"very long cost":        strings.Replace(sampleFields().line(), `"0.1"`, `"`+strings.Repeat("9", 70)+`"`, 1),
		"not a currency":        strings.Replace(sampleFields().line(), `"USD"`, `"usd"`, 1),
		"unknown basis":         strings.Replace(sampleFields().line(), `"measured"`, `"guessed"`, 1),
		"unknown grade":         strings.Replace(sampleFields().line(), `"exact"`, `"perfect"`, 1),
		"unknown enforcement":   strings.Replace(sampleFields().line(), `"wrapper-watchdog"`, `"gateway-precheck-plus"`, 1),
		"no grade":              strings.Replace(sampleFields().line(), `"costroid/attribution_grade"`, `"other"`, 1),
		"not JSON":              "{",
		"a row past the cap":    `{"x":"` + strings.Repeat("a", maxRowBytes) + `"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadRows(strings.NewReader(sampleFields().line() + "\n" + line)); err == nil {
				t.Error("the file was read")
			}
		})
	}
}

func TestNoRowsAndUnreadableRowsShowNoFigure(t *testing.T) {
	cases := map[State]string{
		NoRows:     "No cost rows were recorded for this commit.",
		Unreadable: "The rows file could not be read, so this report shows no figures.",
	}
	for state, sentence := range cases {
		body := commentOf(t, Input{State: state, Amounts: AmountsShown})
		if !strings.Contains(body, sentence) {
			t.Errorf("%s: no sentence in\n%s", state, body)
		}
		if strings.Contains(body, "| Cost |") || strings.Contains(body, "USD") {
			t.Errorf("%s shows a figure:\n%s", state, body)
		}
		if !strings.Contains(body, `"state": "`+string(state)+`"`) {
			t.Errorf("%s: the block does not name the state", state)
		}
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

func TestReportsWithNoFiguresCarryNoLabel(t *testing.T) {
	for _, state := range []State{NoRows, Unreadable} {
		if body := commentOf(t, Input{State: state, Amounts: AmountsShown}); strings.Contains(body, artifactLabel) {
			t.Errorf("%s labels a figure it does not show", state)
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
			fields := sampleFields()
			fields.model = hostile
			fields.agent = hostile
			clean := sampleFields()
			ledger, err := ReadRows(strings.NewReader(strings.ReplaceAll(jsonLine(t, fields), "\n", "") + "\n" + clean.line()))
			if err != nil {
				t.Fatal(err)
			}
			input := Input{Ledger: ledger, State: Reported, HeadSHA: sampleHead, Amounts: AmountsShown}
			output, err := Check(input)
			if err != nil {
				t.Fatal(err)
			}
			for where, text := range map[string]string{"comment": commentOf(t, input), "summary": output.Summary, "text": output.Text} {
				assertInert(t, where, text, hostile)
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
	line := fields
	line.model = "MODEL"
	line.agent = "AGENT"
	return strings.NewReplacer(`"MODEL"`, string(model), `"AGENT"`, string(agent)).Replace(line.line())
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

func TestVisiblePartReadsInTenSeconds(t *testing.T) {
	const wordsInTenSeconds = 60
	table := strings.NewReplacer("|", " ", "---", " ")
	words := strings.Fields(table.Replace(visiblePart(commentOf(t, realInput(t)))))
	t.Logf("%d words", len(words))
	if len(words) > wordsInTenSeconds {
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

func TestAccountLinkNamesTheGitLabProject(t *testing.T) {
	input := realInput(t)
	input.Installation, input.AccountHost = "4242", AccountGitLab
	body := commentOf(t, input)
	if !strings.Contains(body, "(https://app.costroid.com/?installation=gitlab%3A4242)") {
		t.Errorf("no link in\n%s", body)
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
		if body := commentOf(t, input); !strings.Contains(body, "(https://app.costroid.com/)") {
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

package recipelint

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	customerDir        = "testdata/customer"
	existingWorkflowMD = "../../docs/existing-workflow.md"
	lintDoc            = "../../docs/lint.md"
)

// recipeOnlyRules hold Costroid's own recipes to its own choices. None says
// who can start a paid run, so the customer profile never names one.
var recipeOnlyRules = []string{
	RuleUnrecordedPin, RuleIDTokenScope, RulePermissions, RuleAgentPermissions,
	RuleAgentGuard, RuleAgentVersion, RuleAgentContentFlags, RuleAgentProvider, RuleAgentTools, RuleAgentNoteToken,
	RuleReportJob, RuleFollowUpCode, RuleFollowUpArtifact,
}

// lintCustomerFile lints one file under the customer profile and lists every
// finding and note as "severity rule", sorted.
func lintCustomerFile(t *testing.T, name string) (Report, []string) {
	t.Helper()
	report, err := LintWith([]string{filepath.Join(customerDir, name)}, Options{Profile: Customer})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, finding := range slices.Concat(report.Findings, report.Notes) {
		got = append(got, string(finding.Severity)+" "+finding.Rule)
	}
	slices.Sort(got)
	return report, got
}

func TestCustomerWorkflows(t *testing.T) {
	want := map[string][]string{
		"clean.yml":     nil,
		"two-block.yml": nil,
		"high-pull-request-target.yml": {
			"high " + RulePullRequestTarget,
			"high " + RulePullRequestTarget,
			"note " + RuleAgentBudget,
		},
		"high-comment-trigger.yml": {"high " + RuleCommentTrigger},
		"high-agent-open.yml":      {"high " + RuleAgentOpen, "note " + RuleAgentBudget},
		"high-id-token.yml":        {"high " + RuleAgentIDToken, "note " + RuleAgentBudget},
		"high-expression.yml":      {"high " + RuleExpressionInScript},
		"medium.yml": {
			"medium " + RuleAgentOpen,
			"medium " + RuleAgentOpen,
			"medium " + RuleUnpinned,
		},
		"mention.yml": {
			"high " + RuleAgentIDToken,
			"high " + RulePullRequestTarget,
			"medium " + RuleUnpinned,
			"medium " + RuleUnpinned,
			"note " + RuleSubscriptionToken,
		},
	}
	files, err := filepath.Glob(filepath.Join(customerDir, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Fatalf("%d files under %s, %d expected: give each file its findings here", len(files), customerDir, len(want))
	}
	for _, file := range files {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			expected, listed := want[name]
			if !listed {
				t.Fatal("the file has no expected findings")
			}
			report, got := lintCustomerFile(t, name)
			if !slices.Equal(got, expected) {
				t.Fatalf("got %v, want %v\n%v", got, expected, slices.Concat(report.Findings, report.Notes))
			}
			if report.Agents != 1 {
				t.Fatalf("judged %d workflows, want 1", report.Agents)
			}
		})
	}
}

// TestTwoBlockExampleIsClean holds docs/existing-workflow.md's example to the
// test file, byte for byte, and the test file to no finding at all.
func TestTwoBlockExampleIsClean(t *testing.T) {
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(read(t, existingWorkflowMD), -1)
	if len(blocks) != 1 {
		t.Fatalf("the page holds %d YAML blocks, want 1", len(blocks))
	}
	if blocks[0][1] != read(t, filepath.Join(customerDir, "two-block.yml")) {
		t.Fatal("the page's example and testdata/customer/two-block.yml differ")
	}
	report, got := lintCustomerFile(t, "two-block.yml")
	if len(got) != 0 {
		t.Fatalf("findings: %v", slices.Concat(report.Findings, report.Notes))
	}
	for _, block := range []string{"stage: read", "stage: report", "needs: review", "id: claude", "steps.claude.outputs.execution_file", "claude_args: --max-budget-usd 5"} {
		if !strings.Contains(blocks[0][1], block) {
			t.Errorf("the example lacks %q", block)
		}
	}
}

// TestMentionWorkflowGivesOnlyItsHighFindings lints the common @claude mention
// workflow. The recipe profile rejects it for rules that only Costroid's own
// recipes keep. The customer profile names its two high findings and none of
// those: claude-code-action's own check guards the comments, until an input
// opens it to anyone.
func TestMentionWorkflowGivesOnlyItsHighFindings(t *testing.T) {
	source := read(t, filepath.Join(customerDir, "mention.yml"))
	high := func(report Report) []string {
		var rules []string
		for _, finding := range report.Findings {
			if finding.Severity == High {
				rules = append(rules, finding.Rule)
			}
		}
		slices.Sort(rules)
		return rules
	}
	report, _ := lintCustomerFile(t, "mention.yml")
	if got, want := high(report), []string{RuleAgentIDToken, RulePullRequestTarget}; !slices.Equal(got, want) {
		t.Fatalf("high: %v, want %v", got, want)
	}
	for _, finding := range slices.Concat(report.Findings, report.Notes) {
		if slices.Contains(recipeOnlyRules, finding.Rule) {
			t.Errorf("a recipe-only rule: %v", finding)
		}
	}
	const token = "          claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}\n"
	opened := strings.Replace(source, token, token+"          allowed_non_write_users: \"*\"\n", 1)
	if opened == source {
		t.Fatal("the edit changed nothing")
	}
	got, want := high(LintCustomer("mention.yml", []byte(opened))), []string{RuleAgentIDToken, RuleAgentOpen, RuleCommentTrigger, RulePullRequestTarget}
	if !slices.Equal(got, want) {
		t.Fatalf("opened to anyone, high: %v, want %v", got, want)
	}
	strict := LintBytes("mention.yml", []byte(source))
	for _, rule := range []string{RuleAgentVersion, RuleAgentContentFlags, RuleAgentGuard} {
		if !slices.Contains(rules(strict.Findings), rule) {
			t.Errorf("the recipe profile does not name %s, so this test proves nothing about it", rule)
		}
	}
}

// TestClaudeActionInputsOnACommentEvent puts clean.yml's step on
// issue_comment and sets each opening input of claude-code-action. Only both
// inputs empty leaves the action's own check standing.
func TestClaudeActionInputsOnACommentEvent(t *testing.T) {
	source := strings.Replace(read(t, filepath.Join(customerDir, "clean.yml")), "  pull_request:\n", "  issue_comment:\n", 1)
	const prompt = "          prompt: Review this pull request.\n"
	for _, test := range []struct {
		input string
		want  []string
	}{
		{"", nil},
		{`allowed_bots: ""`, nil},
		{`allowed_non_write_users: ""`, nil},
		{`allowed_non_write_users: "*"`, []string{"high " + RuleAgentOpen, "high " + RuleCommentTrigger}},
		{`allowed_bots: "*"`, []string{"high " + RuleAgentOpen, "high " + RuleCommentTrigger}},
		{`allowed_non_write_users: "octocat"`, []string{"high " + RuleCommentTrigger, "medium " + RuleAgentOpen}},
		{`allowed_bots: "dependabot[bot]"`, []string{"high " + RuleCommentTrigger, "medium " + RuleAgentOpen}},
	} {
		t.Run(test.input, func(t *testing.T) {
			edited := source
			if test.input != "" {
				edited = strings.Replace(source, prompt, prompt+"          "+test.input+"\n", 1)
			}
			if !strings.Contains(edited, "issue_comment") || (test.input != "" && edited == source) {
				t.Fatal("the edit changed nothing")
			}
			var got []string
			for _, finding := range LintCustomer("x.yml", []byte(edited)).Findings {
				got = append(got, string(finding.Severity)+" "+finding.Rule)
			}
			slices.Sort(got)
			if !slices.Equal(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestCustomerProfileSkipsAWorkflowWithNoAgent(t *testing.T) {
	const build = "on: pull_request_target\njobs:\n  build:\n    runs-on: ubuntu-24.04\n    steps:\n      - uses: actions/checkout@v4\n      - run: make ${{ github.head_ref }}\n"
	report := LintCustomer("build.yml", []byte(build))
	if report.Agents != 0 || len(report.Findings) != 0 || len(report.Notes) != 0 {
		t.Fatalf("report: %+v", report)
	}
}

func TestCustomerUnreadableFileIsHigh(t *testing.T) {
	report := LintCustomer("broken.yml", []byte("on: [1\n"))
	if len(report.Findings) != 1 || report.Findings[0].Severity != High || report.Findings[0].Rule != RuleUnreadable {
		t.Fatalf("findings: %v", report.Findings)
	}
}

// TestCustomerCommentGuards edits the comment-trigger file's if, so each test
// the rule accepts is shown to clear it.
func TestCustomerCommentGuards(t *testing.T) {
	source := read(t, filepath.Join(customerDir, "high-comment-trigger.yml"))
	const guard = "contains(github.event.comment.body, '@claude')"
	for name, replacement := range map[string]string{
		"association": guard + " && contains(fromJSON('[\"OWNER\",\"MEMBER\"]'), github.event.comment.author_association)",
		"actor":       guard + " && github.actor == 'octocat'",
	} {
		t.Run(name, func(t *testing.T) {
			edited := strings.Replace(source, guard, replacement, 1)
			if edited == source {
				t.Fatal("the edit changed nothing")
			}
			if got := rules(LintCustomer("x.yml", []byte(edited)).Findings); len(got) != 0 {
				t.Fatalf("findings: %v", got)
			}
		})
	}
}

func TestCustomerNotesContentsWrite(t *testing.T) {
	source := read(t, filepath.Join(customerDir, "clean.yml"))
	const granted = "    permissions:\n      contents: read\n      pull-requests: write\n"
	for _, change := range []edit{
		{"write", granted, "    permissions:\n      contents: write\n", RuleAgentContentWrite},
		{"write-all", granted, "    permissions: write-all\n", RuleAgentContentWrite},
		{"no permissions", "permissions: {}\n", "", RuleAgentContentWrite},
	} {
		t.Run(change.name, func(t *testing.T) {
			edited := strings.Replace(source, change.from, change.to, 1)
			if change.name == "no permissions" {
				edited = strings.Replace(edited, granted, "", 1)
			}
			if !strings.Contains(source, change.from) || edited == source {
				t.Fatal("the edit changed nothing")
			}
			if got := rules(LintCustomer("x.yml", []byte(edited)).Notes); !slices.Contains(got, change.rule) {
				t.Fatalf("notes: %v", got)
			}
		})
	}
}

// TestLintDocListsEveryCustomerRule holds docs/lint.md's table to the rules
// and severities the fixtures produce.
func TestLintDocListsEveryCustomerRule(t *testing.T) {
	doc := read(t, lintDoc)
	files, err := filepath.Glob(filepath.Join(customerDir, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{"high " + RuleUnreadable: true}
	for _, file := range files {
		_, got := lintCustomerFile(t, filepath.Base(file))
		for _, line := range got {
			seen[line] = true
		}
	}
	seen["note "+RuleAgentContentWrite] = true
	seen["note "+RuleEgressAudit] = true
	for line := range seen {
		severity, rule, _ := strings.Cut(line, " ")
		row := regexp.MustCompile(`(?m)^\| ` + severity + ` +\| ` + "`" + regexp.QuoteMeta(rule) + "`")
		if !row.MatchString(doc) {
			t.Errorf("docs/lint.md has no %s row for %s", severity, rule)
		}
	}
}

// TestCodeActionStepWithoutBudgetWarns takes clean.yml's cap away in each
// way a workflow can lose it, and keeps it in each way it can be written.
func TestCodeActionStepWithoutBudgetWarns(t *testing.T) {
	source := read(t, filepath.Join(customerDir, "clean.yml"))
	const capped = "          claude_args: --max-budget-usd 5\n"
	const fix = "add --max-budget-usd and your cap in USD to claude_args, such as --max-budget-usd 5"
	for _, test := range []struct {
		name, args string
		warns      bool
	}{
		{"no claude_args", "", true},
		{"other flags", "          claude_args: --max-turns 10\n", true},
		{"a commented flag", "          claude_args: |\n            # --max-budget-usd 5\n            --max-turns 10\n", true},
		{"a longer flag name", "          claude_args: --max-budget-usd-total 5\n", true},
		{"the flag", capped, false},
		{"the flag with =", "          claude_args: --max-budget-usd=5\n", false},
		{"the flag on its own line", "          claude_args: |\n            --max-turns 10\n            --max-budget-usd ${{ env.COSTROID_BUDGET_USD }}\n", false},
		{"one expression", "          claude_args: ${{ inputs.claude_args }}\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			edited := strings.Replace(source, capped, test.args, 1)
			if !strings.Contains(source, capped) || (test.args != capped && edited == source) {
				t.Fatal("the edit changed nothing")
			}
			report := LintCustomer("x.yml", []byte(edited))
			var notes []Finding
			for _, note := range report.Notes {
				if note.Rule == RuleAgentBudget {
					notes = append(notes, note)
				}
			}
			switch {
			case !test.warns && len(notes) != 0:
				t.Fatalf("notes: %v", notes)
			case test.warns && (len(notes) != 1 || notes[0].Severity != Note || !strings.HasSuffix(notes[0].Message, ": "+fix)):
				t.Fatalf("notes: %v", notes)
			}
			if len(report.Findings) != 0 {
				t.Fatalf("a missing cap fails the command: %v", report.Findings)
			}
		})
	}
}

// TestPullRequestTargetNamesTheBlockDate holds the event's finding to the
// dates of GitHub's change, as its changelog of 2026-11-02 states them.
func TestPullRequestTargetNamesTheBlockDate(t *testing.T) {
	report, _ := lintCustomerFile(t, "high-pull-request-target.yml")
	var messages []string
	for _, finding := range report.Findings {
		if finding.Rule == RulePullRequestTarget && strings.HasPrefix(finding.Message, "pull_request_target runs the agent") {
			messages = append(messages, finding.Message)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("event findings: %q", messages)
	}
	for _, part := range []string{"blocks it by default in public repositories from 2026-12-02", "brownouts on 2026-11-09 and 2026-11-16, 16:00 to 18:00 UTC", ": use pull_request"} {
		if !strings.Contains(messages[0], part) {
			t.Errorf("the finding lacks %q: %s", part, messages[0])
		}
	}
	if doc := read(t, lintDoc); !strings.Contains(doc, "2026-12-02") || !strings.Contains(doc, "2026-11-09 and 2026-11-16, 16:00 to 18:00 UTC") {
		t.Error("docs/lint.md does not name the dates")
	}
}

// TestCustomerNotesEgressGaps puts harden-runner in clean.yml's agent job in
// each way that leaves its egress open or broken, and in block mode with a
// list, which says nothing.
func TestCustomerNotesEgressGaps(t *testing.T) {
	source := read(t, filepath.Join(customerDir, "clean.yml"))
	const steps = "    steps:\n"
	const harden = "      - uses: step-security/harden-runner@46b06cea07f9e64621fdf066ba77ac6915163d43 # v2.22.3\n        with:\n"
	for _, test := range []struct {
		name, with string
		notes      int
	}{
		{"audit", "          egress-policy: audit\n", 1},
		{"block with no list", "          egress-policy: block\n", 1},
		{"a list with a line break", "          egress-policy: block\n          allowed-endpoints: |\n            api.anthropic.com:443\n            github.com:443\n", 1},
		{"block with a list", "          egress-policy: block\n          allowed-endpoints: >-\n            api.anthropic.com:443\n            github.com:443\n", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			edited := strings.Replace(source, steps, steps+harden+test.with, 1)
			if edited == source {
				t.Fatal("the edit changed nothing")
			}
			report := LintCustomer("x.yml", []byte(edited))
			if got := rules(report.Notes); len(got) != test.notes || (test.notes == 1 && got[0] != RuleEgressAudit) {
				t.Fatalf("notes: %v", report.Notes)
			}
			if len(report.Findings) != 0 {
				t.Fatalf("an egress gap fails the command: %v", report.Findings)
			}
		})
	}
}

func TestCustomerFindingPrintsItsSeverity(t *testing.T) {
	finding := Finding{Path: "w.yml", Line: 3, Severity: High, Rule: RuleCommentTrigger, Message: "m"}
	if got := finding.String(); got != "w.yml:3: high: comment-trigger: m" {
		t.Fatal(got)
	}
}

// TestShippedRecipesPassTheCustomerProfile keeps a team that copies a recipe
// free of findings under costroid lint --strict.
func TestShippedRecipesPassTheCustomerProfile(t *testing.T) {
	report, err := LintWith([]string{recipesDir}, Options{Profile: Customer})
	if err != nil {
		t.Fatal(err)
	}
	if report.Agents == 0 {
		t.Fatal("judged no recipe")
	}
	if found := slices.Concat(report.Findings, report.Notes); len(found) != 0 {
		t.Fatalf("findings: %v", found)
	}
}

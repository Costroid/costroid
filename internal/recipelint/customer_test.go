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
	RuleAgentGuard, RuleAgentVersion, RuleAgentContentFlags, RuleAgentNoteToken,
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
		},
		"high-comment-trigger.yml": {"high " + RuleCommentTrigger},
		"high-id-token.yml":        {"high " + RuleAgentIDToken},
		"high-expression.yml":      {"high " + RuleExpressionInScript},
		"medium.yml": {
			"medium " + RuleAgentPromptInput,
			"medium " + RuleAgentPromptInput,
			"medium " + RuleUnpinned,
		},
		"mention.yml": {
			"high " + RuleAgentIDToken,
			"high " + RuleCommentTrigger,
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
	for _, block := range []string{"stage: read", "stage: report", "needs: review", "id: claude", "steps.claude.outputs.execution_file"} {
		if !strings.Contains(blocks[0][1], block) {
			t.Errorf("the example lacks %q", block)
		}
	}
}

// TestMentionWorkflowGivesOnlyItsHighFindings lints the common @claude mention
// workflow. The recipe profile rejects it for rules that only Costroid's own
// recipes keep; the customer profile names its three high findings and none
// of those.
func TestMentionWorkflowGivesOnlyItsHighFindings(t *testing.T) {
	report, _ := lintCustomerFile(t, "mention.yml")
	var high []string
	for _, finding := range report.Findings {
		if finding.Severity == High {
			high = append(high, finding.Rule)
		}
	}
	slices.Sort(high)
	if want := []string{RuleAgentIDToken, RuleCommentTrigger, RulePullRequestTarget}; !slices.Equal(high, want) {
		t.Fatalf("high: %v, want %v", high, want)
	}
	for _, finding := range slices.Concat(report.Findings, report.Notes) {
		if slices.Contains(recipeOnlyRules, finding.Rule) {
			t.Errorf("a recipe-only rule: %v", finding)
		}
	}
	strict := LintBytes("mention.yml", []byte(read(t, filepath.Join(customerDir, "mention.yml"))))
	for _, rule := range []string{RuleAgentVersion, RuleAgentContentFlags, RuleAgentGuard} {
		if !slices.Contains(rules(strict.Findings), rule) {
			t.Errorf("the recipe profile does not name %s, so this test proves nothing about it", rule)
		}
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
	for line := range seen {
		severity, rule, _ := strings.Cut(line, " ")
		row := regexp.MustCompile(`(?m)^\| ` + severity + ` +\| ` + "`" + regexp.QuoteMeta(rule) + "`")
		if !row.MatchString(doc) {
			t.Errorf("docs/lint.md has no %s row for %s", severity, rule)
		}
	}
}

func TestCustomerFindingPrintsItsSeverity(t *testing.T) {
	finding := Finding{Path: "w.yml", Line: 3, Severity: High, Rule: RuleCommentTrigger, Message: "m"}
	if got := finding.String(); got != "w.yml:3: high: comment-trigger: m" {
		t.Fatal(got)
	}
}

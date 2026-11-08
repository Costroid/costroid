package recipelint

import (
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	followUpRecipe = "github-follow-up.yml"
	postStep       = "      - name: Post the check\n"
	downloadPath   = "          path: ${{ runner.temp }}/costroid-follow-up\n"
	rowsCommand    = `costroid follow-up --rows "$RUNNER_TEMP/costroid-follow-up/costroid-rows.jsonl"`
)

func TestForkFollowUpShippedRecipePasses(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, followUpRecipe))
	if !strings.Contains(source, "  workflow_run:\n") {
		t.Fatal("the follow-up does not run on workflow_run")
	}
	if report := LintBytes(followUpRecipe, []byte(source)); len(report.Findings) != 0 {
		t.Fatalf("findings: %v", report.Findings)
	}
}

// step is a step inserted before the one that posts the check.
func step(body string) edit {
	return edit{from: postStep, to: body + postStep}
}

func TestForkFollowUpRefusesACheckoutOfTheHead(t *testing.T) {
	const checkout = "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n        with:\n          persist-credentials: false\n"
	edits := map[string]edit{
		"the head commit":       step(checkout + "          ref: ${{ github.event.workflow_run.head_sha }}\n"),
		"the head branch":       step(checkout + "          ref: ${{ github.event.workflow_run.head_branch }}\n"),
		"the fork":              step(checkout + "          repository: ${{ github.event.workflow_run.head_repository.full_name }}\n"),
		"the default branch":    step(checkout),
		"a fetch by variable":   step("      - env:\n          HEAD: ${{ github.event.workflow_run.head_sha }}\n        run: git fetch origin \"$HEAD\"\n"),
		"a fetch of the number": step("      - run: gh pr checkout 7\n"),
		"the job's env":         {from: "    timeout-minutes: 5\n", to: "    timeout-minutes: 5\n    env:\n      HEAD: ${{ github.event.workflow_run.head_sha }}\n"},
		"an agent":              step("      - run: claude -p hello\n"),
	}
	for name, change := range edits {
		t.Run(name, func(t *testing.T) {
			change.rule = RuleFollowUpCode
			expectRule(t, read(t, filepath.Join(recipesDir, followUpRecipe)), change)
		})
	}
}

func TestForkFollowUpRefusesAStepFromTheArtifact(t *testing.T) {
	edits := map[string]edit{
		"a script":             step("      - run: bash \"$RUNNER_TEMP/costroid-follow-up/post.sh\"\n"),
		"a second command":     {from: rowsCommand, to: rowsCommand + "; sh x"},
		"an action on disk":    step("      - uses: ./costroid-follow-up/action\n"),
		"a path in the env":    step("      - env:\n          SCRIPT: ${{ runner.temp }}/costroid-follow-up/post.sh\n        run: bash \"$SCRIPT\"\n"),
		"the workspace":        {from: downloadPath, to: ""},
		"the runner's own":     {from: downloadPath, to: "          path: ${{ runner.temp }}\n"},
		"the path in an input": step("      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2\n        with:\n          path: ${{ runner.temp }}/costroid-follow-up\n"),
	}
	for name, change := range edits {
		t.Run(name, func(t *testing.T) {
			change.rule = RuleFollowUpArtifact
			expectRule(t, read(t, filepath.Join(recipesDir, followUpRecipe)), change)
		})
	}
}

// The agent's own recipes check out the pull request on pull_request, where
// the token is read-only. The follow-up rules leave them alone.
func TestForkFollowUpRulesLeaveOtherWorkflowsAlone(t *testing.T) {
	report := LintBytes("x.yml", []byte(goodWorkflow))
	for _, finding := range report.Findings {
		if finding.Rule == RuleFollowUpCode || finding.Rule == RuleFollowUpArtifact {
			t.Errorf("%v", finding)
		}
	}
	if !isFollowUp(parseOne(t, "on: workflow_run\n")) || !isFollowUp(parseOne(t, "on: [push, workflow_run]\n")) {
		t.Error("a scalar or a list of events is not read")
	}
}

func parseOne(t *testing.T, source string) *yaml.Node {
	t.Helper()
	documents, err := parse([]byte(source))
	if err != nil || len(documents) != 1 {
		t.Fatalf("%v, %d documents", err, len(documents))
	}
	return field(documents[0], "on")
}

func TestForkFollowUpHoldsItsTokenToWhatItNeeds(t *testing.T) {
	const listPulls = "      pull-requests: read\n"
	edits := map[string]edit{
		"no pull-requests: read": {from: listPulls, to: ""},
		"pull-requests: none":    {from: listPulls, to: "      pull-requests: none\n"},
		"pull-requests: write":   {from: listPulls, to: "      pull-requests: write\n"},
		"contents: write":        {from: "      contents: read\n", to: "      contents: write\n"},
		"actions: write":         {from: "      actions: read\n", to: "      actions: write\n"},
		"an id token":            {from: listPulls, to: listPulls + "      id-token: write\n"},
		"issues: write":          {from: listPulls, to: listPulls + "      issues: write\n"},
		"write-all on the job":   {from: "    permissions:\n      # Downloads", to: "    permissions: write-all\n    env:\n      # Downloads"},
	}
	for name, change := range edits {
		t.Run(name, func(t *testing.T) {
			change.rule = RuleFollowUpPermissions
			expectRule(t, read(t, filepath.Join(recipesDir, followUpRecipe)), change)
		})
	}
}

func TestForkFollowUpPermissionsLeaveOtherWorkflowsAlone(t *testing.T) {
	for _, finding := range LintBytes("x.yml", []byte(goodWorkflow)).Findings {
		if finding.Rule == RuleFollowUpPermissions {
			t.Errorf("%v", finding)
		}
	}
	// read-all reads the pull requests, and writes nothing.
	source := read(t, filepath.Join(recipesDir, followUpRecipe))
	start := strings.Index(source, "    permissions:\n      # Downloads")
	end := strings.Index(source, "    steps:\n")
	if start < 0 || end < start {
		t.Fatal("the recipe's job permissions moved")
	}
	readAll := source[:start] + "    permissions: read-all\n" + source[end:]
	for _, finding := range LintBytes("x.yml", []byte(readAll)).Findings {
		if finding.Rule == RuleFollowUpPermissions {
			t.Errorf("read-all: %v", finding)
		}
	}
}

const (
	ghAwFollowUpRecipe = "github-gh-aw-follow-up.yml"
	ghAwIngestRun      = `costroid ingest-run --from gh-aw="$RUNNER_TEMP/gh-aw-usage" --out "$RUNNER_TEMP/costroid-rows.jsonl"`
	ghAwKeepStep       = "      - name: Keep the rows\n"
	ghAwSendStep       = "          platform-token: ${{ secrets.COSTROID_PLATFORM_TOKEN }}\n"
)

// TestGhAwFollowUpRunsNothingFromTheDownload holds the gh-aw follow-up to the
// rules every follow-up keeps. Its download is read by ingest-run --from gh-aw
// and by nothing else, it uploads only the rows ingest-run wrote, and only a
// job that sends them alone may hold id-token: write.
func TestGhAwFollowUpRunsNothingFromTheDownload(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, ghAwFollowUpRecipe))
	if report := LintBytes(ghAwFollowUpRecipe, []byte(source)); len(report.Findings) != 0 {
		t.Fatalf("findings: %v", report.Findings)
	}
	before := func(body string) edit { return edit{from: ghAwKeepStep, to: body + ghAwKeepStep} }
	for name, change := range map[string]edit{
		"a script from the download":    before("      - run: bash \"$RUNNER_TEMP/gh-aw-usage/agent/run.sh\"\n"),
		"a second command":              {from: ghAwIngestRun, to: ghAwIngestRun + "; sh x"},
		"another reader on the usage":   {from: ghAwIngestRun, to: `costroid ingest-run --from codex-home="$RUNNER_TEMP/gh-aw-usage" --out "$RUNNER_TEMP/costroid-rows.jsonl"`},
		"an upload of the download":     {from: "            ${{ runner.temp }}/costroid-rows.run.json\n", to: "            ${{ runner.temp }}/gh-aw-usage\n"},
		"an upload under another name":  {from: "          name: costroid-rows\n", to: "          name: rows\n"},
		"the download in an env":        before("      - env:\n          USAGE: ${{ runner.temp }}/gh-aw-usage\n        run: ls \"$USAGE\"\n"),
		"the download in the workspace": {from: "          path: ${{ runner.temp }}/gh-aw-usage\n", to: ""},
	} {
		t.Run(name, func(t *testing.T) {
			change.rule = RuleFollowUpArtifact
			expectRule(t, source, change)
		})
	}
	for name, change := range map[string]edit{
		"id-token beside another step":   {from: ghAwSendStep, to: ghAwSendStep + "      - run: echo sent\n"},
		"a write on the reading job":     {from: "      checks: write\n", to: "      checks: write\n      id-token: write\n"},
		"contents written by the sender": {from: "      id-token: write\n", to: "      id-token: write\n      contents: write\n"},
	} {
		t.Run(name, func(t *testing.T) {
			change.rule = RuleFollowUpPermissions
			expectRule(t, source, change)
		})
	}
}

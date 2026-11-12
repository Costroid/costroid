package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// estimateFile is a FOCUS cost file an estimator step wrote for a pull request:
// two resources in two sub-accounts, with a tag the file chose. Every name and
// figure is synthetic.
const estimateFile = "BilledCost,BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName,SubAccountId,Tags\n" +
	"0,EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2,example-account-a,\"{\"\"costroid/pr\"\":\"\"99\"\"}\"\n" +
	"0,EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,9.52,aws_ebs_volume.data,Amazon EBS,example-account-b,\n"

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// inPullRequest states a GitHub Actions pull request run, as the CI system
// does, for the join keys.
func inPullRequest(t *testing.T) {
	t.Helper()
	clearCI(t)
	event := writeTemp(t, "event.json", pullRequestEvent())
	for name, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "example/widgets", "GITHUB_RUN_ID": "1234",
		"GITHUB_SHA": mergeSHA, "GITHUB_EVENT_PATH": event, "GITHUB_SERVER_URL": "https://github.com",
	} {
		t.Setenv(name, value)
	}
}

// agentRows writes an agent's rows and its run object, as ingest-run writes
// them for a claude-code-action run, and returns the rows file.
func agentRows(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "costroid-rows.jsonl")
	if code, _, stderr := execute(t, "ingest-run", "--from", "execution-file="+executionFile, "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	return out
}

func readRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var rows []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFocusFileRowsAreEstimates(t *testing.T) {
	inPullRequest(t)
	out := agentRows(t)
	agent, object := readBytes(t, out), readBytes(t, runResultPath(out))
	code, _, stderr := execute(t, "ingest-run", "--from", "focus="+writeTemp(t, "infrastructure.csv", estimateFile), "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"added 2 rows from ", "each figure read from ListCost, with the basis estimate", "EUR 35.008 for 2026-12, estimated, from infrastructure, 2 rows", "warning: 1 row of "} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in %s", want, stderr)
		}
	}
	after := readBytes(t, out)
	if !bytes.HasPrefix(after, agent) {
		t.Fatal("the agent's rows did not stay first, byte for byte")
	}
	if !bytes.Equal(readBytes(t, runResultPath(out)), object) {
		t.Error("the run's object changed")
	}
	rows := readRows(t, out)
	estimates := rows[len(rows)-2:]
	for index, row := range estimates {
		tags, _ := row["Tags"].(map[string]any)
		for column, want := range map[string]any{
			"BilledCost": "0", "EffectiveCost": "0", "ListCost": []string{"25.488", "9.52"}[index],
			"BillingCurrency": "EUR", "ChargePeriodStart": "2026-12-01T00:00:00Z", "ChargePeriodEnd": "2027-01-01T00:00:00Z",
			"SubAccountId": []string{"example-account-a", "example-account-b"}[index],
		} {
			if row[column] != want {
				t.Errorf("row %d: %s reads %v, want %v", index, column, row[column], want)
			}
		}
		for tag, want := range map[string]any{
			"costroid/cost_basis": "estimate", "costroid/cost_source": "infrastructure", "costroid/repo": "github.com/example/widgets",
			"costroid/pr": "7", "costroid/commit_sha": headCommit, "costroid/workflow_run_id": "1234",
			"costroid/enforcement_grade": "none", "costroid/attribution_grade": "unattributed", "costroid/agent": nil,
		} {
			if tags[tag] != want {
				t.Errorf("row %d: %s reads %v, want %v", index, tag, tags[tag], want)
			}
		}
	}
}

func TestASourceNamesTheEstimatesOrigin(t *testing.T) {
	clearCI(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	code, _, stderr := execute(t, "ingest-run", "--from", "focus="+writeTemp(t, "plan.csv", estimateFile), "--out", out, "--source", "hcp-terraform")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	tags, _ := firstRow(t, out)["Tags"].(map[string]any)
	if tags["costroid/cost_source"] != "hcp-terraform" {
		t.Errorf("cost_source reads %v", tags["costroid/cost_source"])
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--source", "HCP Terraform"}, "ingest-run --source takes 1 to 64 lower-case letters, digits and single hyphens"},
		{[]string{"--source", "otlp"}, "ingest-run --source takes a name of its own"},
	} {
		args := append([]string{"ingest-run", "--from", "focus=" + writeTemp(t, "plan.csv", estimateFile), "--out", filepath.Join(t.TempDir(), "rows.jsonl")}, tc.args...)
		if code, _, stderr := execute(t, args...); code != exitFailure || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit %d: %s", tc.args, code, stderr)
		}
	}
	for _, flag := range [][]string{{"--source", "hcp-terraform"}, {"--source", "infrastructure"}, {"--optional"}, {"--optional=false"}} {
		args := append([]string{"ingest-run", "--from", "execution-file=" + executionFile, "--out", filepath.Join(t.TempDir(), "rows.jsonl")}, flag...)
		if code, _, stderr := execute(t, args...); code != exitFailure || !strings.Contains(stderr, "ingest-run takes --source and --optional only with focus") {
			t.Errorf("%v with execution-file: exit %d: %s", flag, code, stderr)
		}
	}
}

func TestMissingOptionalFileSaysWhy(t *testing.T) {
	inPullRequest(t)
	out := agentRows(t)
	before := readBytes(t, out)
	gone := filepath.Join(t.TempDir(), "infrastructure.csv")

	code, _, stderr := execute(t, "ingest-run", "--from", "focus="+gone, "--optional", "--out", out)
	if code != 0 {
		t.Fatalf("an optional file that is not there: exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"costroid: the infrastructure estimate is not available: there is no file at " + gone + ".",
		"The step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret.",
		"No rows were added to " + out,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in %s", want, stderr)
		}
	}
	if !bytes.Equal(readBytes(t, out), before) {
		t.Error("the rows file changed")
	}
	code, comment, stderr := execute(t, "report", "--rows", out, "--preview")
	const line = "Infrastructure estimate: not available (the step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret)."
	if code != 0 || strings.Count(comment, line) != 1 {
		t.Errorf("the report on those rows: exit %d, %d lines that say why: %s%s", code, strings.Count(comment, line), comment, stderr)
	}
	if !strings.Contains(comment, "This run cost USD") {
		t.Error("the agent's figure left the comment")
	}
	found := filepath.Join(t.TempDir(), "infrastructure.csv")
	if err := os.WriteFile(found, []byte(estimateFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := execute(t, "ingest-run", "--from", "focus="+found, "--optional", "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(estimateNotePath(out)); !os.IsNotExist(err) {
		t.Errorf("the note outlived the estimate it was about: %v", err)
	}
	if _, comment, _ := execute(t, "report", "--rows", out, "--preview"); strings.Contains(comment, line) || !strings.Contains(comment, "Infrastructure, estimated: EUR 35.008 for 2026-12") {
		t.Errorf("the report once the file is there: %s", comment)
	}
	if err := os.WriteFile(out, before, 0o644); err != nil {
		t.Fatal(err)
	}
	none := filepath.Join(t.TempDir(), "rows.jsonl")
	if code, _, _ := execute(t, "ingest-run", "--from", "focus="+gone, "--optional", "--out", none); code != 0 {
		t.Errorf("exit %d", code)
	}
	if _, err := os.Stat(none); !os.IsNotExist(err) {
		t.Errorf("an optional file that is not there created a rows file: %v", err)
	}

	code, _, stderr = execute(t, "ingest-run", "--from", "focus="+gone, "--out", out)
	if code != exitFailure || !strings.Contains(stderr, "does not exist: the step that writes it did not run or wrote nothing. Pass --optional where that step can be skipped") {
		t.Errorf("a required file that is not there: exit %d: %s", code, stderr)
	}
	if !bytes.Equal(readBytes(t, out), before) {
		t.Error("the rows file changed")
	}
}

func TestARefusedFileAddsNoRow(t *testing.T) {
	clearCI(t)
	out := agentRows(t)
	before := readBytes(t, out)
	saving := strings.Replace(estimateFile, ",9.52,", ",-9.52,", 1)
	code, _, stderr := execute(t, "ingest-run", "--from", "focus="+writeTemp(t, "infrastructure.csv", saving), "--out", out)
	if code != exitFailure || !strings.Contains(stderr, `line 3, ListCost, "-9.52": negative: the row contract holds amounts of 0 and above`) {
		t.Errorf("exit %d: %s", code, stderr)
	}
	if !bytes.Equal(readBytes(t, out), before) {
		t.Error("a refused file added rows")
	}
	torn := writeTemp(t, "rows.jsonl", strings.TrimSuffix(string(before), "\n"))
	code, _, stderr = execute(t, "ingest-run", "--from", "focus="+writeTemp(t, "infrastructure.csv", estimateFile), "--out", torn)
	if code != exitFailure || !strings.Contains(stderr, "does not end with a line feed") {
		t.Errorf("a rows file without its last line feed: exit %d: %s", code, stderr)
	}
	link := filepath.Join(t.TempDir(), "rows.jsonl")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := execute(t, "ingest-run", "--from", "focus="+writeTemp(t, "infrastructure.csv", estimateFile), "--out", link); code != exitFailure || !strings.Contains(stderr, "is not a regular file") {
		t.Errorf("a link in place of the rows file: exit %d: %s", code, stderr)
	}
	if !bytes.Equal(readBytes(t, out), before) {
		t.Error("rows were added through a link")
	}
}

// The ingest-run lines of docs/infrastructure.md's shell and YAML blocks.
var (
	docsCodeBlock  = regexp.MustCompile("(?ms)^```(?:sh|yaml)\n(.*?)^```")
	docsIngestLine = regexp.MustCompile(`(?m)costroid ingest-run --from focus=.*$`)
)

// TestTheInfrastructurePageExampleParses holds the page's example to the
// command, so the example cannot go stale.
func TestTheInfrastructurePageExampleParses(t *testing.T) {
	var lines []string
	for _, block := range docsCodeBlock.FindAllStringSubmatch(readFile(t, filepath.Join(repositoryRoot, "docs", "infrastructure.md")), -1) {
		lines = append(lines, docsIngestLine.FindAllString(block[1], -1)...)
	}
	if len(lines) < 2 {
		t.Fatal("docs/infrastructure.md shows no ingest-run --from focus line")
	}
	for _, line := range lines {
		args := strings.Fields(line)[2:]
		flags, err := parseIngestRun(args, &bytes.Buffer{}, &bytes.Buffer{})
		if err != nil || !flags.source.kind.estimate {
			t.Errorf("%q: %v", line, err)
		}
	}
}

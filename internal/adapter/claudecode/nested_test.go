package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	runSession    = "00000000-0000-4000-8000-000000000002"
	nestedSession = "00000000-0000-4000-8000-0000000000b1"
	nestedRequest = "req_fixture_nested"
	runCWD        = "/work"
)

// The recorded subagent run's events span 11:15:12.814 to 11:15:18.313.
var (
	runStart = time.Date(2026, 11, 1, 11, 15, 10, 0, time.UTC)
	runEnd   = time.Date(2026, 11, 1, 11, 15, 30, 0, time.UTC)
	inside   = time.Date(2026, 11, 1, 11, 15, 15, 0, time.UTC)
)

// outsideCI clears the variables a CI system sets, which the test may run under.
func outsideCI(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
}

// insideCI sets one CI system's own variable.
func insideCI(t *testing.T, variable string) {
	t.Helper()
	outsideCI(t)
	t.Setenv(variable, "true")
}

// agentTree lays out the recorded subagent run, outside CI, with the working
// directory on its main session file, as a real file states it and the
// recording cut.
func agentTree(t *testing.T) (config, project string) {
	t.Helper()
	outsideCI(t)
	config = installSession(t, "subagent", runSession)
	project = filepath.Join(config, "projects", "-work")
	main := filepath.Join(project, runSession+".jsonl")
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	stated := strings.ReplaceAll(string(data), `"sessionId":`, `"cwd":"`+runCWD+`","sessionId":`)
	if err := os.WriteFile(main, []byte(stated), 0o644); err != nil {
		t.Fatal(err)
	}
	return config, project
}

type nestedFile struct {
	session, request, cwd string
	start, end            time.Time
}

// nestedAt is a nested session inside the run, in the run's directory.
func nestedAt(start, end time.Time) nestedFile {
	return nestedFile{session: nestedSession, request: nestedRequest, cwd: runCWD, start: start, end: end}
}

// write fills the not-recorded template and saves it at path.
func (n nestedFile) write(t *testing.T, path string) {
	t.Helper()
	template, err := os.ReadFile(filepath.Join("testdata", "not-recorded", "nested", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.NewReplacer(
		"@session", n.session, "@request", n.request,
		"@start", n.start.Format(time.RFC3339Nano), "@end", n.end.Format(time.RFC3339Nano),
		`"cwd":"/work"`, `"cwd":"`+n.cwd+`"`,
	).Replace(string(template))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (n nestedFile) writeIn(t *testing.T, project string) {
	t.Helper()
	n.write(t, filepath.Join(project, n.session+".jsonl"))
}

// finishTree replays the recorded stream over the tree inside the run window.
func finishTree(t *testing.T, config string) adapter.Report {
	t.Helper()
	return finishTreeBilledBy(t, config, "none")
}

// finishTreeBilledBy replays the stream with its apiKeySource replaced.
func finishTreeBilledBy(t *testing.T, config, apiKeySource string) adapter.Report {
	t.Helper()
	session := Agent{ConfigDir: config}.NewSession(adapter.Plan{}).(*Session)
	data, err := os.ReadFile(fixture("subagent", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.ReplaceAll(line, `"apiKeySource":"none"`, `"apiKeySource":"`+apiKeySource+`"`)
		if err := session.Feed([]byte(line), recorded); err != nil {
			t.Fatal(err)
		}
	}
	session.RunWindow(runStart, runEnd)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// assertRunAlone checks that the run's own rows are the recording's, and that
// nothing was counted as nested.
func assertRunAlone(t *testing.T, report adapter.Report) {
	t.Helper()
	if len(report.Nested) != 0 {
		t.Errorf("%d nested sessions counted: %+v", len(report.Nested), report.Nested)
	}
	assertRunRows(t, report)
}

func assertRunRows(t *testing.T, report adapter.Report) {
	t.Helper()
	got, want := sum(report.Requests), resultTotals(t, "subagent")
	if report.CostSource != SourceSessionFile || got.InputUncached != want.InputTokens || got.Output != want.OutputTokens || got.CacheRead != want.CacheRead {
		t.Errorf("the run's own rows moved: %s %+v", report.CostSource, got)
	}
}

func warned(report adapter.Report, text string) bool {
	for _, warning := range report.Warnings {
		if strings.Contains(warning, nestedSession) && strings.Contains(warning, text) {
			return true
		}
	}
	return false
}

func TestNestedSessionInsideTheRunIsCountedUnderItsOwnID(t *testing.T) {
	config, project := agentTree(t)
	nestedAt(inside, inside.Add(time.Second)).writeIn(t, project)
	report := finishTree(t, config)
	assertRunRows(t, report)
	if len(report.Nested) != 1 {
		t.Fatalf("%d nested sessions, want 1", len(report.Nested))
	}
	nested := report.Nested[0]
	if nested.SessionID != nestedSession || nested.AgentVersion != version || nested.CostSource != SourceSessionFile || nested.CostBasis != focus.ListEquivalent {
		t.Errorf("%s %s %s %s", nested.SessionID, nested.AgentVersion, nested.CostSource, nested.CostBasis)
	}
	if len(nested.Requests) != 1 || nested.Requests[0].ID != nestedRequest {
		t.Fatalf("%+v", nested.Requests)
	}
	if got := nested.Requests[0].Tokens; got != (adapter.Tokens{InputUncached: 1000, Output: 200}) {
		t.Errorf("%+v", got)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestSessionsThatStartOutsideTheRunAreNotCounted(t *testing.T) {
	for name, nested := range map[string]nestedFile{
		"an earlier run on the same runner": nestedAt(runStart.Add(-time.Hour), runStart.Add(-time.Minute)),
		"one that began before this run":    nestedAt(runStart.Add(-time.Second), inside),
		"a later run":                       nestedAt(runEnd.Add(time.Second), runEnd.Add(time.Minute)),
	} {
		t.Run(name, func(t *testing.T) {
			config, project := agentTree(t)
			nested.writeIn(t, project)
			report := finishTree(t, config)
			assertRunAlone(t, report)
			if len(report.Warnings) != 0 {
				t.Errorf("warnings %q", report.Warnings)
			}
		})
	}
}

func TestSessionStillWritingAfterTheRunIsNamedAndNotCounted(t *testing.T) {
	config, project := agentTree(t)
	nestedAt(inside, runEnd.Add(time.Second)).writeIn(t, project)
	report := finishTree(t, config)
	assertRunAlone(t, report)
	if !warned(report, "wrote after it ended") {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestSessionInAnotherWorkingDirectoryIsNamedAndNotCounted(t *testing.T) {
	config, project := agentTree(t)
	nested := nestedAt(inside, inside.Add(time.Second))
	nested.cwd = "/work/other"
	nested.writeIn(t, project)
	report := finishTree(t, config)
	assertRunAlone(t, report)
	if !warned(report, "another working directory") {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestARunThatStatesNoWorkingDirectoryCountsNoNestedSession(t *testing.T) {
	outsideCI(t)
	config := installSession(t, "subagent", runSession)
	nestedAt(inside, inside.Add(time.Second)).writeIn(t, filepath.Join(config, "projects", "-work"))
	report := finishTree(t, config)
	assertRunAlone(t, report)
	if !warned(report, "states no working directory") {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestTheStreamsWorkingDirectoryIsTheRuns(t *testing.T) {
	outsideCI(t)
	config := installSession(t, "subagent", runSession)
	nestedAt(inside, inside.Add(time.Second)).writeIn(t, filepath.Join(config, "projects", "-work"))
	session := Agent{ConfigDir: config}.NewSession(adapter.Plan{}).(*Session)
	if err := session.Feed([]byte(`{"type":"system","subtype":"init","cwd":"/work","session_id":"`+runSession+`"}`), recorded); err != nil {
		t.Fatal(err)
	}
	feedStream(t, session, "subagent")
	session.RunWindow(runStart, runEnd)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nested) != 1 {
		t.Fatalf("%d nested sessions, warnings %q", len(report.Nested), report.Warnings)
	}
}

func TestARunsOwnSubagentFileIsNeverCountedAsNested(t *testing.T) {
	config, project := agentTree(t)
	// Older Claude Code versions kept a subagent's transcript beside the main
	// one, under the run's session id.
	copyFile(t, fixture("subagent", "subagents/agent-1.jsonl"), filepath.Join(project, "agent-1.jsonl"))
	report := finishTree(t, config)
	assertRunAlone(t, report)
	if len(report.Warnings) != 0 {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestARequestTheRunAlreadyHoldsIsNotCountedAgain(t *testing.T) {
	config, project := agentTree(t)
	nested := nestedAt(inside, inside.Add(time.Second))
	nested.request = "req_fixture_0003"
	nested.writeIn(t, project)
	assertRunAlone(t, finishTree(t, config))
}

func TestANestedSessionsSubagentsAreCountedWithIt(t *testing.T) {
	config, project := agentTree(t)
	nestedAt(inside, inside.Add(time.Second)).writeIn(t, project)
	subagent := nestedFile{session: nestedSession, request: "req_fixture_nested_agent", cwd: runCWD, start: inside, end: inside.Add(2 * time.Second)}
	subagent.write(t, filepath.Join(project, nestedSession, "subagents", "agent-1.jsonl"))
	report := finishTree(t, config)
	if len(report.Nested) != 1 || len(report.Nested[0].Requests) != 2 {
		t.Fatalf("%+v", report.Nested)
	}
	if got := sum(report.Nested[0].Requests); got != (adapter.Tokens{InputUncached: 2000, Output: 400}) {
		t.Errorf("%+v", got)
	}
}

func TestExecutionFileRunCountsANestedSession(t *testing.T) {
	config, project := agentTree(t)
	nestedAt(inside, inside.Add(time.Second)).writeIn(t, project)
	file, err := os.Open(notRecordedExecutionFile)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := Agent{ConfigDir: config}.ReadExecutionFile(file, time.Date(2026, 11, 1, 11, 15, 19, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nested) != 1 || report.Nested[0].SessionID != nestedSession {
		t.Fatalf("%+v, warnings %q", report.Nested, report.Warnings)
	}
}

func TestAFileLastWrittenBeforeTheRunIsNotRead(t *testing.T) {
	config, project := agentTree(t)
	stale := filepath.Join(project, nestedSession+".jsonl")
	if err := os.WriteFile(stale, []byte("not a session file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, runStart.Add(-time.Hour), runStart.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	report := finishTree(t, config)
	assertRunAlone(t, report)
	if len(report.Warnings) != 0 {
		t.Errorf("the stale file was read: %q", report.Warnings)
	}
}

// sideCall is a nested session a plugin started in a temporary directory, so
// it sits in that directory's project directory.
func sideCall(start, end time.Time) nestedFile {
	nested := nestedAt(start, end)
	nested.cwd = "/var/side-call"
	return nested
}

func (n nestedFile) writeInProjectOf(t *testing.T, config string) {
	t.Helper()
	n.write(t, filepath.Join(config, "projects", "-var-side-call", n.session+".jsonl"))
}

func TestInCIASessionInAnyDirectoryInsideTheRunIsCounted(t *testing.T) {
	for _, variable := range []string{"GITHUB_ACTIONS", "GITLAB_CI"} {
		t.Run(variable, func(t *testing.T) {
			config, _ := agentTree(t)
			insideCI(t, variable)
			sideCall(inside, inside.Add(time.Second)).writeInProjectOf(t, config)
			report := finishTree(t, config)
			assertRunRows(t, report)
			if len(report.Nested) != 1 || report.Nested[0].SessionID != nestedSession || len(report.Nested[0].Requests) != 1 {
				t.Fatalf("%+v, warnings %q", report.Nested, report.Warnings)
			}
			if len(report.Warnings) != 0 {
				t.Errorf("warnings %q", report.Warnings)
			}
		})
	}
}

func TestOutsideCIASessionInAnotherProjectDirectoryIsNotCounted(t *testing.T) {
	config, _ := agentTree(t)
	sideCall(inside, inside.Add(time.Second)).writeInProjectOf(t, config)
	report := finishTree(t, config)
	assertRunAlone(t, report)
	if len(report.Warnings) != 0 {
		t.Errorf("warnings %q", report.Warnings)
	}
}

func TestInCIASessionOutsideTheRunIsStillNotCounted(t *testing.T) {
	config, _ := agentTree(t)
	insideCI(t, "GITHUB_ACTIONS")
	sideCall(runStart.Add(-time.Hour), runStart.Add(-time.Minute)).writeInProjectOf(t, config)
	assertRunAlone(t, finishTree(t, config))
}

func TestANestedSessionTakesTheRunsBillingBasis(t *testing.T) {
	for source, want := range map[string]focus.CostBasis{
		"ANTHROPIC_API_KEY": focus.Measured,
		"none":              focus.ListEquivalent,
	} {
		t.Run(source, func(t *testing.T) {
			config, project := agentTree(t)
			nestedAt(inside, inside.Add(time.Second)).writeIn(t, project)
			report := finishTreeBilledBy(t, config, source)
			if report.CostBasis != want || len(report.Nested) != 1 || report.Nested[0].CostBasis != want {
				t.Fatalf("run %s, nested %+v, want %s", report.CostBasis, report.Nested, want)
			}
		})
	}
}

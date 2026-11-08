package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	sdkCodex    = "testdata/0.164.0-alpha.11.1/sdk-codex"
	sdkThreadID = "00000000-0000-4000-8000-000000000001"
	sdkDay      = "sessions/2026/11/08"
)

// sdkHome is a Codex home for one test, outside CI, holding the exec
// recording's rollout file from an earlier run.
func sdkHome(t *testing.T) string {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv(codexAPIKeyVariable, "a-key")
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeRollout(t, home, "sessions/2026/11/07/rollout-2026-11-07T10-00-00-"+recordedThreadID+".jsonl", fixture(t, "testdata/0.164.0-alpha.11.1/exec/rollout.jsonl"))
	return home
}

// recordedThreadID is the thread the exec recording's rollout file names.
const recordedThreadID = "00000000-0000-4000-8000-000000000004"

func writeRollout(t *testing.T, home, name, text string) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// startSDK starts reading a program's run, as the run does before it starts
// the program.
func startSDK(t *testing.T, dir string) *SDKSession {
	t.Helper()
	plan, err := SDKAgent{}.Plan([]string{"node", "dist/codex.js"}, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	return SDKAgent{Dir: dir}.NewSession(plan).(*SDKSession)
}

// endSDK feeds the program's stdout, ends the watch and reads the run.
func endSDK(t *testing.T, session *SDKSession, stdout []string) (adapter.Report, [][]adapter.Request, error) {
	t.Helper()
	for _, line := range stdout {
		if err := session.Feed([]byte(line), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var settled [][]adapter.Request
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	session.Tail(ended, func(requests []adapter.Request) { settled = append(settled, requests) })
	report, err := session.Finish()
	return report, settled, err
}

// The recorded Codex SDK run's thread appears under CODEX_HOME while the
// program runs. The run takes it with no line of the program's stdout, reads
// its one request from the rollout file, settles it for the watchdog, and
// grades the run as codex exec's. The earlier thread the home held is left
// alone, and the rows come to the recording's own counts, priced by hand.
func TestCodexSDKRollout(t *testing.T) {
	home := sdkHome(t)
	session := startSDK(t, "/work")
	writeRollout(t, home, sdkDay+"/rollout-2026-11-08T04-14-30-"+sdkThreadID+".jsonl", fixture(t, filepath.Join(sdkCodex, "rollout.jsonl")))
	report, settled, err := endSDK(t, session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionID != sdkThreadID || report.AgentVersion != recordedVersion || report.CostSource != costSourceRollout || report.CostBasis != focus.Measured {
		t.Errorf("session %s, version %s, %s, %s", report.SessionID, report.AgentVersion, report.CostSource, report.CostBasis)
	}
	if report.Enforcement.Grade != adapter.GradeWrapperWatchdog || report.Enforcement.Note != watchedNote || len(report.Nested) != 0 || len(report.Warnings) != 0 {
		t.Errorf("%+v, warnings %q", report.Enforcement, report.Warnings)
	}
	// The stream's own total: 10038 input, 10035 of it written to the cache,
	// and 5 output tokens.
	want := adapter.Tokens{InputUncached: 3, CacheWrite30m: 10035, Output: 5}
	if len(report.Requests) != 1 || report.Requests[0].Model != "gpt-6-luna" || report.Requests[0].Grain != adapter.PerRequest || requestSum(report.Requests) != want {
		t.Fatalf("%+v", report.Requests)
	}
	if len(settled) != 1 || len(settled[0]) != 1 {
		t.Errorf("the watchdog saw %v", settled)
	}
	// gpt-6-luna per million: 3 × 0.10 + 10035 × 0.125 + 5 × 0.50 = 1257.175.
	if got := listCost(t, report.Requests).String(); got != "0.001257175" {
		t.Errorf("list cost %s USD, the recorded counts priced by hand give 0.001257175 USD", got)
	}

	// The same run read through the program's stdout, which here is the
	// SDK's own events, comes to the same requests.
	home = sdkHome(t)
	again := startSDK(t, "/work")
	stdout := strings.Split(strings.TrimSpace(fixture(t, filepath.Join(sdkCodex, "stream.jsonl"))), "\n")
	writeRollout(t, home, sdkDay+"/rollout-2026-11-08T04-14-30-"+sdkThreadID+".jsonl", fixture(t, filepath.Join(sdkCodex, "rollout.jsonl")))
	read, _, err := endSDK(t, again, stdout)
	if err != nil || requestSum(read.Requests) != want {
		t.Errorf("%+v, %v", read.Requests, err)
	}
}

// The watchdog sees each request settle while the program runs.
func TestCodexSDKSettlesEachRequestWhileTheProgramRuns(t *testing.T) {
	home := sdkHome(t)
	session := startSDK(t, "/work")
	lines := strings.SplitAfter(fixture(t, filepath.Join(sdkCodex, "rollout.jsonl")), "\n")
	path := writeRollout(t, home, sdkDay+"/rollout-2026-11-08T04-14-30-"+sdkThreadID+".jsonl", lines[0])
	ctx, cancel := context.WithCancel(context.Background())
	settled := make(chan []adapter.Request, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Tail(ctx, func(requests []adapter.Request) { settled <- requests })
	}()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines[1:] {
		if _, err := file.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	file.Close()
	select {
	case requests := <-settled:
		if len(requests) != 1 {
			t.Errorf("%+v", requests)
		}
	case <-time.After(5 * time.Second):
		t.Error("no request settled while the program ran")
	}
	cancel()
	<-done
}

// A thread outside the run's directory is not the run's outside CI, and is in
// CI. A thread the home held before the run, written while it ran, refuses
// the run, because it holds an earlier run's spend. A home with no new thread
// says why it has no rows.
func TestCodexSDKThreadsTheRunDoesNotCount(t *testing.T) {
	recording := func(t *testing.T) string { return fixture(t, filepath.Join(sdkCodex, "rollout.jsonl")) }
	t.Run("another directory", func(t *testing.T) {
		home := sdkHome(t)
		session := startSDK(t, "/elsewhere")
		writeRollout(t, home, sdkDay+"/rollout-2026-11-08T04-14-30-"+sdkThreadID+".jsonl", recording(t))
		report, _, err := endSDK(t, session, nil)
		if err != nil || len(report.Requests) != 0 || !slices.ContainsFunc(report.Warnings, func(w string) bool {
			return strings.Contains(w, "thread "+sdkThreadID+" started while the program ran in another working directory")
		}) {
			t.Errorf("%+v, %v", report, err)
		}
	})
	t.Run("in CI", func(t *testing.T) {
		home := sdkHome(t)
		t.Setenv("GITHUB_ACTIONS", "true")
		session := startSDK(t, "/elsewhere")
		writeRollout(t, home, sdkDay+"/rollout-2026-11-08T04-14-30-"+sdkThreadID+".jsonl", recording(t))
		report, _, err := endSDK(t, session, nil)
		if err != nil || len(report.Requests) != 1 {
			t.Errorf("%+v, %v", report, err)
		}
	})
	t.Run("an earlier thread", func(t *testing.T) {
		home := sdkHome(t)
		session := startSDK(t, "/work")
		earlier := filepath.Join(home, "sessions", "2026", "11", "07", "rollout-2026-11-07T10-00-00-"+recordedThreadID+".jsonl")
		later := time.Now().Add(time.Minute)
		if err := os.Chtimes(earlier, later, later); err != nil {
			t.Fatal(err)
		}
		_, _, err := endSDK(t, session, nil)
		if err == nil || !strings.Contains(err.Error(), "was started before the program ran and written while it ran") {
			t.Errorf("%v", err)
		}
	})
	t.Run("no thread", func(t *testing.T) {
		sdkHome(t)
		report, _, err := endSDK(t, startSDK(t, "/work"), nil)
		if err != nil || len(report.Requests) != 0 || report.Enforcement.Grade != adapter.GradeNone || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "started no Codex thread there") {
			t.Errorf("%+v, %v", report, err)
		}
	})
}

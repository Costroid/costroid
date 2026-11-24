package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// askingLine is what a command says before its first request to the stub.
func askingLine(stub *scmtest.Stub) string {
	return "costroid: asking " + strings.TrimPrefix(stub.Server.URL, "http://") + "\n"
}

// stillWaitingLines are the lines of said that say a command still waits for
// what. Each must state one interval more than the line before it, of at most
// limit, so a line is said at each interval and none is skipped or repeated.
func stillWaitingLines(t *testing.T, said, what string, every, limit time.Duration) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(said, "\n") {
		if !strings.HasPrefix(line, "costroid: still waiting") {
			continue
		}
		count++
		if want := fmt.Sprintf("costroid: still waiting for %s, %s of at most %s", what, span(time.Duration(count)*every), span(limit)); line != want {
			t.Errorf("line %d of the wait reads %q, want %q", count, line, want)
		}
	}
	return count
}

// TestACommandThatWaitsSaysSoAtAFixedInterval holds the line a wait says: at
// each interval, in whole intervals, for as long as the command sleeps, never
// before the interval has passed, and never under --quiet. A wait that names
// no interval says nothing, and one whose context ends stops at once.
func TestACommandThatWaitsSaysSoAtAFixedInterval(t *testing.T) {
	const every, limit = 20 * time.Millisecond, 400 * time.Millisecond
	var said bytes.Buffer
	waiting := stillWaiting{stderr: cli.NewMessages(&said), what: "the answer", every: every, limit: limit}
	started := time.Now()
	for range 3 {
		if !waiting.sleep(context.Background(), 30*time.Millisecond) {
			t.Fatal("a sleep under a context that has not ended answered false")
		}
	}
	lines := stillWaitingLines(t, said.String(), "the answer", every, limit)
	if elapsed := time.Since(started); lines < 4 || time.Duration(lines)*every > elapsed {
		t.Errorf("%d lines in %s, want one at each %s and none early:\n%s", lines, elapsed, every, said.String())
	}

	said.Reset()
	silent := stillWaiting{stderr: cli.NewMessages(&said), what: "the answer", limit: limit}
	if !silent.sleep(context.Background(), 30*time.Millisecond) || said.Len() != 0 {
		t.Errorf("a wait with no interval said %q", said.String())
	}

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	started = time.Now()
	if stopped := (&stillWaiting{stderr: &said, what: "the answer", every: every, limit: limit}); stopped.sleep(ended, time.Minute) || time.Since(started) > 10*time.Second {
		t.Error("a sleep under a context that ended went on")
	}
	// The two waits a person can meet say so every 10 s.
	if ingestPace.every != 10*time.Second || statementPace.every != 10*time.Second {
		t.Errorf("export says it waits every %s and the gateway every %s, want 10s", ingestPace.every, statementPace.every)
	}
	if span(10*time.Second) != "10 s" || span(150*time.Second) != "150 s" || span(20*time.Millisecond) != "20ms" {
		t.Errorf("a span reads %q, %q and %q", span(10*time.Second), span(150*time.Second), span(20*time.Millisecond))
	}
}

// TestExportSaysItStillWaitsForTheVerdict sends a batch to a platform that
// never states a verdict. export says at each interval that it still waits,
// on standard error, until it gives the wait up. --quiet drops the lines, and
// a verdict that comes at once has none.
func TestExportSaysItStillWaitsForTheVerdict(t *testing.T) {
	pace := verdictPace{interval: time.Millisecond, limit: 300 * time.Millisecond, every: 50 * time.Millisecond}
	const what = "Costroid Platform's verdict on batch 903114-16873409921-1"
	run := func(t *testing.T, verdicts []turn, extra ...string) (int, string, string) {
		stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
		stub.verdicts = verdicts
		env := stub.githubEnv()
		var stdout, stderr bytes.Buffer
		args := append([]string{"--rows", exampleRows, "--platform-url", stub.server.URL, "--installation-id", testInstallation}, extra...)
		code := export(context.Background(), args, stub.server.Client(), func(name string) string { return env[name] }, pace, stdio{stdout: &stdout, stderr: cli.NewMessages(&stderr)})
		return code, stdout.String(), stderr.String()
	}
	never := slices.Repeat([]turn{pending("ingesting")}, 100000)
	code, stdout, stderr := run(t, never)
	if lines := stillWaitingLines(t, stderr, what, pace.every, pace.limit); code != 0 || stdout != "" || lines < 3 || lines > 6 {
		t.Errorf("exit %d, stdout %q, and %d lines of the wait, want one each %s for %s:\n%s", code, stdout, lines, pace.every, pace.limit, stderr)
	}
	if !strings.Contains(stderr, "costroid: warning: batch 903114-16873409921-1 has no verdict after ") {
		t.Errorf("export did not give the wait up:\n%s", stderr)
	}
	if quietCode, _, quiet := run(t, never, "--quiet"); quietCode != code || strings.Contains(quiet, "still waiting") || !strings.Contains(quiet, "costroid: warning: batch ") {
		t.Errorf("under --quiet: exit %d, stderr:\n%s", quietCode, quiet)
	}
	if code, _, stderr := run(t, []turn{ingested(11, 0)}); code != 0 || strings.Contains(stderr, "still waiting") {
		t.Errorf("a verdict that came at once: exit %d, stderr:\n%s", code, stderr)
	}
}

// TestGatewayMintSaysItStillWaitsForThePlatform answers the key's attestation
// 503 with a Retry-After of 60 s, on a clock where a second lasts 10 ms. mint
// says at each interval that it still waits, then sends the write again.
func TestGatewayMintSaysItStillWaitsForThePlatform(t *testing.T) {
	platform := newStatementPlatform(t)
	job, _ := exampleJob(t)
	platform.names(t)
	second, pace := retryAfterSecond, statementPace
	t.Cleanup(func() { retryAfterSecond, statementPace = second, pace })
	retryAfterSecond = 10 * time.Millisecond
	statementPace = verdictPace{interval: time.Millisecond, limit: 150 * retryAfterSecond, every: 100 * time.Millisecond}
	platform.turns = []statementTurn{{status: http.StatusServiceUnavailable, body: refusalExample("INSTALLATION_PROOF_KEYS_UNAVAILABLE", attestationWrite), retry: "60"}}
	code, _, stderr := job.mintFor(t, gatewaytest.New(t, gatewayAdmin))
	lines := stillWaitingLines(t, stderr, "Costroid Platform to take the key's attestation", statementPace.every, statementPace.limit)
	if code != 0 || lines < 3 || lines > 8 || !strings.Contains(stderr, "attested to Costroid Platform") {
		t.Errorf("exit %d and %d lines of the wait, want one each %s for a wait of %s:\n%s", code, lines, statementPace.every, 60*retryAfterSecond, stderr)
	}
}

// TestResultSaysWhichHostItAsksBeforeItAsks holds result to the line seven
// commands say before their first request to a host, once, first on standard
// error, and dropped under --quiet.
func TestResultSaysWhichHostItAsksBeforeItAsks(t *testing.T) {
	stub, newest := threeReports(t)
	code, stdout, stderr := execute(t, "result", "--pr", "7", "--json")
	if code != 0 || stdout != checkBlock(t, newest.Text)+"\n" || stderr != askingLine(stub) {
		t.Errorf("exit %d, stderr %q, want the one line %q", code, stderr, askingLine(stub))
	}
	if code, quietOut, quiet := execute(t, "result", "--quiet", "--pr", "7", "--json"); code != 0 || quietOut != stdout || quiet != "" {
		t.Errorf("under --quiet: exit %d, stderr %q", code, quiet)
	}
}

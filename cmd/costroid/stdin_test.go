package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/mcpserver"
)

// executeWith runs a command line with text on standard input.
func executeWith(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := dispatch(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestValidateReadsStandardInputForADash(t *testing.T) {
	t.Chdir(t.TempDir())
	code, stdout, stderr := executeWith(t, "policy:\n  run_budget_usd: \"5\"\n", "validate", "-")
	if code != 0 || stdout != "standard input: valid\n" || stderr != nextAfterValidate+"\n" {
		t.Errorf("a valid file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = executeWith(t, "policy:\n  no_such_key: 1\n", "validate", "-")
	if code != exitRejected || !strings.HasPrefix(stdout, "standard input: ") || stderr != "" {
		t.Errorf("a file that breaks the schema: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestValidateTakesAFileWhoseNameOpensWithADashAfterTwoDashes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "-base.yml"), "policy:\n  run_budget_usd: \"5\"\n")
	if code, stdout, stderr := execute(t, "validate", "--", "-base.yml"); code != 0 || !strings.HasPrefix(stdout, "-base.yml: valid\n") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := execute(t, "validate", "-base.yml"); code != exitUsage || !strings.HasPrefix(stderr, "costroid: -base.yml is not a flag of costroid validate\n") {
		t.Errorf("without the two dashes: exit %d, stderr %q", code, stderr)
	}
}

// TestEmitOTLPReadsItsRowsFromStandardInputForADash gives emit-otlp a line
// that is no row on standard input. It must refuse that line, and not look
// for a file named with a dash.
func TestEmitOTLPReadsItsRowsFromStandardInputForADash(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	code, stdout, stderr := executeWith(t, "this line is no row\n", "emit-otlp", "--rows", "-")
	if code != exitFailure || stdout != "" || strings.Contains(stderr, "no such file") || !strings.Contains(stderr, "line 1") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, _, stderr = executeWith(t, "", "emit-otlp", "--rows", "-")
	if code != 0 || strings.Contains(stderr, "no such file") {
		t.Errorf("no rows: exit %d, stderr %q", code, stderr)
	}
}

// TestEveryCommandThatReadsRowsReadsStandardInputForADash runs each command
// that takes --rows on a file, then on a dash with the file's bytes on
// standard input, and holds the two runs to the same result.
func TestEveryCommandThatReadsRowsReadsStandardInputForADash(t *testing.T) {
	rows := readFile(t, realRows)
	at := []string{"--at", heldInstant}

	t.Run("policy", func(t *testing.T) {
		dir := besideADashFile(t, rows)
		config := writeBaseConfig(t, overBudget)
		for _, form := range [][]string{nil, {"--json"}} {
			args := slices.Concat([]string{"policy", "--config", config}, at, form)
			code, stdout, stderr := execute(t, append(args, "--rows", filepath.Join(dir, "rows.jsonl"))...)
			if code != 0 || !strings.Contains(stdout, "run_budget") && !strings.Contains(stdout, "run budget") {
				t.Fatalf("%v on the file: exit %d, stdout %q, stderr %q", form, code, stdout, stderr)
			}
			piped, pipedOut, pipedErr := executeWith(t, rows, append(args, "--rows", "-")...)
			if piped != code || pipedOut != stdout || pipedErr != stderr {
				t.Errorf("%v on standard input: exit %d, stdout %q, stderr %q; the file gives exit %d, %q, %q", form, piped, pipedOut, pipedErr, code, stdout, stderr)
			}
		}
	})

	t.Run("report --preview", func(t *testing.T) {
		holdTheClock(t)
		dir := besideADashFile(t, rows)
		code, stdout, stderr := execute(t, "report", "--preview", "--rows", filepath.Join(dir, "rows.jsonl"))
		if code != 0 || !strings.Contains(stdout, "USD "+realTotal) {
			t.Fatalf("on the file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		piped, pipedOut, pipedErr := executeWith(t, rows, "report", "--preview", "--rows", "-")
		if piped != code || pipedOut != stdout || pipedErr != stderr {
			t.Errorf("on standard input: exit %d, stdout %q, stderr %q; the file gives exit %d, %q, %q", piped, pipedOut, pipedErr, code, stdout, stderr)
		}
	})

	t.Run("report", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		stub.Files[configFile] = overBudget
		if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("on the file: exit %d, %d checks: %s", code, len(stub.Checks), stderr)
		}
		if code, _, stderr := executeWith(t, rows, "report", "--rows", "-"); code != 0 || len(stub.Checks) != 2 {
			t.Fatalf("on standard input: exit %d, %d checks: %s", code, len(stub.Checks), stderr)
		}
		file, piped := stub.Checks[0], stub.Checks[1]
		if !strings.Contains(file.Title, "USD "+realTotal) || piped.Title != file.Title || piped.Conclusion != file.Conclusion || piped.Summary != file.Summary {
			t.Errorf("standard input posts %q, %s; the file posts %q, %s", piped.Title, piped.Conclusion, file.Title, file.Conclusion)
		}
	})

	t.Run("follow-up", func(t *testing.T) {
		stub := followUpEnvironment(t, "stranger/widgets")
		stub.Files[configFile] = overBudget
		if code, _, stderr := execute(t, "follow-up", "--rows", realRows); code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("on the file: exit %d, %d checks: %s", code, len(stub.Checks), stderr)
		}
		if code, _, stderr := executeWith(t, rows, "follow-up", "--rows", "-"); code != 0 || len(stub.Checks) != 2 {
			t.Fatalf("on standard input: exit %d, %d checks: %s", code, len(stub.Checks), stderr)
		}
		file, piped := stub.Checks[0], stub.Checks[1]
		if !strings.Contains(file.Title, "USD "+realTotal) || piped.Title != file.Title || piped.Conclusion != file.Conclusion || piped.Summary != file.Summary {
			t.Errorf("standard input posts %q, %s; the file posts %q, %s", piped.Title, piped.Conclusion, file.Title, file.Conclusion)
		}
	})

	t.Run("export --format", func(t *testing.T) {
		code, stdout, stderr := execute(t, "export", "--format", "focus-csv", "--rows", exampleRows)
		if code != 0 || !strings.Contains(stdout, "BillingCurrency") {
			t.Fatalf("on the file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		piped, pipedOut, pipedErr := executeWith(t, readFile(t, exampleRows), "export", "--format", "focus-csv", "--rows", "-")
		if piped != code || pipedOut != stdout || pipedErr != stderr {
			t.Errorf("on standard input: exit %d, stdout %q, stderr %q; the file gives exit %d, %q, %q", piped, pipedOut, pipedErr, code, stdout, stderr)
		}
	})

	t.Run("export", func(t *testing.T) {
		stub := newPlatformStub(t, http.StatusAccepted, `{"batch_id":"903114-16873409921-1","status":"accepted"}`)
		code, _, stderr := stub.export(t, stub.githubEnv())
		if code != 0 || !strings.Contains(stderr, "sha256 ") {
			t.Fatalf("on the file: exit %d, stderr %q", code, stderr)
		}
		piped, _, pipedErr := stub.exportFrom(t, readFile(t, exampleRows), stub.githubEnv(), "--rows", "-")
		if piped != code || pipedErr != stderr {
			t.Errorf("on standard input: exit %d, stderr %q; the file gives exit %d, %q", piped, pipedErr, code, stderr)
		}
	})

	t.Run("reconcile", func(t *testing.T) {
		ledger := writeLedger(t, 100900)
		days := []string{"--from", "2026-10-14", "--to", "2026-10-14"}
		code, stdout, stderr := runReconcile(t, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, environment, append(days, "--rows", ledger)...)
		if code != 0 || !strings.Contains(stdout, "TOKENS") {
			t.Fatalf("on the file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		var pipedOut, pipedErr bytes.Buffer
		piped := reconcileCommand(context.Background(), append(days, "--rows", "-"), environment, &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}, fixedNow,
			stdio{stdin: strings.NewReader(readFile(t, ledger)), stdout: &pipedOut, stderr: &pipedErr})
		if piped != code || pipedOut.String() != stdout || pipedErr.String() != stderr {
			t.Errorf("on standard input: exit %d, stdout %q, stderr %q; the file gives exit %d, %q, %q", piped, pipedOut.String(), pipedErr.String(), code, stdout, stderr)
		}
	})
}

// heldInstant is the time the tests give a command whose output states when
// its verdict was reached, to the second. Two runs of one command line then
// print the same bytes, whatever the clock does between them. policy takes it
// with --at, and holdTheClock gives it to a preview, which has no such flag.
const heldInstant = "2026-11-21T09:00:00Z"

// holdTheClock holds the time of every preview in t to heldInstant.
func holdTheClock(t *testing.T) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, heldInstant)
	if err != nil {
		t.Fatal(err)
	}
	running := previewClock
	previewClock = func() time.Time { return at }
	t.Cleanup(func() { previewClock = running })
}

// besideADashFile moves the test to a directory that holds the rows as
// rows.jsonl, and beside them what a reader of a file named with a dash would
// find: a file of that name that holds no row, and a result object that
// states another cap. It returns the directory.
func besideADashFile(t *testing.T, rows string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rows.jsonl"), rows)
	writeFile(t, filepath.Join(dir, standardInput), "this line is no row\n")
	writeFile(t, filepath.Join(dir, runResultPath(standardInput)), `{"cap":{"currency":"USD","amount":"2"}}`+"\n")
	t.Chdir(dir)
	return dir
}

// TestRowsOnStandardInputAreNamedAsStandardInput holds the lines that name
// the rows. A line that says a file is kept says so of a file alone.
func TestRowsOnStandardInputAreNamedAsStandardInput(t *testing.T) {
	t.Run("no rows", func(t *testing.T) {
		code, stdout, stderr := executeWith(t, "", "export", "--format", "focus-csv", "--rows", "-")
		if want := "costroid: standard input holds no rows, so nothing was written\n"; code != 0 || stdout != "" || stderr != want {
			t.Errorf("exit %d, stdout %q, stderr %q, want %q", code, stdout, stderr, want)
		}
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
		code, _, stderr = executeWith(t, "", "emit-otlp", "--rows", "-")
		if want := "costroid: standard input holds no rows, so nothing was sent\n"; code != 0 || stderr != want {
			t.Errorf("emit-otlp: exit %d, stderr %q, want %q", code, stderr, want)
		}
	})
	t.Run("a refusal that keeps a file", func(t *testing.T) {
		stub := newPlatformStub(t, http.StatusPaymentRequired, `{"error":{"code":"PLAN_WRITES_REFUSED","message":"Nothing was stored."}}`)
		code, _, stderr := stub.exportFrom(t, readFile(t, exampleRows), stub.githubEnv(), "--rows", "-")
		if want := "costroid: warning: Costroid Platform refused batch 903114-16873409921-1: 402 PLAN_WRITES_REFUSED. Nothing in the rows is wrong\n"; code != 0 || stderr != want {
			t.Errorf("exit %d, stderr %q, want %q", code, stderr, want)
		}
	})
}

// TestFollowUpHoldsStandardInputToItsCap gives follow-up one byte more than
// it reads of a rows file. It must post the reason and read no row.
func TestFollowUpHoldsStandardInputToItsCap(t *testing.T) {
	stub := followUpEnvironment(t, "stranger/widgets")
	code, _, stderr := executeWith(t, strings.Repeat("\n", maxFollowUpRows+1), "follow-up", "--rows", "-")
	if code != 0 || len(stub.Checks) != 1 {
		t.Fatalf("exit %d, %d checks: %s", code, len(stub.Checks), stderr)
	}
	if want := fmt.Sprintf("costroid: warning: the rows file holds %d bytes, over the follow-up's cap of %d, so it was not read\n", maxFollowUpRows+1, maxFollowUpRows); !strings.Contains(stderr, want) {
		t.Errorf("stderr %q lacks %q", stderr, want)
	}
	if !strings.HasPrefix(stub.Checks[0].Title, "No figure (") {
		t.Errorf("the check's title is %q", stub.Checks[0].Title)
	}
}

// TestTheMCPServerReadsNoRowsFromItsOwnStandardInput holds the policy tool to
// a file: the server's standard input carries the session.
func TestTheMCPServerReadsNoRowsFromItsOwnStandardInput(t *testing.T) {
	_, err := policyAnswer(context.Background(), mcpserver.PolicyRequest{Rows: standardInput})
	if err == nil || !strings.Contains(err.Error(), "rows names a file") {
		t.Errorf("the policy tool on a dash: %v", err)
	}
}

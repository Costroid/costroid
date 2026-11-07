package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/run"
)

type ingestRunFlags struct {
	executionFile string
	codexHome     string
	// since is when the job started. Zero reads every file in codexHome.
	since   time.Time
	out     string
	account accountFlags
}

func parseIngestRun(args []string, stderr io.Writer) (ingestRunFlags, error) {
	set := flag.NewFlagSet("ingest-run", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	executionFile := set.String("execution-file", "", "the execution_file output of claude-code-action")
	codexHome := set.String("codex-home", "", "the codex-home given to openai/codex-action")
	since := set.String("since", "", "with --codex-home, when the job started, in RFC 3339")
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines")
	account := addAccountFlags(set)
	if err := set.Parse(args); err != nil {
		return ingestRunFlags{}, err
	}
	switch {
	case *executionFile != "" && *codexHome != "":
		return ingestRunFlags{}, errors.New("ingest-run takes --execution-file or --codex-home, not both")
	case *codexHome != "" && (*out == "" || set.NArg() != 0):
		return ingestRunFlags{}, errors.New("ingest-run needs --codex-home and --out")
	case *codexHome == "" && (*executionFile == "" || *out == "" || set.NArg() != 0):
		return ingestRunFlags{}, errors.New("ingest-run needs --execution-file and --out")
	case *since != "" && *codexHome == "":
		return ingestRunFlags{}, errors.New("ingest-run takes --since only with --codex-home")
	}
	flags := ingestRunFlags{executionFile: *executionFile, codexHome: *codexHome, out: *out, account: account}
	if *since != "" {
		at, err := time.Parse(time.RFC3339Nano, *since)
		if err != nil {
			return ingestRunFlags{}, fmt.Errorf("ingest-run takes --since as an RFC 3339 time, not %q", *since)
		}
		flags.since = at
	}
	if err := account.check(); err != nil {
		return ingestRunFlags{}, err
	}
	return flags, nil
}

// endedRun is a run that already ended, as ingest-run reads it.
type endedRun interface {
	// agent is the costroid/agent tag of the run's rows.
	agent() string
	// read reads the run. A refusal is a run.ReaderError, so its first line
	// can reach the report.
	read() (adapter.Report, error)
	close() error
}

// openEndedRun opens what the flags name. Nothing is written before it is
// open, so a path that names nothing leaves no rows file.
func openEndedRun(flags ingestRunFlags) (endedRun, error) {
	if flags.codexHome == "" {
		return openExecutionFile(flags.executionFile)
	}
	info, err := os.Stat(flags.codexHome)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--codex-home %s is not a directory", flags.codexHome)
	}
	return codexHome{dir: flags.codexHome, since: flags.since}, nil
}

// codexHome is the codex-home an openai/codex-action step wrote its rollout
// files to.
type codexHome struct {
	dir   string
	since time.Time
}

func (codexHome) agent() string { return codex.Agent{}.Name() }

func (h codexHome) read() (adapter.Report, error) {
	report, err := codex.Agent{}.ReadHome(h.dir, h.since)
	if err != nil {
		return adapter.Report{}, &run.ReaderError{Err: err}
	}
	return report, nil
}

func (codexHome) close() error { return nil }

// openedExecution is the file claude-code-action wrote, open, and when it was
// saved.
type openedExecution struct {
	file  *os.File
	saved time.Time
}

func openExecutionFile(path string) (endedRun, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat %s: %w", path, err), file.Close())
	}
	return openedExecution{file: file, saved: info.ModTime().UTC().Truncate(time.Second)}, nil
}

func (openedExecution) agent() string { return claudecode.Name }

func (e openedExecution) close() error { return e.file.Close() }

// read reads the file. A file the reader refuses is a run.ReaderError, so its
// first line can reach the report.
func (e openedExecution) read() (adapter.Report, error) {
	report, err := claudecode.Agent{}.ReadExecutionFile(e.file, e.saved)
	if err != nil {
		return adapter.Report{}, &run.ReaderError{Err: err}
	}
	return report, nil
}

func ingestRunCommand(args []string, stderr io.Writer) int {
	flags, err := parseIngestRun(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil {
		err = ingestRun(flags, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

// ingestRun writes the rows and, beside them, the result object, as costroid
// run does. Once the execution file or the Codex home is open, the rows file
// and the object are written whatever the reader makes of it, so the report
// can say why no rows came.
func ingestRun(flags ingestRunFlags, stderr io.Writer) error {
	pinned, err := catalog.Load()
	if err != nil {
		return err
	}
	// An object an earlier run left would give these rows its reason.
	if err := os.Remove(runResultPath(flags.out)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	ended, err := openEndedRun(flags)
	if err != nil {
		return err
	}
	// A file opened for reading has nothing left to flush.
	defer func() { _ = ended.close() }()
	rows, err := os.Create(flags.out)
	if err != nil {
		return err
	}
	ci := readCI(stderr)
	record := runRecord{out: flags.out, head: ci.Head}
	result, err := recordEndedRun(flags, pinned, ci, ended, rows)
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	record.reason = result.Reason
	record.agentError, record.ciRun = result.AgentError, ciRunFromEnvironment(os.Getenv)
	if err != nil {
		recordRunResult(io.Discard, stderr, record.failed(err))
		return err
	}
	recordRunResult(io.Discard, stderr, record)
	summarizeRows(stderr, flags.out, pinned.Version(), result)
	fmt.Fprintf(stderr, "costroid: %s\n", result.Enforcement.Note)
	warnAccount(stderr, flags.account)
	for _, warning := range result.Report.Warnings {
		fmt.Fprintf(stderr, "costroid: warning: %s\n", warning)
	}
	return nil
}

func recordEndedRun(flags ingestRunFlags, pinned *catalog.Catalog, ci cicontext.Context, ended endedRun, rows io.Writer) (run.Result, error) {
	report, err := ended.read()
	if err != nil {
		return run.Result{}, err
	}
	return run.Record(run.Options{
		BillingAccount: *flags.account.billing,
		Credential:     *flags.account.credential,
		CI:             ci,
		Rows:           rows,
		Catalog:        pinned,
	}, ended.agent(), report)
}

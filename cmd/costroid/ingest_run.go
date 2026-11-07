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
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/run"
)

type ingestRunFlags struct {
	executionFile string
	out           string
	account       accountFlags
}

func parseIngestRun(args []string, stderr io.Writer) (ingestRunFlags, error) {
	set := flag.NewFlagSet("ingest-run", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	executionFile := set.String("execution-file", "", "the execution_file output of claude-code-action")
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines")
	account := addAccountFlags(set)
	if err := set.Parse(args); err != nil {
		return ingestRunFlags{}, err
	}
	if *executionFile == "" || *out == "" || set.NArg() != 0 {
		return ingestRunFlags{}, errors.New("ingest-run needs --execution-file and --out")
	}
	if err := account.check(); err != nil {
		return ingestRunFlags{}, err
	}
	return ingestRunFlags{executionFile: *executionFile, out: *out, account: account}, nil
}

// openedExecution is the file claude-code-action wrote, open, and when it was
// saved.
type openedExecution struct {
	file  *os.File
	saved time.Time
}

func openExecutionFile(path string) (openedExecution, error) {
	file, err := os.Open(path)
	if err != nil {
		return openedExecution{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return openedExecution{}, errors.Join(fmt.Errorf("stat %s: %w", path, err), file.Close())
	}
	return openedExecution{file: file, saved: info.ModTime().UTC().Truncate(time.Second)}, nil
}

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
// run does. Once the execution file is open, the rows file and the object are
// written whatever the reader makes of it, so the report can say why no rows
// came.
func ingestRun(flags ingestRunFlags, stderr io.Writer) error {
	pinned, err := catalog.Load()
	if err != nil {
		return err
	}
	// An object an earlier run left would give these rows its reason.
	if err := os.Remove(runResultPath(flags.out)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	execution, err := openExecutionFile(flags.executionFile)
	if err != nil {
		return err
	}
	defer execution.file.Close()
	rows, err := os.Create(flags.out)
	if err != nil {
		return err
	}
	ci := readCI(stderr)
	record := runRecord{out: flags.out, head: ci.Head}
	result, err := recordExecution(flags, pinned, ci, execution, rows)
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	record.reason = result.Reason
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

func recordExecution(flags ingestRunFlags, pinned *catalog.Catalog, ci cicontext.Context, execution openedExecution, rows io.Writer) (run.Result, error) {
	report, err := execution.read()
	if err != nil {
		return run.Result{}, err
	}
	return run.Record(run.Options{
		BillingAccount: *flags.account.billing,
		Credential:     *flags.account.credential,
		CI:             ci,
		Rows:           rows,
		Catalog:        pinned,
	}, claudecode.Name, report)
}

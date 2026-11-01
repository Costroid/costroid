package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/catalog"
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

func readExecutionFile(path string) (adapter.Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return adapter.Report{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return adapter.Report{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return claudecode.Agent{}.ReadExecutionFile(file, info.ModTime().UTC().Truncate(time.Second))
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

func ingestRun(flags ingestRunFlags, stderr io.Writer) error {
	pinned, err := catalog.Load()
	if err != nil {
		return err
	}
	report, err := readExecutionFile(flags.executionFile)
	if err != nil {
		return err
	}
	rows, err := os.Create(flags.out)
	if err != nil {
		return err
	}
	result, err := run.Record(run.Options{
		BillingAccount: *flags.account.billing,
		Credential:     *flags.account.credential,
		CI:             readCI(stderr),
		Rows:           rows,
		Catalog:        pinned,
	}, claudecode.Name, report)
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	if err != nil {
		return err
	}
	summarizeRows(stderr, flags.out, pinned.Version(), result)
	fmt.Fprintf(stderr, "costroid: %s\n", result.Enforcement.Note)
	warnAccount(stderr, flags.account)
	for _, warning := range report.Warnings {
		fmt.Fprintf(stderr, "costroid: warning: %s\n", warning)
	}
	return nil
}

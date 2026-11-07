package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/run"
)

type ingestRunFlags struct {
	source givenSource
	// since is when the job started. Zero reads every file the source holds.
	since   time.Time
	out     string
	account accountFlags
}

// sourceKind is one kind of thing a finished run leaves that ingest-run reads.
type sourceKind struct {
	// name is KIND in --from KIND=PATH.
	name string
	// alias says --NAME PATH reads as --from NAME=PATH, as it did before
	// --from existed.
	alias bool
	// what says what PATH names.
	what string
	// since says the kind takes --since.
	since bool
	open  func(path string, since time.Time) (endedRun, error)
}

// sourceKinds is every source ingest-run reads, one row each. ingest-run
// reads exactly one, however it is named.
var sourceKinds = []sourceKind{
	{name: "execution-file", alias: true, what: "the execution_file output of anthropics/claude-code-action", open: openExecutionFile},
	{name: "codex-home", alias: true, what: "the codex-home given to openai/codex-action", since: true, open: openCodexHome},
}

// givenSource is one source the command line names.
type givenSource struct {
	kind *sourceKind
	path string
}

// sourcesFlag gathers every source named, by --from or by an alias.
type sourcesFlag struct{ given []givenSource }

func (f *sourcesFlag) add(kind *sourceKind, path string) error {
	if path == "" {
		return fmt.Errorf("%s takes a path", kind.name)
	}
	f.given = append(f.given, givenSource{kind: kind, path: path})
	return nil
}

// from reads KIND=PATH.
func (f *sourcesFlag) from(value string) error {
	name, path, found := strings.Cut(value, "=")
	for i := range sourceKinds {
		if found && sourceKinds[i].name == name {
			return f.add(&sourceKinds[i], path)
		}
	}
	return fmt.Errorf("takes KIND=PATH, with KIND one of %s", kindNames(sourceKinds, "or"))
}

// kindNames lists the kinds in prose, the last joined by word.
func kindNames(kinds []sourceKind, word string) string {
	names := make([]string, len(kinds))
	for i, kind := range kinds {
		names[i] = kind.name
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + word + " " + names[len(names)-1]
}

func parseIngestRun(args []string, stderr io.Writer) (ingestRunFlags, error) {
	set := flag.NewFlagSet("ingest-run", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	var sources sourcesFlag
	set.Func("from", "KIND=PATH, the source to read", sources.from)
	var sinceKinds []sourceKind
	for i := range sourceKinds {
		kind := &sourceKinds[i]
		if kind.alias {
			set.Func(kind.name, kind.what, func(path string) error { return sources.add(kind, path) })
		}
		if kind.since {
			sinceKinds = append(sinceKinds, *kind)
		}
	}
	since := set.String("since", "", "when the job started, in RFC 3339")
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines")
	account := addAccountFlags(set)
	if err := set.Parse(args); err != nil {
		return ingestRunFlags{}, err
	}
	given := sources.given
	switch {
	case len(given) > 1:
		names := make([]string, len(given))
		for i, source := range given {
			names[i] = source.kind.name
		}
		return ingestRunFlags{}, fmt.Errorf("ingest-run reads one source, and was given %d: %s", len(given), strings.Join(names, ", "))
	case len(given) == 0 || *out == "" || set.NArg() != 0:
		return ingestRunFlags{}, fmt.Errorf("ingest-run needs --from KIND=PATH and --out, with KIND one of %s", kindNames(sourceKinds, "or"))
	case *since != "" && !given[0].kind.since:
		return ingestRunFlags{}, fmt.Errorf("ingest-run takes --since only with %s", kindNames(sinceKinds, "or"))
	}
	flags := ingestRunFlags{source: given[0], out: *out, account: account}
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
	return flags.source.kind.open(flags.source.path, flags.since)
}

func openCodexHome(dir string, since time.Time) (endedRun, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("codex-home %s is not a directory", dir)
	}
	return codexHome{dir: dir, since: since}, nil
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

func openExecutionFile(path string, _ time.Time) (endedRun, error) {
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
// run does. Once the source is open, the rows file and the object are written
// whatever the reader makes of it, so the report can say why no rows came.
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

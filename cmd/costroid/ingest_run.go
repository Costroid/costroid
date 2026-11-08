package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

type ingestRunFlags struct {
	source givenSource
	// since is when the job started. Zero reads every file the source holds.
	since   time.Time
	out     string
	account accountFlags
	// shape is what --tag and --focus-profile add to every row.
	shape focus.Shape
	// cap is what the workflow states about the cap the run was held to.
	cap claudecode.Cap
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
	// capped says the kind takes --cap-usd and --claude-args.
	capped bool
	open   func(path string, since time.Time) (endedRun, error)
}

// sourceKinds is every source ingest-run reads, one row each. ingest-run
// reads exactly one, however it is named.
var sourceKinds = []sourceKind{
	{name: "execution-file", alias: true, what: "the execution_file output of anthropics/claude-code-action", capped: true, open: openExecutionFile},
	{name: "codex-home", alias: true, what: "the codex-home given to openai/codex-action", since: true, open: openCodexHome},
	{name: "gh-aw", what: "the directory a gh-aw run's usage artifact was downloaded to", open: openGhAwUsage},
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

func parseIngestRun(args []string, stdout, stderr io.Writer) (ingestRunFlags, error) {
	set := flag.NewFlagSet("ingest-run", flag.ContinueOnError)
	set.SetOutput(stderr)
	var sources sourcesFlag
	set.Func("from", "KIND=PATH, the source to read", sources.from)
	var sinceKinds, cappedKinds []sourceKind
	for i := range sourceKinds {
		kind := &sourceKinds[i]
		if kind.alias {
			set.Func(kind.name, kind.what, func(path string) error { return sources.add(kind, path) })
		}
		if kind.since {
			sinceKinds = append(sinceKinds, *kind)
		}
		if kind.capped {
			cappedKinds = append(cappedKinds, *kind)
		}
	}
	since := set.String("since", "", "when the job started, in RFC 3339")
	capUSD := set.String("cap-usd", "", "the cap the workflow held the run to, in USD")
	claudeArgs := set.String("claude-args", "", "the claude_args the claude-code-action step was given")
	out := set.String("out", "", "the file the FOCUS rows go to, as JSON lines")
	account := addAccountFlags(set)
	shaping := addRowFlags(set)
	if err := parseFlags(set, args, ingestRunHelp, stdout, stderr); err != nil {
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
	case (*capUSD != "" || *claudeArgs != "") && !given[0].kind.capped:
		return ingestRunFlags{}, fmt.Errorf("ingest-run takes --cap-usd and --claude-args only with %s", kindNames(cappedKinds, "or"))
	case *claudeArgs != "" && *capUSD == "":
		return ingestRunFlags{}, errors.New("ingest-run takes --claude-args only with --cap-usd, the cap they are held to")
	}
	flags := ingestRunFlags{source: given[0], out: *out, account: account}
	if *since != "" {
		at, err := time.Parse(time.RFC3339Nano, *since)
		if err != nil {
			return ingestRunFlags{}, fmt.Errorf("ingest-run takes --since as an RFC 3339 time, not %q", *since)
		}
		flags.since = at
	}
	if *capUSD != "" {
		amount, err := decimal.Parse(*capUSD)
		if err != nil || amount.Sign() <= 0 {
			return ingestRunFlags{}, fmt.Errorf("ingest-run takes --cap-usd as a USD figure above 0, not %q", *capUSD)
		}
		flags.cap = claudecode.Cap{USD: &amount, Args: *claudeArgs}
	}
	if err := account.check(); err != nil {
		return ingestRunFlags{}, err
	}
	shape, err := shaping.shape()
	if err != nil {
		return ingestRunFlags{}, err
	}
	flags.shape = shape
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

// openEndedRun opens what the flags name, and gives an execution file the
// cap the workflow states. Nothing is written before it is open, so a path
// that names nothing leaves no rows file.
func openEndedRun(flags ingestRunFlags) (endedRun, error) {
	ended, err := flags.source.kind.open(flags.source.path, flags.since)
	if execution, ok := ended.(openedExecution); ok && err == nil {
		execution.stated = flags.cap
		return execution, nil
	}
	return ended, err
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

func openGhAwUsage(dir string, _ time.Time) (endedRun, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("gh-aw %s does not exist: the run uploaded no usage artifact, or its download failed", dir)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("gh-aw %s is not a directory: download the run's usage artifact into a directory of its own", dir)
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return ghawUsage{dir: dir, info: filepath.Join(filepath.Dir(absolute), ghaw.InfoArtifact)}, nil
}

// ghawUsage is the usage artifact of a gh-aw run, downloaded, and the
// directory beside it that its info artifact is read from, where gh run
// download puts it when it downloads both. The run wrote them, so they are
// read as data and nothing in them is run.
type ghawUsage struct {
	dir  string
	info string
}

func (ghawUsage) agent() string { return ghaw.Name }

func (u ghawUsage) read() (adapter.Report, error) {
	info, err := u.infoArtifact()
	if err != nil {
		return adapter.Report{}, &run.ReaderError{Err: err}
	}
	report, err := ghaw.ReadUsage(os.DirFS(u.dir), info)
	if err != nil {
		return adapter.Report{}, &run.ReaderError{Err: err}
	}
	return report, nil
}

// infoArtifact is the info directory beside the usage artifact, nil when there
// is none. A link or a file in its place is refused.
func (u ghawUsage) infoArtifact() (fs.FS, error) {
	info, err := os.Lstat(u.info)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("gh-aw: read the info artifact: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("gh-aw: the %s beside the usage artifact is not a directory: download the run's info artifact into a directory of its own", ghaw.InfoArtifact)
	}
	return os.DirFS(u.info), nil
}

func (ghawUsage) close() error { return nil }

// openedExecution is the file claude-code-action wrote, open, when it was
// saved, and the cap the workflow states for its run.
type openedExecution struct {
	file   *os.File
	saved  time.Time
	stated claudecode.Cap
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
	report, err := claudecode.Agent{}.ReadCappedExecutionFile(e.file, e.saved, e.stated)
	if err != nil {
		return adapter.Report{}, &run.ReaderError{Err: err}
	}
	return report, nil
}

func ingestRunCommand(args []string, stdout, stderr io.Writer) int {
	flags, err := parseIngestRun(args, stdout, stderr)
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
	record := runRecord{out: flags.out, budget: flags.cap.USD, head: ci.Head}
	result, err := recordEndedRun(flags, pinned, ci, ended, rows)
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.out, closeErr)
	}
	record.reason = result.Reason
	record.agentError, record.ciRun = result.AgentError, ciRunFromEnvironment(os.Getenv)
	record.agentStated = result.Report.AgentStated
	if err != nil {
		recordRunResult(io.Discard, stderr, record.failed(err))
		return err
	}
	recordRunResult(io.Discard, stderr, record)
	summarizeRows(stderr, flags.out, pinned.Version(), result)
	fmt.Fprintf(stderr, "costroid: %s\n", result.Enforcement.Note)
	if stated := result.Report.AgentStated; stated != nil {
		fmt.Fprintf(stderr, "costroid: %s stated %s AI credits for this run. Costroid does not convert credits.\n", stated.Source, stated.Quantity)
	}
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
		Rows:           focus.Shaped(rows, flags.shape),
		Catalog:        pinned,
	}, ended.agent(), report)
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reconcile"
)

const (
	exitTokensMiss = 1

	anthropicKeyVariable = "ANTHROPIC_ADMIN_KEY"
	openAIKeyVariable    = "OPENAI_ADMIN_KEY"

	storedRowsMode = 0o644
)

// clock is what a reconciliation reads the time from.
type clock func() time.Time

type reconcileFlags struct {
	rows       string
	credential string
	upstream   map[string]string
	out        string
	days       reconcile.Range
	reports    string
	asOf       time.Time
}

// upstreamSources are the sources an upstream credential can be named for.
var upstreamSources = []string{reconcile.SourceAnthropic, reconcile.SourceOpenAI}

// upstreamFlag reads --upstream-credential SOURCE=ID, once per source.
func upstreamFlag(into map[string]string) func(string) error {
	return func(value string) error {
		source, id, found := strings.Cut(value, "=")
		if !found || id == "" {
			return fmt.Errorf("%q must read SOURCE=ID, such as anthropic=apikey_01Example", value)
		}
		if !slices.Contains(upstreamSources, source) {
			return fmt.Errorf("source %q must be one of %s", source, strings.Join(upstreamSources, ", "))
		}
		if _, named := into[source]; named {
			return fmt.Errorf("%s is named twice: a gateway holds one key per provider", source)
		}
		into[source] = id
		return nil
	}
}

func parseReconcile(args []string, stderr io.Writer) (reconcileFlags, error) {
	set := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	rows := set.String("rows", "", "the FOCUS rows, as JSON lines")
	from := set.String("from", "", "the first UTC day, YYYY-MM-DD")
	to := set.String("to", "", "the last UTC day, YYYY-MM-DD")
	credential := set.String("credential", "", "the credential of rows whose SubAccountId is null")
	upstream := map[string]string{}
	set.Func("upstream-credential", "SOURCE=ID: the provider key a gateway holds, which rows drawn on its run keys are compared under", upstreamFlag(upstream))
	out := set.String("out", "", "the file reconciliation rows are kept in, one per source, credential and day")
	reports := set.String("reports", "", "a directory of saved provider reports, read in place of the provider")
	asOf := set.String("as-of", "", "when the saved reports were saved, as a UTC instant")
	if err := set.Parse(args); err != nil {
		return reconcileFlags{}, err
	}
	if *rows == "" || *from == "" || *to == "" || set.NArg() != 0 {
		return reconcileFlags{}, errors.New("reconcile needs --rows, --from and --to")
	}
	days, err := reconcile.ParseRange(*from, *to)
	if err != nil {
		return reconcileFlags{}, err
	}
	flags := reconcileFlags{rows: *rows, credential: *credential, upstream: upstream, out: *out, days: days, reports: *reports}
	if (*reports == "") != (*asOf == "") {
		return reconcileFlags{}, errors.New("--reports and --as-of go together")
	}
	if *asOf != "" {
		if flags.asOf, err = time.Parse(focus.InstantLayout, *asOf); err != nil {
			return reconcileFlags{}, errors.New("--as-of must read like 2026-11-04T00:00:00Z")
		}
	}
	return flags, nil
}

// fromSavedReports swaps the provider for a directory. A saved report is read
// as of the instant it was saved, because that is what decides which days it
// could have finished.
func fromSavedReports(flags reconcileFlags, getenv func(string) string, client reconcile.Doer, now clock) (func(string) string, reconcile.Doer, clock) {
	if flags.reports == "" {
		return getenv, client, now
	}
	noKey := func(string) string { return savedReportsHoldNoKey }
	return noKey, &savedReports{directory: flags.reports}, func() time.Time { return flags.asOf }
}

func reconcileCommand(ctx context.Context, args []string, getenv func(string) string, client reconcile.Doer, now clock, stdout, stderr io.Writer) int {
	flags, err := parseReconcile(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	result, err := reconcileRows(ctx, flags, getenv, client, now, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if err := reconcile.Render(stdout, result); err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if result.Misses() > 0 {
		return exitTokensMiss
	}
	return 0
}

func reconcileRows(ctx context.Context, flags reconcileFlags, getenv func(string) string, client reconcile.Doer, now clock, stderr io.Writer) (reconcile.Result, error) {
	getenv, client, now = fromSavedReports(flags, getenv, client, now)
	ledger, err := readLedger(flags)
	if err != nil {
		return reconcile.Result{}, err
	}
	if ledger.Uncredentialed > 0 {
		return reconcile.Result{}, fmt.Errorf("%d rows have a null SubAccountId: pass --credential to name their credential", ledger.Uncredentialed)
	}
	for _, source := range upstreamSources {
		if count := ledger.Gatewayed[source]; count > 0 {
			return reconcile.Result{}, fmt.Errorf("%d %s rows were drawn on a gateway's run keys, which the provider's report never names: pass --upstream-credential %s=ID with the id of the key the gateway sends with", count, source, source)
		}
	}
	if ledger.Repeats > 0 {
		fmt.Fprintf(stderr, "costroid: warning: %d repeated rows in %s were counted once\n", ledger.Repeats, flags.rows)
	}
	providers, err := providersFor(ledger, getenv, client)
	if err != nil {
		return reconcile.Result{}, err
	}
	pinned, err := catalog.Load()
	if err != nil {
		return reconcile.Result{}, err
	}
	result, err := reconcile.Reconcile(ctx, ledger, reconcile.Options{
		Range:     flags.days,
		Now:       now().UTC(),
		Catalog:   pinned,
		Providers: providers,
	})
	if err != nil {
		return reconcile.Result{}, err
	}
	if flags.out != "" {
		if err := storeRows(flags.out, result.Rows); err != nil {
			return reconcile.Result{}, err
		}
	}
	return result, nil
}

func readLedger(flags reconcileFlags) (*reconcile.Ledger, error) {
	rows, err := os.Open(flags.rows)
	if err != nil {
		return nil, err
	}
	ledger, err := reconcile.ReadLedgerWith(rows, flags.days, reconcile.LedgerOptions{Credential: flags.credential, Upstream: flags.upstream})
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", flags.rows, closeErr)
	}
	return ledger, err
}

// providersFor reads each admin key from this process's own environment. A key
// is asked for only when the ledger holds that provider's rows.
func providersFor(ledger *reconcile.Ledger, getenv func(string) string, client reconcile.Doer) ([]reconcile.Provider, error) {
	constructors := map[string]struct {
		variable string
		build    func(reconcile.Doer, string) reconcile.Provider
	}{
		reconcile.SourceAnthropic: {anthropicKeyVariable, reconcile.NewAnthropic},
		reconcile.SourceOpenAI:    {openAIKeyVariable, reconcile.NewOpenAI},
	}
	var providers []reconcile.Provider
	for _, source := range ledger.Sources() {
		constructor := constructors[source]
		key := getenv(constructor.variable)
		if key == "" {
			return nil, fmt.Errorf("%s is not set: the %s report needs an admin key", constructor.variable, source)
		}
		providers = append(providers, constructor.build(client, key))
	}
	return providers, nil
}

// storeRows replaces the file in one rename, so a failed run leaves the last
// good rows in place.
func storeRows(path string, rows []reconcile.Row) error {
	var current io.Reader = strings.NewReader("")
	existing, err := os.Open(path)
	switch {
	case err == nil:
		defer existing.Close()
		current = existing
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := reconcile.Merge(current, rows, temporary); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Chmod(storedRowsMode); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", temporary.Name(), err)
	}
	return os.Rename(temporary.Name(), path)
}

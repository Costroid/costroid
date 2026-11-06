// Command accuracy keeps the inputs of one day's reconciliation, writes the
// accuracy page from every day kept, and checks the page against them.
//
// No line of the page is read back as a figure. The page is recomputed from
// the kept inputs by `costroid reconcile` every time, and a page that differs
// from its recomputation by one byte is refused.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"
)

const usage = `Usage:
  go run ./scripts/accuracy -save DIR -day YYYY-MM-DD -rows FILE [-credential ID]
  go run ./scripts/accuracy -write FILE [-inputs DIR] [-costroid BINARY]
  go run ./scripts/accuracy -check FILE [-days N] [-inputs DIR] [-costroid BINARY]

-save keeps one UTC day's inputs in DIR: the rows, the provider's usage and
cost reports cut to the credentials the rows name, and when the reports were
saved. It reads ANTHROPIC_ADMIN_KEY and OPENAI_ADMIN_KEY. When a report's key
is not set, it records that no provider report was saved and exits 0.

-write re-runs costroid reconcile on each day kept under -inputs, one
directory per day, and writes the page. -check recomputes the page the same
way, refuses it when one line differs, and passes only when the last -days
days on it are consecutive UTC days that each passed.

-costroid names the binary that reconciles. Without it, the command builds
cmd/costroid from this module.

Exit codes: 0 done or passed, 1 the page was refused, 125 the inputs could not
be read or the reports could not be saved.
`

const (
	exitRefused = 1
	exitFailure = 125

	defaultInputs = "accuracy-inputs"
	defaultDays   = 14
)

// errRefused marks a page that was read and turned down, as against inputs
// that could not be read.
var errRefused = errors.New("refused")

type options struct {
	save, day, rows, credential string
	write, check                string
	inputs, costroid            string
	days                        int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], world{getenv: os.Getenv, client: liveClient(), now: time.Now}, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// world is what -save reads from outside the process.
type world struct {
	getenv func(string) string
	client doer
	now    func() time.Time
}

func run(ctx context.Context, args []string, outside world, stdout, stderr io.Writer) int {
	opts, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "accuracy: %v\n", err)
		return exitFailure
	}
	switch {
	case opts.save != "":
		err = save(ctx, opts, outside, stdout)
	case opts.write != "":
		err = writePage(ctx, opts, stdout, stderr)
	default:
		err = checkPage(ctx, opts, stdout, stderr)
	}
	if errors.Is(err, errRefused) {
		fmt.Fprintf(stderr, "accuracy: %v\n", err)
		return exitRefused
	}
	if err != nil {
		fmt.Fprintf(stderr, "accuracy: %v\n", err)
		return exitFailure
	}
	return 0
}

func parse(args []string, stderr io.Writer) (options, error) {
	var opts options
	set := flag.NewFlagSet("accuracy", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	set.StringVar(&opts.save, "save", "", "the directory one day's inputs are kept in")
	set.StringVar(&opts.day, "day", "", "the UTC day to save, YYYY-MM-DD")
	set.StringVar(&opts.rows, "rows", "", "the rows of that day, as JSON lines")
	set.StringVar(&opts.credential, "credential", "", "the credential of rows whose SubAccountId is null")
	set.StringVar(&opts.write, "write", "", "the page to write")
	set.StringVar(&opts.check, "check", "", "the page to check")
	set.StringVar(&opts.inputs, "inputs", defaultInputs, "the directory of kept days, one directory per day")
	set.StringVar(&opts.costroid, "costroid", "", "the costroid binary that reconciles")
	set.IntVar(&opts.days, "days", defaultDays, "how many consecutive passing days -check needs")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected argument %q", set.Arg(0))
	}
	modes := 0
	for _, mode := range []string{opts.save, opts.write, opts.check} {
		if mode != "" {
			modes++
		}
	}
	switch {
	case modes != 1:
		return options{}, errors.New("give one of -save, -write and -check")
	case opts.save != "" && (opts.day == "" || opts.rows == ""):
		return options{}, errors.New("-save needs -day and -rows")
	case opts.check != "" && opts.days < 1:
		return options{}, errors.New("-days must be 1 or more")
	}
	return opts, nil
}

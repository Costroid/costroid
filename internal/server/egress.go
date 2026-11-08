package server

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/store"
)

// EgressUsage is egress' help page. costroid help egress prints it on stdout,
// and a command line egress cannot read prints it on stderr.
const EgressUsage = `Usage:
  costroid egress report [--json]

egress report prints the service's outbound calls: one line per UTC day, host
and outcome, which is completed, failed or refused. A refused call never
opened a socket. The report holds no path, query, header or body. A service
writes its counters every 5 seconds. --json prints the counters as one JSON
object.

Environment:
  COSTROID_DATABASE_URL  the application role's connection

Exit codes of egress:
  0                 the report was printed
  125               costroid could not read the command line or the database
`

const noEgress = "No outbound call has been recorded."

// EgressMain runs costroid egress and returns an exit code.
func EgressMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := egressCommand(ctx, args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

func egressCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, EgressUsage)
		return errors.New("egress needs report")
	}
	switch args[0] {
	case "report":
		return egressReport(ctx, args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, EgressUsage)
		return flag.ErrHelp
	}
	return fmt.Errorf("unknown egress command %q", args[0])
}

func egressReport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("egress report", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, EgressUsage) }
	asJSON := set.Bool("json", false, "print JSON")
	if err := set.Parse(args); err != nil {
		return err
	}
	databaseURL, err := required(envDatabase)
	if err != nil {
		return err
	}
	ledger, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer ledger.Close()
	counts, err := ledger.Egress(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(struct {
			Counters []egress.Count `json:"counters"`
		}{counts})
	}
	return printEgress(stdout, counts)
}

func printEgress(w io.Writer, counts []egress.Count) error {
	if len(counts) == 0 {
		_, err := fmt.Fprintln(w, noEgress)
		return err
	}
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "DAY\tHOST\tOUTCOME\tCOUNT")
	for _, count := range counts {
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\n", count.Day, count.Host, count.Outcome, count.Count)
	}
	return table.Flush()
}

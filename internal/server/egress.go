package server

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/store"
)

// EgressUsage is egress' help page. costroid help egress prints it on stdout.
// egressGroup is what costroid egress prints with no subcommand.
var egressGroup = cli.Group{
	Command:     "egress",
	Summary:     "egress report prints the service's outbound calls: one line per UTC day, host and outcome, which is completed, failed or refused.",
	Subcommands: []cli.Row{{"report", "print the calls by day, host and outcome"}},
	Example:     []string{"costroid egress report"},
}

var EgressUsage = cli.Page{
	Command:  "egress",
	Summary:  egressGroup.Summary,
	Usage:    []string{"costroid egress report [--json]"},
	Examples: []string{"costroid egress report", "costroid egress report --json"},
	Flags:    []cli.Row{{"--json", "print the counters as one JSON object"}},
	Sections: []cli.Section{
		{Heading: "What the report holds", Body: []cli.Block{cli.Text(`A refused call never
opened a socket. The report holds no path, query, header or body. A service
writes its counters every 5 seconds. --json prints the counters as one JSON
object.`)}},
	},
	Environment: []cli.Block{cli.Rows{{envDatabase, "the application role's connection"}}},
	Exits: []cli.Row{
		{"0", "the report was printed"},
		cli.UsageExit,
		{"125", "costroid could not read the database"},
	},
	Docs: "ledger",
}.String()

const noEgress = "No outbound call has been recorded."

// EgressMain runs costroid egress and returns an exit code.
func EgressMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, egressGroup)
		return cli.ExitUsage
	}
	err := egressCommand(ctx, args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return cli.Fail(stderr, err)
	}
	return 0
}

func egressCommand(ctx context.Context, args []string, stdout io.Writer) error {
	switch args[0] {
	case "report":
		return egressReport(ctx, args[1:], stdout)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, EgressUsage)
		return flag.ErrHelp
	}
	return egressGroup.Unknown(args[0])
}

func egressReport(ctx context.Context, args []string, stdout io.Writer) error {
	set := flag.NewFlagSet("egress report", flag.ContinueOnError)
	asJSON := set.Bool("json", false, "print JSON")
	if err := cli.Parse(set, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, EgressUsage)
		}
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

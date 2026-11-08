package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
)

// demoRows are the rows ingest-run writes from a recorded claude-code-action
// run. demo/README.md names the recording.
//
//go:embed demo/rows.jsonl
var demoRows string

// demoCappedRows are the rows of a recorded Codex run that costroid stopped at
// a USD 0.001 cap. demo/README.md names the recording.
//
//go:embed demo/capped.jsonl
var demoCappedRows string

// The pull request the demo's comment is posted on. Its commit, check run,
// installation and CI run are the examples docs/comment-example.md shows; its
// figures are the recorded run's.
const (
	demoHead         = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	demoBase         = "1111111111111111111111111111111111111111"
	demoCheckRun     = int64(16873409921)
	demoInstallation = "58120394"
	demoRunURL       = "https://github.com/example/widgets/actions/runs/19203384756"
	demoConfig       = "policy:\n  run_budget_usd: \"5\"\n"
	// demoFailingConfig sets a run budget under the recorded run's cost.
	demoFailingConfig = "policy:\n  run_budget_usd: \"0.01\"\n"
	// demoCap is the cap the recorded Codex run was stopped at.
	demoCap = "0.001"
)

var (
	demoWorkflowAdded = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	demoCheckedAt     = time.Date(2026, 11, 7, 11, 40, 0, 0, time.UTC)
	// demoEnforcedAt is after the seven days in which a rule that fails only
	// warns, so a broken budget fails the check.
	demoEnforcedAt = time.Date(2026, 11, 10, 11, 40, 0, 0, time.UTC)
)

// The states demo shows with --state.
const (
	demoNeutral = "neutral"
	demoCapped  = "capped"
	demoFailure = "failure"
	demoSkipped = "skipped"
)

var demoStates = []string{demoNeutral, demoCapped, demoFailure, demoSkipped}

// demoCommand prints the comment report posts for a recorded run on a pull
// request, for a terminal or, under --markdown, as the Markdown it posts. It
// needs no CI system, no host and no key.
func demoCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("demo", flag.ContinueOnError)
	set.SetOutput(stderr)
	markdown := set.Bool("markdown", false, "print the comment as the Markdown report posts")
	state := set.String("state", demoNeutral, "the comment to show: "+strings.Join(demoStates, ", "))
	err := parseFlags(set, args, demoHelp, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil && set.NArg() > 0 {
		err = errors.New("demo takes no argument")
	}
	if err == nil && !slices.Contains(demoStates, *state) {
		err = fmt.Errorf("--state takes %s, not %q", strings.Join(demoStates, ", "), *state)
	}
	if err == nil {
		err = printDemo(ctx, *state, *markdown, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

func printDemo(ctx context.Context, state string, markdown bool, stdout io.Writer) error {
	body, err := demoStateComment(ctx, state)
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	if !markdown {
		body = forTerminal(body)
	}
	if _, err := io.WriteString(stdout, body); err != nil {
		return fmt.Errorf("demo: print the comment: %w", err)
	}
	return nil
}

// demoStateComment is the comment for one state. Each state's figures are a
// recorded run's; its pull request and costroid.yml are the example's.
func demoStateComment(ctx context.Context, state string) (string, error) {
	rows, config, at := demoRows, demoConfig, demoCheckedAt
	switch state {
	case demoCapped:
		rows = demoCappedRows
	case demoFailure:
		config, at = demoFailingConfig, demoEnforcedAt
	case demoSkipped:
		rows = ""
	}
	ledger, err := comment.ReadRows(strings.NewReader(rows))
	if err != nil {
		return "", err
	}
	usage, err := policy.ReadUsage(strings.NewReader(rows))
	if err != nil {
		return "", err
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:  usage,
		Trust:  policy.Unverified,
		Config: policy.ConfigFile{Data: []byte(config), Found: true, Ref: demoBase, FromBase: true},
		Start:  policy.Start{At: demoWorkflowAdded},
		At:     at,
	})
	if err != nil {
		return "", err
	}
	checkRun := demoCheckRun
	input := comment.Input{
		Ledger:       ledger,
		State:        comment.Reported,
		HeadSHA:      demoHead,
		Installation: demoInstallation,
		CheckRunID:   &checkRun,
		Amounts:      comment.AmountsShown,
		RunURL:       demoRunURL,
		Verdict:      &verdict,
	}
	switch state {
	case demoCapped:
		amount := decimal.MustParse(demoCap)
		input.Budget = &amount
	case demoSkipped:
		skipped := true
		input.State, input.Reason, input.AgentSkipped = comment.NoRows, comment.AgentSkipped, &skipped
	}
	return comment.Comment(input)
}

// terminalWidth is the column prose wraps at. A table is as wide as its
// widest cells.
const terminalWidth = 80

var (
	markdownLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	summaryElement = regexp.MustCompile(`^<summary>(.*)</summary>$`)
)

// forTerminal renders the comment as plain text: each paragraph wrapped, each
// table in aligned columns, links as their text and address. The machine
// block is for programs that read the check run, so it is left out, and so
// is the comment's marker.
func forTerminal(markdown string) string {
	var blocks []string
	lines := strings.Split(markdown, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case line == "" || line == comment.Marker || line == "<details>" || line == "</details>":
		case strings.HasPrefix(line, "### "):
			blocks = append(blocks, strings.TrimPrefix(line, "### "))
		case summaryElement.MatchString(line):
			summary := summaryElement.FindStringSubmatch(line)[1]
			if strings.HasPrefix(summary, "Machine block") {
				for i < len(lines) && lines[i] != "</details>" {
					i++
				}
				continue
			}
			blocks = append(blocks, plainText(summary)+":")
		case strings.HasPrefix(line, "|"):
			end := i
			for end < len(lines) && strings.HasPrefix(lines[end], "|") {
				end++
			}
			blocks = append(blocks, alignedTable(lines[i:end]))
			i = end - 1
		default:
			blocks = append(blocks, paragraph(line))
		}
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// paragraph wraps one line of the comment. The links the comment lists on one
// line, apart by a middle dot, go on a line each.
func paragraph(line string) string {
	parts := strings.Split(plainText(line), " · ")
	for index, part := range parts {
		parts[index] = strings.TrimSuffix(wrap(part, terminalWidth), "\n")
	}
	return strings.Join(parts, "\n")
}

// plainText drops the comment's inline Markdown: emphasis, code spans and
// links. The comment's own text and Data hold no other markup.
func plainText(text string) string {
	text = markdownLink.ReplaceAllString(text, "$1 ($2)")
	return strings.NewReplacer("**", "", "`", "").Replace(text)
}

// alignedTable lays out a Markdown table's header and rows in columns two
// spaces apart, without its delimiter row. The comment never escapes a pipe
// in a cell.
func alignedTable(lines []string) string {
	var rows [][]string
	var widths []int
	for _, line := range lines {
		cells := strings.Split(strings.Trim(line, "| "), " | ")
		if strings.Trim(strings.Join(cells, ""), "-: ") == "" {
			continue
		}
		for column, cell := range cells {
			cells[column] = plainText(cell)
			if column == len(widths) {
				widths = append(widths, 0)
			}
			widths[column] = max(widths[column], utf8.RuneCountInString(cells[column]))
		}
		rows = append(rows, cells)
	}
	out := make([]string, len(rows))
	for index, cells := range rows {
		var row strings.Builder
		for column, cell := range cells {
			if column == len(cells)-1 {
				row.WriteString(cell)
				break
			}
			row.WriteString(cell + strings.Repeat(" ", widths[column]-utf8.RuneCountInString(cell)+2))
		}
		out[index] = row.String()
	}
	return strings.Join(out, "\n")
}

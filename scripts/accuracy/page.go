package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/reconcile"
)

const (
	pageMode = 0o644
	noFigure = "none"
)

// line is one reconciliation of one credential on one day, as the page shows
// it. A line with no failures passed. explained says it passed only once the
// residual's named causes were taken out. writer is the Costroid release that
// wrote the day's rows, empty when the day does not name it.
type line struct {
	day string
	named
	writer    string
	tokens    []tokenFigure
	dollars   *dollarFigure
	failures  []string
	explained bool
	digest    string
}

func (l line) passed() bool { return len(l.failures) == 0 }

func (l line) reconciled() bool { return l.tokens != nil || l.dollars != nil }

func (l line) verdict() string {
	switch {
	case !l.passed():
		return strings.Join(l.failures, ", ")
	case l.explained:
		return "pass after named causes"
	default:
		return "pass"
	}
}

// amountOf reads an amount `costroid reconcile` wrote.
func amountOf(text string) (decimal.Decimal, error) {
	amount, err := decimal.ParseNumber(text)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%q is not a decimal", text)
	}
	return amount, nil
}

// gapOf is a delta against the provider's figure, as the reconciliation table
// prints it.
func gapOf(delta, provider string) (reconcile.Percent, error) {
	part, err := amountOf(delta)
	if err != nil {
		return reconcile.Percent{}, err
	}
	whole, err := amountOf(provider)
	if err != nil {
		return reconcile.Percent{}, err
	}
	return reconcile.PercentOf(part, whole), nil
}

// largest is the largest gap of the lines seen, by its size. A gap against a
// zero provider figure is larger than any other. seen is false until a line
// is.
type largest struct {
	gap  reconcile.Percent
	seen bool
}

func (l largest) with(gap reconcile.Percent) largest {
	if !l.seen {
		return largest{gap: gap, seen: true}
	}
	held, known := l.gap.Size()
	next, nextKnown := gap.Size()
	if !known || (nextKnown && held.Cmp(next) >= 0) {
		return l
	}
	return largest{gap: gap, seen: true}
}

func (l largest) cell() string {
	if !l.seen {
		return noFigure
	}
	return l.gap.String()
}

const header = `# Accuracy

Costroid prices an agent's run from the agent's own token counts. This page compares those figures with the provider's usage and cost reports, one UTC day at a time, for the API key that Costroid's own test repository runs an agent on.

## Method

Each day a scheduled job in a private repository runs one small Claude Code task under ` + "`costroid run`" + `, with a budget of 0.10 USD. Two days later a second job saves the provider's usage and cost reports for that day, cut to that key's own records, beside every row the key's runs wrote that day. The rows, the reports and the time they were saved are that day's inputs. Each week they are committed to the private repository.

Measured cost is what the day's rows on the key cost at list price, from Costroid's own rows. It reads on a day with no provider report as well, so the test's spend is always on the page.

` + "`go run ./scripts/accuracy -write docs/accuracy.md`" + ` re-runs ` + "`costroid reconcile`" + ` on each day's inputs and writes this page. A day passes when every token class matches the usage report within 1%, every price the cost report implies equals the catalog's to the last digit the report states, each token class's list-price dollars land within 1% of the cost report's where the report splits them by class, and the day's list-price dollars land within 10% of the cost report, or within 10% once the residual's named causes are taken out. [Compare a ledger with a provider's report](reconcile.md) defines each step and each cause.

Each day names the Costroid release that kept its inputs, which the job pins to the release it runs the agent under. A day fails when it names no release, when it names a build that is no release, or when its release trails the newest release out on that day by more than one.

No line is edited by hand. ` + "`go run ./scripts/accuracy -check docs/accuracy.md -days 14`" + ` recomputes every line from the inputs, refuses the page when one line differs, and passes only when the last 14 days on it are consecutive and each passed. The last column is the SHA-256 of a listing of each input file's SHA-256 and path, in the form ` + "`sha256sum`" + ` prints, so a line names the exact inputs it was computed from.
`

// render writes the page. The same lines always give the same bytes.
func render(lines []line) []byte {
	var page bytes.Buffer
	page.WriteString(header)
	page.WriteString("\n## By agent version\n\n")
	renderVersions(&page, lines)
	page.WriteString("\n## Days\n\n")
	renderDays(&page, lines)
	return page.Bytes()
}

type agentVersion struct {
	agent                   string
	reconciled, passed      int
	tokens, dollars, causes largest
}

func renderVersions(w io.Writer, lines []line) {
	var versions []*agentVersion
	for _, l := range lines {
		if !l.reconciled() {
			continue
		}
		index := slices.IndexFunc(versions, func(v *agentVersion) bool { return v.agent == l.agent })
		if index < 0 {
			versions = append(versions, &agentVersion{agent: l.agent})
			index = len(versions) - 1
		}
		versions[index].add(l)
	}
	if len(versions) == 0 {
		fmt.Fprintln(w, "No day has been reconciled yet.")
		return
	}
	slices.SortFunc(versions, func(a, b *agentVersion) int { return strings.Compare(a.agent, b.agent) })
	fmt.Fprintln(w, "| Agent | Days reconciled | Days passed | Largest token error | Largest dollar error | Largest dollar error after named causes |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- |")
	for _, v := range versions {
		fmt.Fprintf(w, "| %s | %d | %d | %s | %s | %s |\n", cell(v.agent), v.reconciled, v.passed, v.tokens.cell(), v.dollars.cell(), v.causes.cell())
	}
}

// add counts one line. The figures were read when the line was judged, so
// they parse.
func (v *agentVersion) add(l line) {
	v.reconciled++
	if l.passed() {
		v.passed++
	}
	for _, check := range l.tokens {
		gap, _ := gapOf(check.Delta, check.Provider)
		v.tokens = v.tokens.with(gap)
	}
	if l.dollars.compared() {
		residual, _ := gapOf(l.dollars.Residual, l.dollars.Provider)
		unattributed, _ := gapOf(l.dollars.Unattributed, l.dollars.Provider)
		v.dollars = v.dollars.with(residual)
		v.causes = v.causes.with(unattributed)
	}
}

func renderDays(w io.Writer, lines []line) {
	if len(lines) == 0 {
		fmt.Fprintln(w, "No day has been reconciled yet.")
		return
	}
	fmt.Fprintln(w, "| Day (UTC) | Source | Agent, Costroid release | Catalog | Measured cost | Tokens, ledger / provider | List USD, ledger / provider: day; each class | Residual USD | Causes | Verdict | Inputs SHA-256 |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, l := range lines {
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | `%s` |\n", l.day, cell(l.source), writerCell(l), cell(l.catalog), cell(l.measured),
			tokensCell(l.tokens), dollarsCell(l.dollars), residualCell(l.dollars), causesCell(l.dollars), l.verdict(), l.digest)
	}
}

// writerCell names the agent that ran and the Costroid release that wrote
// its rows.
func writerCell(l line) string {
	writer := l.writer
	if writer == "" {
		writer = noFigure
	}
	return cell(l.agent) + ", Costroid " + cell(writer)
}

func tokensCell(checks []tokenFigure) string {
	if len(checks) == 0 {
		return noFigure
	}
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		gap, _ := gapOf(check.Delta, check.Provider)
		parts = append(parts, fmt.Sprintf("%s %s / %s (%s)", cell(check.Class), cell(check.Ledger), cell(check.Provider), gap))
	}
	return strings.Join(parts, "; ")
}

func dollarsCell(dollars *dollarFigure) string {
	if dollars == nil {
		return noFigure
	}
	if !dollars.compared() {
		return fmt.Sprintf("%s / %s", cell(dollars.Ledger), cell(dollars.Provider))
	}
	gap, _ := gapOf(dollars.Residual, dollars.Provider)
	parts := []string{fmt.Sprintf("%s / %s (%s)", cell(dollars.Ledger), cell(dollars.Provider), gap)}
	for _, class := range dollars.Classes {
		gap, _ := gapOf(class.Delta, class.Provider)
		parts = append(parts, fmt.Sprintf("%s %s / %s (%s)", cell(class.Class), cell(class.Ledger), cell(class.Provider), gap))
	}
	return strings.Join(parts, "; ")
}

// compared reports whether the ledger's dollars stood against an API bill.
func (d *dollarFigure) compared() bool {
	return d != nil && reconcile.DollarVerdict(d.Verdict) != reconcile.NoBill
}

func residualCell(dollars *dollarFigure) string {
	if !dollars.compared() {
		return noFigure
	}
	return cell(dollars.Residual)
}

func causesCell(dollars *dollarFigure) string {
	if !dollars.compared() {
		return noFigure
	}
	var parts []string
	for _, named := range dollars.Causes {
		parts = append(parts, cell(named.Name)+" "+cell(named.Amount))
	}
	if dollars.Unattributed != "" && dollars.Unattributed != "0" {
		parts = append(parts, "unattributed "+cell(dollars.Unattributed))
	}
	if len(parts) == 0 {
		return noFigure
	}
	return strings.Join(parts, "; ")
}

// cell keeps a value from closing a table cell or the row.
func cell(text string) string {
	text = strings.ReplaceAll(text, "|", `\|`)
	return strings.Join(strings.Fields(text), " ")
}

// compute recomputes every line from the kept inputs.
func compute(ctx context.Context, opts options, stderr io.Writer) ([]line, error) {
	days, err := readDays(opts.inputs)
	if err != nil {
		return nil, err
	}
	releases, err := readReleases(opts.changelog)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "accuracy-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	binary, err := costroidBinary(ctx, opts.costroid, scratch, stderr)
	if err != nil {
		return nil, err
	}
	groups, err := spans(days)
	if err != nil {
		return nil, err
	}
	writers := map[string]string{}
	for _, day := range days {
		writers[day.record.Day] = strings.TrimPrefix(day.record.Writer, "v")
	}
	var lines []line
	for _, group := range groups {
		groupLines, err := groupLinesOf(ctx, binary, group, scratch, stderr)
		if err != nil {
			return nil, err
		}
		for i := range groupLines {
			writer := writers[groupLines[i].day]
			groupLines[i].writer = writer
			if failure := writerFailure(writer, groupLines[i].day, releases); failure != "" {
				groupLines[i].failures = append(groupLines[i].failures, failure)
			}
		}
		lines = append(lines, groupLines...)
	}
	return lines, nil
}

// groupLinesOf recomputes the lines of a group of days: as one range when
// they can be joined, and each day alone otherwise.
func groupLinesOf(ctx context.Context, binary string, group []keptDay, scratch string, stderr io.Writer) ([]line, error) {
	if joinable(group) {
		lines, err := spanLines(ctx, binary, group, scratch, stderr)
		if !errors.Is(err, errUnsettledDay) {
			return lines, err
		}
	}
	var lines []line
	for _, day := range group {
		dayLines, err := linesOf(ctx, binary, day, scratch, stderr)
		if err != nil {
			return nil, err
		}
		lines = append(lines, dayLines...)
	}
	return lines, nil
}

// The verdicts a line fails with when the release that wrote its rows is
// not the one that ships.
const (
	failNoWriter         = "day names no writer"
	failWriterUnreleased = "writer is no release"
	failWriterLags       = "writer lags the newest release"
)

// maxWriterLag is how many releases the writer may trail the newest release
// out on its day: one, for the day a release ships and the pin moves.
const maxWriterLag = 1

// writerFailure judges the release that wrote a day's rows against the
// releases out on that day, counted in the changelog's order. A writer the
// changelog does not list, or lists after the day, is no release.
func writerFailure(writer, day string, releases []release) string {
	if writer == "" {
		return failNoWriter
	}
	newest := slices.IndexFunc(releases, func(r release) bool { return r.date <= day })
	wrote := slices.IndexFunc(releases, func(r release) bool { return r.version == writer })
	switch {
	case newest < 0 || wrote < newest:
		return failWriterUnreleased
	case wrote-newest > maxWriterLag:
		return failWriterLags
	default:
		return ""
	}
}

// release is one entry of the changelog: a version and its UTC date.
type release struct {
	version, date string
}

const (
	modulePath = "github.com/Costroid/costroid"
	changelog  = "CHANGELOG.md"
)

var changelogHeading = regexp.MustCompile(`^## (\S+) \(([0-9]{4}-[0-9]{2}-[0-9]{2})\)$`)

// readReleases lists the releases in a changelog, newest first: the one at
// path, or this module's when path is empty. A second-level heading of
// another form is refused, so no release is skipped when the lag is counted.
func readReleases(path string) ([]release, error) {
	if path == "" {
		root, err := moduleRoot()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(root, changelog)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the releases: %w", err)
	}
	var releases []release
	for _, text := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(text, "## ") {
			continue
		}
		match := changelogHeading.FindStringSubmatch(text)
		if match == nil {
			return nil, fmt.Errorf("%s: the heading %q is not ## <release> (<date>)", path, text)
		}
		releases = append(releases, release{version: match[1], date: match[2]})
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("%s lists no release", path)
	}
	return releases, nil
}

// moduleRoot is the nearest directory at or above the working directory
// whose go.mod declares this module.
func moduleRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		data, err := os.ReadFile(filepath.Join(directory, "go.mod"))
		if err == nil && declares(data, modulePath) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("run this inside a checkout of " + modulePath + ": no go.mod above the working directory declares it")
		}
		directory = parent
	}
}

func declares(goMod []byte, module string) bool {
	for _, text := range strings.Split(string(goMod), "\n") {
		if fields := strings.Fields(text); len(fields) == 2 && fields[0] == "module" {
			return fields[1] == module
		}
	}
	return false
}

// costroidBinary builds cmd/costroid from this module unless a binary is named.
func costroidBinary(ctx context.Context, named, scratch string, stderr io.Writer) (string, error) {
	if named != "" {
		return named, nil
	}
	binary := filepath.Join(scratch, "costroid")
	if err := goBuild(ctx, binary, stderr); err != nil {
		return "", fmt.Errorf("build costroid: %w", err)
	}
	return binary, nil
}

func writePage(ctx context.Context, opts options, stdout, stderr io.Writer) error {
	lines, err := compute(ctx, opts, stderr)
	if err != nil {
		return err
	}
	if err := os.WriteFile(opts.write, render(lines), pageMode); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "accuracy: wrote %s from %d lines\n", opts.write, len(lines))
	return nil
}

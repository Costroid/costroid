package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reconcile"
)

const (
	dayLayout = "2006-01-02"
	maxRow    = 1 << 20

	// The exit codes of costroid reconcile that carry a result.
	reconcileMatched = 0
	reconcileMissed  = 1
)

// keptDay is one directory of inputs. Digest covers every file in it.
type keptDay struct {
	directory string
	record    dayRecord
	digest    string
}

// readDays lists the kept days in order. Anything under inputs that is not a
// kept day is refused, so nothing sits there unread.
func readDays(inputs string) ([]keptDay, error) {
	entries, err := os.ReadDir(inputs)
	if err != nil {
		return nil, fmt.Errorf("read the kept days: %w", err)
	}
	var days []keptDay
	for _, entry := range entries {
		if _, err := time.Parse(dayLayout, entry.Name()); err != nil || !entry.IsDir() {
			return nil, fmt.Errorf("%s is not a kept day: inputs hold one directory per day, named YYYY-MM-DD", filepath.Join(inputs, entry.Name()))
		}
		day, err := readDay(filepath.Join(inputs, entry.Name()))
		if err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, nil
}

func readDay(directory string) (keptDay, error) {
	data, err := os.ReadFile(filepath.Join(directory, dayFile))
	if err != nil {
		return keptDay{}, err
	}
	var record dayRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return keptDay{}, fmt.Errorf("%s: %w", filepath.Join(directory, dayFile), err)
	}
	switch {
	case record.Day != filepath.Base(directory):
		return keptDay{}, fmt.Errorf("%s keeps %q, not the day it is named for", directory, record.Day)
	case record.Report != reportSaved && record.Report != reportNone:
		return keptDay{}, fmt.Errorf("%s: report reads %q, want %s or %s", directory, record.Report, reportSaved, reportNone)
	}
	if _, err := time.Parse(focus.InstantLayout, record.SavedAt); err != nil {
		return keptDay{}, fmt.Errorf("%s: saved_at: %w", directory, err)
	}
	digest, err := digestOf(directory)
	if err != nil {
		return keptDay{}, err
	}
	return keptDay{directory: directory, record: record, digest: digest}, nil
}

// digestOf is the SHA-256 of a listing that names each file's SHA-256 and its
// path, in the form sha256sum prints, in path order.
func digestOf(directory string) (string, error) {
	var listing bytes.Buffer
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&listing, "%x  %s\n", sha256.Sum256(data), filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest %s: %w", directory, err)
	}
	sum := sha256.Sum256(listing.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// reconciledRow is one row of `costroid reconcile --out`. Amounts stay the
// strings the command wrote.
type reconciledRow struct {
	Source     string        `json:"source"`
	Credential string        `json:"credential"`
	Day        string        `json:"day"`
	State      string        `json:"state"`
	Tokens     []tokenFigure `json:"tokens"`
	Dollars    *dollarFigure `json:"dollars"`
}

type tokenFigure struct {
	Class    string `json:"class"`
	Ledger   string `json:"ledger"`
	Provider string `json:"provider"`
	Delta    string `json:"delta"`
	Within   bool   `json:"within"`
}

type dollarFigure struct {
	Verdict      string  `json:"verdict"`
	Ledger       string  `json:"ledger"`
	Provider     string  `json:"provider"`
	Residual     string  `json:"residual"`
	Causes       []cause `json:"causes"`
	Unattributed string  `json:"unattributed"`
}

type cause struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// errReconcileRefused is a day whose inputs `costroid reconcile` would not
// read. It is a failed day, never a missing one.
var errReconcileRefused = errors.New("costroid reconcile refused the inputs")

// reconcileDay re-runs `costroid reconcile` on one kept day.
func reconcileDay(ctx context.Context, binary string, day keptDay, scratch string, stderr io.Writer) ([]reconciledRow, error) {
	out := filepath.Join(scratch, day.record.Day+".jsonl")
	args := []string{
		"reconcile",
		"--rows", filepath.Join(day.directory, rowsFile),
		"--from", day.record.Day,
		"--to", day.record.Day,
		"--reports", filepath.Join(day.directory, reportsDirectory),
		"--as-of", day.record.SavedAt,
		"--out", out,
	}
	if day.record.Credential != "" {
		args = append(args, "--credential", day.record.Credential)
	}
	command := exec.CommandContext(ctx, binary, args...)
	var said bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &said
	err := command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil, errors.As(err, &exit) && exit.ExitCode() == reconcileMissed:
	case errors.As(err, &exit):
		fmt.Fprintf(stderr, "accuracy: %s: costroid reconcile exited %d: %s", day.record.Day, exit.ExitCode(), said.String())
		return nil, errReconcileRefused
	default:
		return nil, fmt.Errorf("%s: run %s: %w", day.record.Day, binary, err)
	}
	return readReconciled(out)
}

func readReconciled(path string) ([]reconciledRow, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var rows []reconciledRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, maxRow)
	for scanner.Scan() {
		var row reconciledRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}

// labelRow is the part of a ledger row that names who ran and what priced it.
type labelRow struct {
	ChargePeriodStart   time.Time         `json:"ChargePeriodStart"`
	ServiceProviderName string            `json:"ServiceProviderName"`
	SubAccountID        *string           `json:"SubAccountId"`
	Tags                map[string]string `json:"Tags"`
	CatalogVersion      *string           `json:"x_CostroidCatalogVersion"`
}

// labels is each agent version and catalog version the day's rows name.
type labels struct {
	rows []labelRow
}

func readLabels(day keptDay) (labels, error) {
	file, err := os.Open(filepath.Join(day.directory, rowsFile))
	if err != nil {
		return labels{}, err
	}
	defer file.Close()
	var kept labels
	decoder := json.NewDecoder(file)
	for {
		var row labelRow
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			return kept, nil
		}
		if err != nil {
			return labels{}, fmt.Errorf("%s: %w", file.Name(), err)
		}
		if row.ChargePeriodStart.UTC().Format(dayLayout) == day.record.Day {
			kept.rows = append(kept.rows, row)
		}
	}
}

// of returns the agents, the catalogs and the sources of the rows that match
// a source and credential. Empty strings match every row.
func (l labels) of(source, credential, fallback string) (agents, catalogs, sources string) {
	var agentNames, catalogNames, sourceNames []string
	for _, row := range l.rows {
		rowSource := strings.ToLower(row.ServiceProviderName)
		rowCredential := fallback
		if row.SubAccountID != nil {
			rowCredential = *row.SubAccountID
		}
		if (source != "" && rowSource != source) || (credential != "" && rowCredential != credential) {
			continue
		}
		agentNames = appendNew(agentNames, strings.TrimSpace(row.Tags[focus.TagAgent]+" "+row.Tags[focus.TagAgentVersion]))
		if row.CatalogVersion != nil {
			catalogNames = appendNew(catalogNames, *row.CatalogVersion)
		}
		sourceNames = appendNew(sourceNames, rowSource)
	}
	return joined(agentNames), joined(catalogNames), joined(sourceNames)
}

func appendNew(names []string, name string) []string {
	if name == "" || slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}

func joined(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// linesOf recomputes one kept day's lines.
func linesOf(ctx context.Context, binary string, day keptDay, scratch string, stderr io.Writer) ([]line, error) {
	names, err := readLabels(day)
	if err != nil {
		return nil, err
	}
	whole := func(failure string) []line {
		agent, catalog, source := names.of("", "", day.record.Credential)
		if source == "none" {
			failure = failNoRows
		}
		return []line{{day: day.record.Day, source: source, agent: agent, catalog: catalog, digest: day.digest, failures: []string{failure}}}
	}
	if day.record.Report == reportNone {
		return whole(failNoReport), nil
	}
	rows, err := reconcileDay(ctx, binary, day, scratch, stderr)
	if errors.Is(err, errReconcileRefused) {
		return whole(failRefused), nil
	}
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return whole(failNoReconciliation), nil
	}
	lines := make([]line, 0, len(rows))
	for _, row := range rows {
		agent, catalog, _ := names.of(row.Source, row.Credential, day.record.Credential)
		judged, err := judge(row)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", day.record.Day, err)
		}
		judged.agent, judged.catalog, judged.digest = agent, catalog, day.digest
		lines = append(lines, judged)
	}
	return lines, nil
}

// The verdicts a line can fail with.
const (
	failNoReport         = "no provider report"
	failNoRows           = "no rows"
	failRefused          = "inputs refused"
	failNoReconciliation = "nothing reconciled"
	failPending          = "pending"
	failTokens           = "token miss"
	failNoBill           = "no API bill"
	failDollars          = "dollar miss"
)

// judge reads one reconciliation row. Tokens pass only as `reconcile` judged
// them. Dollars pass within 10%, or within 10% once the named causes are
// taken out of the residual.
func judge(row reconciledRow) (line, error) {
	judged := line{day: row.Day, source: row.Source, tokens: row.Tokens, dollars: row.Dollars}
	switch reconcile.State(row.State) {
	case reconcile.Pending:
		judged.failures = append(judged.failures, failPending)
		return judged, nil
	case reconcile.TokensMiss:
		judged.failures = append(judged.failures, failTokens)
	case reconcile.Matched:
	default:
		return line{}, fmt.Errorf("unknown state %q", row.State)
	}
	if row.Dollars == nil {
		return line{}, errors.New("a settled row has no dollar figures")
	}
	switch reconcile.DollarVerdict(row.Dollars.Verdict) {
	case reconcile.Within:
	case reconcile.NoBill:
		judged.failures = append(judged.failures, failNoBill)
	case reconcile.Outside:
		explained, err := explained(*row.Dollars)
		if err != nil {
			return line{}, err
		}
		if !explained {
			judged.failures = append(judged.failures, failDollars)
		}
		judged.named = explained
	default:
		return line{}, fmt.Errorf("unknown dollar verdict %q", row.Dollars.Verdict)
	}
	return judged, nil
}

// explained reports whether named causes bring the residual inside the
// tolerance: what no cause reached is at most 10% of the provider's figure.
func explained(dollars dollarFigure) (bool, error) {
	if len(dollars.Causes) == 0 {
		return false, nil
	}
	share, err := percentOf(dollars.Unattributed, dollars.Provider)
	if err != nil {
		return false, err
	}
	return share.finite && share.value.Cmp(ratOf(reconcile.DollarTolerancePercent)) <= 0, nil
}

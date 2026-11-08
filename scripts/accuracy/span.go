package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Costroid/costroid/internal/reconcile"
)

// savedReportMode keeps a merged report as `costroid reconcile` keeps a saved
// one: readable by its owner alone.
const savedReportMode = 0o600

// refreshedField is where an Anthropic report stamps when its data was last
// refreshed. A day that ends after the stamp reads unsettled.
const refreshedField = "data_refreshed_at"

// errUnsettledDay is a kept day whose own saved report does not settle it.
// Its group is not joined, so each of its days reads as it does alone.
var errUnsettledDay = errors.New("a saved report does not settle its own day")

// spans groups the kept days a run crossed midnight UTC between. A run is
// dated by its start, and the provider dates each request by when it was
// served, so a day of the run read alone misses. The days of a group are
// reconciled as one range, as `costroid reconcile` joins them. A group holds
// consecutive kept days; a run that reaches a day not kept leaves its own day
// alone, and that day reads pending.
func spans(days []keptDay) ([][]keptDay, error) {
	var groups [][]keptDay
	reach, previous := "", ""
	for _, day := range days {
		last, err := lastDay(day)
		if err != nil {
			return nil, err
		}
		if n := len(groups); n > 0 && day.record.Day <= reach && follows(previous, day.record.Day) {
			groups[n-1] = append(groups[n-1], day)
		} else {
			groups, reach = append(groups, []keptDay{day}), ""
		}
		reach, previous = max(reach, last), day.record.Day
	}
	return groups, nil
}

// lastDay is the last UTC day a kept day's own rows run into. A period that
// ends at midnight holds the day before it.
func lastDay(day keptDay) (string, error) {
	file, err := os.Open(filepath.Join(day.directory, rowsFile))
	if err != nil {
		return "", err
	}
	defer file.Close()
	last := day.record.Day
	decoder := json.NewDecoder(file)
	for {
		var row struct {
			ChargePeriodStart time.Time `json:"ChargePeriodStart"`
			ChargePeriodEnd   time.Time `json:"ChargePeriodEnd"`
		}
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			return last, nil
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", file.Name(), err)
		}
		if row.ChargePeriodStart.UTC().Format(dayLayout) != day.record.Day || row.ChargePeriodEnd.IsZero() {
			continue
		}
		last = max(last, row.ChargePeriodEnd.Add(-time.Nanosecond).UTC().Format(dayLayout))
	}
}

// joinable says whether a group's days can be reconciled as one: each kept
// its reports, and all name the same credential for rows that name none.
func joinable(group []keptDay) bool {
	for _, day := range group {
		if day.record.Report != reportSaved || day.record.Credential != group[0].record.Credential {
			return false
		}
	}
	return len(group) > 1
}

// spanLines recomputes the lines of a group's days from one reconciliation
// over them. Each day's line reads that day's rows of it, beside that day's
// own rows and digest.
func spanLines(ctx context.Context, binary string, group []keptDay, scratch string, stderr io.Writer) ([]line, error) {
	first, last := group[0].record.Day, group[len(group)-1].record.Day
	directory := filepath.Join(scratch, first+"-to-"+last)
	if err := joinInputs(group, directory); err != nil {
		return nil, fmt.Errorf("%s to %s: %w", first, last, err)
	}
	input := reconcileInput{from: first, to: last, directory: directory, credential: group[0].record.Credential}
	for _, day := range group {
		input.savedAt = max(input.savedAt, day.record.SavedAt)
	}
	reconciled, refused := reconcileInputs(ctx, binary, input, directory+".jsonl", stderr)
	if refused != nil && !errors.Is(refused, errReconcileRefused) {
		return nil, refused
	}
	var lines []line
	for _, day := range group {
		var dayLines []line
		var err error
		if refused != nil {
			dayLines, err = wholeDay(day, failRefused)
		} else {
			dayLines, err = reconciledLines(day, rowsOn(reconciled, day.record.Day))
		}
		if err != nil {
			return nil, err
		}
		lines = append(lines, dayLines...)
	}
	return lines, nil
}

// rowsOn is the reconciliation rows of one day.
func rowsOn(reconciled []reconciledRow, day string) []reconciledRow {
	var on []reconciledRow
	for _, row := range reconciled {
		if row.Day == day {
			on = append(on, row)
		}
	}
	return on
}

// joinInputs writes the inputs of one reconciliation over a group's days:
// each day's own rows, and each saved report with the records of every day.
func joinInputs(group []keptDay, directory string) error {
	if err := os.MkdirAll(filepath.Join(directory, reportsDirectory), directoryMode); err != nil {
		return err
	}
	var rows bytes.Buffer
	for _, day := range group {
		own, err := ownRows(day)
		if err != nil {
			return err
		}
		rows.Write(own)
	}
	if err := os.WriteFile(filepath.Join(directory, rowsFile), rows.Bytes(), keptMode); err != nil {
		return err
	}
	for _, name := range reconcile.SavedReportFiles {
		var saved []keptDay
		for _, day := range group {
			if _, err := os.Stat(filepath.Join(day.directory, reportsDirectory, name)); err == nil {
				saved = append(saved, day)
			}
		}
		if len(saved) == 0 {
			continue
		}
		if err := mergeReports(saved, name, filepath.Join(directory, reportsDirectory, name)); err != nil {
			return err
		}
	}
	return nil
}

// ownRows is the lines of a kept day's rows file that start on that day, as
// readLabels reads them, so no row counts on two days.
func ownRows(day keptDay) ([]byte, error) {
	file, err := os.Open(filepath.Join(day.directory, rowsFile))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var own bytes.Buffer
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, maxRow)
	for scanner.Scan() {
		var row struct {
			ChargePeriodStart time.Time `json:"ChargePeriodStart"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name(), err)
		}
		if row.ChargePeriodStart.UTC().Format(dayLayout) == day.record.Day {
			own.Write(append(scanner.Bytes(), '\n'))
		}
	}
	return own.Bytes(), scanner.Err()
}

// mergeReports writes one report whose data holds the records of each day's
// saved report called name, in order. Each saved report is one complete page
// of its own day, so its records are all it holds. Every other field is the
// first day's, but the refresh stamp, which is the latest: each day's own
// stamp must settle that day, or the days are not joined.
func mergeReports(days []keptDay, name, to string) error {
	var merged map[string]json.RawMessage
	var records []json.RawMessage
	var latest time.Time
	for _, day := range days {
		path := filepath.Join(day.directory, reportsDirectory, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var page map[string]json.RawMessage
		if err := json.Unmarshal(data, &page); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		var these []json.RawMessage
		if err := json.Unmarshal(page["data"], &these); err != nil {
			return fmt.Errorf("%s: data: %w", path, err)
		}
		records = append(records, these...)
		if merged == nil {
			merged = page
		}
		if stamp, found := page[refreshedField]; found && string(stamp) != "null" {
			var refreshed time.Time
			if err := json.Unmarshal(stamp, &refreshed); err != nil {
				return fmt.Errorf("%s: %s: %w", path, refreshedField, err)
			}
			end, err := time.Parse(dayLayout, day.record.Day)
			if err != nil {
				return err
			}
			if refreshed.Before(end.AddDate(0, 0, 1)) {
				return errUnsettledDay
			}
			if refreshed.After(latest) {
				latest, merged[refreshedField] = refreshed, stamp
			}
		}
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	merged["data"] = data
	page, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return os.WriteFile(to, page, savedReportMode)
}

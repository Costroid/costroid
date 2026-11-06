package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reconcile"
)

const (
	dayFile          = "day.json"
	rowsFile         = "rows.jsonl"
	reportsDirectory = "reports"

	reportSaved = "saved"
	reportNone  = "none"

	keptMode      = 0o644
	directoryMode = 0o755
)

// adminKeys names the variable each source's report reads its key from, as
// `costroid reconcile` does.
var adminKeys = map[string]string{
	reconcile.SourceAnthropic: "ANTHROPIC_ADMIN_KEY",
	reconcile.SourceOpenAI:    "OPENAI_ADMIN_KEY",
}

var fetchers = map[string]func(reconcile.Doer, string) reconcile.Provider{
	reconcile.SourceAnthropic: reconcile.NewAnthropic,
	reconcile.SourceOpenAI:    reconcile.NewOpenAI,
}

type doer = reconcile.Doer

func liveClient() doer { return reconcile.NewClient() }

// dayRecord is day.json: which day the directory keeps, when its reports were
// saved and whether they were. Reason says why a report is missing.
type dayRecord struct {
	Day        string `json:"day"`
	SavedAt    string `json:"saved_at"`
	Credential string `json:"credential"`
	Report     string `json:"report"`
	Reason     string `json:"reason"`
}

// save keeps the rows, the reports and day.json. day.json is written whatever
// happens after the rows are kept, so a failed save still leaves a day that
// reads as failed.
func save(ctx context.Context, opts options, outside world, stdout io.Writer) error {
	days, err := reconcile.ParseRange(opts.day, opts.day)
	if err != nil {
		return err
	}
	rows, err := os.ReadFile(opts.rows)
	if err != nil {
		return err
	}
	if err := os.Mkdir(opts.save, directoryMode); err != nil {
		return fmt.Errorf("keep the day: %w", err)
	}
	if err := os.WriteFile(filepath.Join(opts.save, rowsFile), rows, keptMode); err != nil {
		return fmt.Errorf("keep the rows: %w", err)
	}
	record := dayRecord{
		Day:        opts.day,
		SavedAt:    outside.now().UTC().Format(focus.InstantLayout),
		Credential: opts.credential,
		Report:     reportNone,
	}
	saveErr := saveReports(ctx, opts, days, rows, outside, &record)
	if err := keepRecord(opts.save, record); err != nil {
		return errors.Join(saveErr, err)
	}
	if saveErr != nil {
		return saveErr
	}
	if record.Report == reportNone {
		fmt.Fprintf(stdout, "accuracy: no provider report for %s: %s\n", record.Day, record.Reason)
		return nil
	}
	fmt.Fprintf(stdout, "accuracy: kept the rows and the provider reports for %s in %s\n", record.Day, opts.save)
	return nil
}

// saveReports fetches every report the rows need, or none of them. A missing
// key is recorded and is not an error.
func saveReports(ctx context.Context, opts options, days reconcile.Range, rows []byte, outside world, record *dayRecord) error {
	ledger, err := reconcile.ReadLedger(bytes.NewReader(rows), days, opts.credential)
	if err != nil {
		record.Reason = "the rows could not be read"
		return err
	}
	if ledger.Uncredentialed > 0 {
		record.Reason = "rows name no credential"
		return fmt.Errorf("%d rows have a null SubAccountId: pass -credential", ledger.Uncredentialed)
	}
	sources := ledger.Sources()
	if len(sources) == 0 {
		record.Reason = "the rows hold no token row of a reconciled provider on this day"
		return nil
	}
	keys := map[string]string{}
	for _, source := range sources {
		keys[source] = outside.getenv(adminKeys[source])
		if keys[source] == "" {
			record.Reason = adminKeys[source] + " is not set"
			return nil
		}
	}
	directory := filepath.Join(opts.save, reportsDirectory)
	if err := os.Mkdir(directory, directoryMode); err != nil {
		record.Reason = "the reports could not be kept"
		return err
	}
	recorder := &reconcile.Recorder{Client: outside.client, Directory: directory}
	for _, source := range sources {
		provider := fetchers[source](recorder, keys[source])
		if _, err := provider.Fetch(ctx, days, ledger.Credentials(source)); err != nil {
			record.Reason = "the " + source + " report could not be fetched"
			return errors.Join(fmt.Errorf("%s report: %w", source, err), os.RemoveAll(directory))
		}
	}
	record.Report = reportSaved
	return nil
}

func keepRecord(directory string, record dayRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, dayFile), append(data, '\n'), keptMode); err != nil {
		return fmt.Errorf("keep %s: %w", dayFile, err)
	}
	return nil
}

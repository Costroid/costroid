package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
)

// estimateFlags are what ingest-run --from focus takes beside the file.
type estimateFlags struct {
	// source is costroid/cost_source on every row.
	source string
	// optional says a file that is not there adds no rows and ends the
	// command with exit 0, as on a fork's pull request whose estimator step
	// had no secret to run with.
	optional bool
}

// ingestEstimate adds a FOCUS cost file's rows to the rows file, after any
// rows it holds, and leaves the run's result object as it is: the rows are
// not a run's. Every row is checked before one is written, so a refused file
// adds nothing.
func ingestEstimate(flags ingestRunFlags, stderr io.Writer) error {
	// A note an earlier command left would say an estimate this one reads is
	// not available.
	if err := os.Remove(estimateNotePath(flags.out)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	path := flags.source.path
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && flags.estimate.optional:
		fmt.Fprintf(stderr, "costroid: the infrastructure estimate is not available: there is no file at %s. The step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret. No rows were added to %s\n", path, flags.out)
		writeEstimateNote(flags, stderr)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("focus %s does not exist: the step that writes it did not run or wrote nothing. Pass --optional where that step can be skipped", path)
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("focus %s is not a regular file: write the estimate to a file of its own", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	read, err := focusfile.Read(file, focusfile.Options{
		Source:         flags.estimate.source,
		Tags:           readCI(stderr).Tags(),
		BillingAccount: *flags.account.billing,
	})
	if err != nil {
		return fmt.Errorf("focus %s: %w", path, err)
	}
	var rows bytes.Buffer
	writer := focus.NewWriter(focus.Shaped(&rows, flags.shape))
	for _, row := range read.Rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("focus %s: %w", path, err)
		}
	}
	if err := appendRows(flags.out, rows.Bytes()); err != nil {
		return err
	}
	summarizeEstimate(stderr, path, flags.out, read, rows.Bytes())
	return nil
}

// estimateNotePath is where ingest-run --optional notes an estimate that was
// not there: beside the rows file, named after it, as the run's object is, so
// whatever carries the rows can carry the note.
func estimateNotePath(rows string) string {
	return strings.TrimSuffix(rows, filepath.Ext(rows)) + ".infrastructure.json"
}

// writeEstimateNote leaves the note the report reads to say the estimate is
// not available. A note that cannot be written costs the comment its line and
// nothing else, so it is a warning.
func writeEstimateNote(flags ingestRunFlags, stderr io.Writer) {
	note, err := json.Marshal(focusfile.NotAvailable(flags.estimate.source))
	if err == nil {
		err = writeNew(estimateNotePath(flags.out), string(note)+"\n")
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the note that the estimate is not available was not written, so the comment will not say so: %v\n", err)
	}
}

// appendRows adds whole lines to the end of the rows file, and creates it
// when it is not there. A link in its place is refused, and so is a file whose
// last line has no line feed, which a row would join.
func appendRows(path string, rows []byte) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("the rows path %s is not a regular file, so no rows were added to it", path)
	case info.Size() > 0:
		if err := endsInLineFeed(path, info.Size()); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = file.Write(rows)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("add rows to %s: %w", path, err)
	}
	return nil
}

func endsInLineFeed(path string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, size-1); err != nil {
		return fmt.Errorf("read the end of %s: %w", path, err)
	}
	if last[0] != '\n' {
		return fmt.Errorf("the rows file %s does not end with a line feed, so a row added to it would join its last line. Write the estimate's rows after the run's rows file is complete", path)
	}
	return nil
}

// summarizeEstimate states what was added, one line per currency, basis,
// source and period, as the comment states it, and counts the rows whose tags
// were dropped.
func summarizeEstimate(stderr io.Writer, path, out string, read focusfile.File, rows []byte) {
	if len(read.Rows) == 0 {
		fmt.Fprintf(stderr, "costroid: %s holds no rows, so no rows were added to %s\n", path, out)
		return
	}
	fmt.Fprintf(stderr, "costroid: added %d %s from %s to %s, each figure read from %s, with the basis estimate\n", len(read.Rows), plural("row", len(read.Rows)), path, out, read.CostColumn)
	ledger, err := comment.ReadRows(bytes.NewReader(rows))
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	for _, figure := range ledger.Infrastructure.Figures {
		fmt.Fprintf(stderr, "costroid: %s %s for %s, estimated, from %s, %d %s\n", figure.Currency, figure.Cost, figure.Period, figure.Source, figure.Rows, plural("row", figure.Rows))
	}
	if more := ledger.Infrastructure.FiguresOmitted; more > 0 {
		fmt.Fprintf(stderr, "costroid: %d more %s\n", more, plural("figure", more))
	}
	if read.TaggedRows > 0 {
		fmt.Fprintf(stderr, "costroid: warning: %d %s of %s carried Tags, which were dropped: a file can carry any tag, and the rows take the pull request's from the CI system\n", read.TaggedRows, plural("row", read.TaggedRows), path)
	}
}

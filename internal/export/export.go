// Package export writes a rows file in the import format of another cost tool:
// FOCUS 1.4 CSV, a Vantage custom provider's CSV, or Datadog's Custom Costs
// CSV. Every amount leaves as the row wrote it. Nothing is converted, rounded
// or added up, and a tool that takes one currency refuses a row in another.
package export

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/focus"
)

// Format is a value of costroid export --format.
type Format string

// The formats export writes.
const (
	FocusCSV Format = "focus-csv"
	Vantage  Format = "vantage"
	Datadog  Format = "datadog"
)

// Formats are the formats in the order the help names them.
var Formats = []Format{FocusCSV, Vantage, Datadog}

// ErrNoRows is a rows file that holds no row, of which no format writes a file.
var ErrNoRows = errors.New("the rows file holds no rows")

// maxNamedRows bounds the rows a refusal names.
const maxNamedRows = 10

// RowsError refuses rows a format cannot take. It names the first 10 and
// counts the rest.
type RowsError struct {
	Format Format
	Rows   []string
	Reason string
}

func (e *RowsError) Error() string {
	named := e.Rows[:min(len(e.Rows), maxNamedRows)]
	text := fmt.Sprintf("%s refuses %s", e.Format, strings.Join(named, "; "))
	if rest := len(e.Rows) - len(named); rest > 0 {
		text += fmt.Sprintf(" and %d more rows", rest)
	}
	return text + ": " + e.Reason
}

// ParseFormat reads a --format value.
func ParseFormat(name string) (Format, error) {
	if slices.Contains(Formats, Format(name)) {
		return Format(name), nil
	}
	names := make([]string, len(Formats))
	for i, format := range Formats {
		names[i] = string(format)
	}
	return "", fmt.Errorf("--format is %q, and export writes %s", name, strings.Join(names, ", "))
}

// File is one file of an export and the number of rows it holds.
type File struct {
	Body []byte
	Rows int
}

// Export is what a format made of a rows file.
type Export struct {
	Files []File
	// OmittedTags are the tag keys a Datadog file has no column for, because
	// a row lacks them and Datadog takes no blank cell.
	OmittedTags []string
}

// Write reads rows as JSON lines and writes them in format. A line that is
// not a row of the contract refuses the export, as does a row the format
// cannot take, and nothing is written.
func Write(format Format, lines io.Reader) (Export, error) {
	var contract bytes.Buffer
	count, err := focus.WriteCSV(&contract, lines)
	if err != nil {
		return Export{}, err
	}
	if count == 0 {
		return Export{}, ErrNoRows
	}
	if format == FocusCSV {
		return Export{Files: []File{{Body: contract.Bytes(), Rows: count}}}, nil
	}
	rows, err := readTable(contract.Bytes())
	if err != nil {
		return Export{}, err
	}
	switch format {
	case Vantage:
		return writeVantage(rows)
	case Datadog:
		return writeDatadog(rows)
	}
	return Export{}, fmt.Errorf("export writes no format %q", format)
}

// table is the contract's CSV read back: its header and one record per row.
type table struct {
	header  []string
	records [][]string
}

func readTable(data []byte) (table, error) {
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return table{}, fmt.Errorf("read the rows as CSV: %w", err)
	}
	return table{header: records[0], records: records[1:]}, nil
}

// row is one record of a table and its place among the rows, counted from 1.
type row struct {
	number int
	cells  []string
	header []string
}

func (t table) rows() []row {
	rows := make([]row, len(t.records))
	for i, record := range t.records {
		rows[i] = row{number: i + 1, cells: record, header: t.header}
	}
	return rows
}

// cell is the row's value of a contract column, empty for null.
func (r row) cell(column string) string {
	return r.cells[slices.Index(r.header, column)]
}

// name states the row as a refusal names it: its place and its id.
func (r row) name() string {
	return fmt.Sprintf("row %d (x_CostroidRowId %s)", r.number, r.cell("x_CostroidRowId"))
}

// encodeRecord is one record as a CSV line.
func encodeRecord(record []string) ([]byte, error) {
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	if err := writer.Write(record); err != nil {
		return nil, fmt.Errorf("write a CSV record: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("write a CSV record: %w", err)
	}
	return out.Bytes(), nil
}

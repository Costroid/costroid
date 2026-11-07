package focus

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
)

// maxLineBytes bounds one line of a rows file.
const maxLineBytes = 1 << 20

// csvColumns are the row contract's columns in the order Row declares them,
// which is the order of the CSV header.
var csvColumns = rowColumns()

func rowColumns() []string {
	row := reflect.TypeFor[Row]()
	columns := make([]string, row.NumField())
	for i := range columns {
		columns[i], _, _ = strings.Cut(row.Field(i).Tag.Get("json"), ",")
	}
	return columns
}

// WriteCSV reads rows as JSON lines and writes them as the row contract's CSV:
// a header of every column, then one record per row. A null is a blank cell,
// and Tags is its JSON object, compacted, in the order the line names its keys.
// It returns the number of rows. A line that is not a row refuses the file, and
// the caller discards what was written.
func WriteCSV(out io.Writer, lines io.Reader) (int, error) {
	writer := csv.NewWriter(out)
	if err := writer.Write(csvColumns); err != nil {
		return 0, fmt.Errorf("focus: write the CSV header: %w", err)
	}
	scanner := bufio.NewScanner(lines)
	scanner.Buffer(nil, maxLineBytes)
	rows := 0
	for line := 1; scanner.Scan(); line++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		record, err := csvRecord(scanner.Bytes())
		if err != nil {
			return rows, fmt.Errorf("focus: rows line %d: %w", line, err)
		}
		if err := writer.Write(record); err != nil {
			return rows, fmt.Errorf("focus: write row %d as CSV: %w", line, err)
		}
		rows++
	}
	if err := scanner.Err(); errors.Is(err, bufio.ErrTooLong) {
		return rows, fmt.Errorf("focus: a rows line is past %d bytes", maxLineBytes)
	} else if err != nil {
		return rows, fmt.Errorf("focus: read rows: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return rows, fmt.Errorf("focus: write CSV: %w", err)
	}
	return rows, nil
}

func csvRecord(line []byte) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	var extra []string
	for name := range fields {
		if !slices.Contains(csvColumns, name) {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		return nil, fmt.Errorf("%s is not a column of the row contract", strings.Join(extra, ", "))
	}
	record := make([]string, len(csvColumns))
	for i, column := range csvColumns {
		raw, found := fields[column]
		if !found {
			return nil, fmt.Errorf("the column %s is missing", column)
		}
		cell, err := csvCell(column, raw)
		if err != nil {
			return nil, err
		}
		record[i] = cell
	}
	return record, nil
}

func csvCell(column string, raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", nil
	}
	if column == "Tags" {
		var tags map[string]string
		if err := json.Unmarshal(raw, &tags); err != nil {
			return "", errors.New("the column Tags is not an object of strings")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return "", fmt.Errorf("the column Tags: %w", err)
		}
		return compact.String(), nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", fmt.Errorf("%s is not a string or null", column)
	}
	if text == "" {
		return "", fmt.Errorf("%s is an empty string, which a CSV cell cannot tell from null", column)
	}
	return text, nil
}

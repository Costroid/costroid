package reconcile

import (
	"encoding/json"
	"fmt"
)

// Limits bound one PUT /reconciliations body as GET /quotas states them:
// max_reconciliation_lines, max_reconciliation_bytes and
// max_reconciliation_line_bytes. A nil bound is no limit.
type Limits struct {
	Lines, Bytes, LineBytes *int64
}

// Body is one PUT /reconciliations body: newline-delimited JSON, each line a
// row as --out writes it. Keys names the row of each line, in order.
type Body struct {
	Data []byte
	Keys []Key
}

// Bodies cuts rows into bodies inside the limits, in the rows' order. The
// rows of one span go in one body, the first day before its later days, so a
// reader that reads a later day by its first day's row never waits on a body
// still to come. A row or a span no body can hold is refused, and no body is
// cut or sent.
func Bodies(rows []Row, limits Limits) ([]Body, error) {
	var bodies []Body
	var current Body
	for _, unit := range spanUnits(rows) {
		var data []byte
		keys := make([]Key, 0, len(unit))
		for _, row := range unit {
			line, err := json.Marshal(row)
			if err != nil {
				return nil, fmt.Errorf("reconcile: write the row of %s %s %s: %w", row.Source, row.Credential, row.Day, err)
			}
			line = append(line, '\n')
			if limits.LineBytes != nil && int64(len(line)) > *limits.LineBytes {
				return nil, fmt.Errorf("the row of %s %s %s is %d bytes, and Costroid Platform takes at most %d in a line, its max_reconciliation_line_bytes, so nothing was sent", row.Source, row.Credential, row.Day, len(line), *limits.LineBytes)
			}
			data = append(data, line...)
			keys = append(keys, row.Key)
		}
		if err := limits.hold(unit, len(keys), len(data)); err != nil {
			return nil, err
		}
		if len(current.Keys) > 0 && limits.past(len(current.Keys)+len(keys), len(current.Data)+len(data)) {
			bodies, current = append(bodies, current), Body{}
		}
		current.Data = append(current.Data, data...)
		current.Keys = append(current.Keys, keys...)
	}
	if len(current.Keys) > 0 {
		bodies = append(bodies, current)
	}
	return bodies, nil
}

// past reports whether a body of so many lines and bytes is past a limit.
func (l Limits) past(lines, size int) bool {
	return (l.Lines != nil && int64(lines) > *l.Lines) || (l.Bytes != nil && int64(size) > *l.Bytes)
}

// hold refuses a row, or the rows of a span, that no one body can hold.
func (l Limits) hold(unit []Row, lines, size int) error {
	if !l.past(lines, size) {
		return nil
	}
	first, last := unit[0], unit[len(unit)-1]
	what := fmt.Sprintf("the row of %s %s %s holds ", first.Source, first.Credential, first.Day)
	if len(unit) > 1 {
		what = fmt.Sprintf("the %d rows of %s %s from %s to %s, a span that goes in one body, hold ", len(unit), first.Source, first.Credential, first.Day, last.Day)
	}
	if l.Lines != nil && int64(lines) > *l.Lines {
		return fmt.Errorf("%s%d lines, and Costroid Platform takes at most %d in a body, its max_reconciliation_lines, so nothing was sent", what, lines, *l.Lines)
	}
	return fmt.Errorf("%s%d bytes, and Costroid Platform takes at most %d in a body, its max_reconciliation_bytes, so nothing was sent", what, size, *l.Bytes)
}

// spanUnits groups the rows that go in one body: each span's rows together,
// and every other row alone. Rows come sorted by key, so a span's days are
// next to one another.
func spanUnits(rows []Row) [][]Row {
	var units [][]Row
	for i, row := range rows {
		if n := len(units); i > 0 && row.Span != nil && sameSpan(rows[i-1], row) {
			units[n-1] = append(units[n-1], row)
			continue
		}
		units = append(units, []Row{row})
	}
	return units
}

func sameSpan(a, b Row) bool {
	return a.Span != nil && b.Span != nil && a.Source == b.Source && a.Credential == b.Credential && *a.Span == *b.Span
}

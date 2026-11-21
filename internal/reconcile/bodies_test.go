package reconcile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func limitOf(n int64) *int64 { return &n }

// bodyRows are four rows in key order: a day alone, the two days of a span,
// and another day alone.
func bodyRows() []Row {
	span := &Span{CauseRunSpansDays, "2026-10-14", "2026-10-15"}
	row := func(credential, day string, span *Span) Row {
		return Row{Key: Key{SourceAnthropic, credential, day}, State: Matched, Currency: currency, Span: span, Notes: []string{}}
	}
	return []Row{
		row("apikey_01", "2026-10-14", nil),
		row("apikey_02", "2026-10-14", span),
		row("apikey_02", "2026-10-15", span),
		row("apikey_03", "2026-10-14", nil),
	}
}

func keysOf(bodies []Body) [][]string {
	var all [][]string
	for _, body := range bodies {
		var keys []string
		for _, key := range body.Keys {
			keys = append(keys, key.Credential+" "+key.Day)
		}
		all = append(all, keys)
	}
	return all
}

// A body holds as many lines as max_reconciliation_lines allows, and the rows
// of a span go in one body, first day first, even when that leaves room in
// the body before it. Each line is the row as --out writes it.
func TestBodiesKeepASpanInOneBody(t *testing.T) {
	rows := bodyRows()
	bodies, err := Bodies(rows, Limits{Lines: limitOf(2)})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"apikey_01 2026-10-14"}, {"apikey_02 2026-10-14", "apikey_02 2026-10-15"}, {"apikey_03 2026-10-14"}}
	if got := keysOf(bodies); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("bodies %v, want %v", got, want)
	}
	var lines bytes.Buffer
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(append(line, '\n'))
	}
	var sent []byte
	for _, body := range bodies {
		sent = append(sent, body.Data...)
	}
	if !bytes.Equal(sent, lines.Bytes()) {
		t.Errorf("the bodies hold\n%s\nwant the rows as --out writes them\n%s", sent, lines.Bytes())
	}

	whole, err := Bodies(rows, Limits{})
	if err != nil || len(whole) != 1 || len(whole[0].Keys) != 4 {
		t.Errorf("with no limit: %d bodies, %v", len(whole), err)
	}
	size := int64(len(bodies[0].Data) + len(bodies[1].Data))
	bySize, err := Bodies(rows, Limits{Bytes: limitOf(size)})
	if err != nil || len(bySize) != 2 || len(bySize[0].Keys) != 3 {
		t.Errorf("at %d bytes: %v, %v; want the first three rows, then the last", size, keysOf(bySize), err)
	}
}

// A row past max_reconciliation_line_bytes, or a span past a body's limit,
// fits no body, so nothing is cut and the refusal names it.
func TestBodiesRefuseWhatNoBodyHolds(t *testing.T) {
	rows := bodyRows()
	for name, check := range map[string]struct {
		limits Limits
		want   string
	}{
		"a line":         {Limits{LineBytes: limitOf(64)}, "the row of anthropic apikey_01 2026-10-14 is "},
		"a span's lines": {Limits{Lines: limitOf(1)}, "the 2 rows of anthropic apikey_02 from 2026-10-14 to 2026-10-15, a span that goes in one body, hold 2 lines, and Costroid Platform takes at most 1 in a body, its max_reconciliation_lines"},
		"a span's bytes": {Limits{Bytes: limitOf(400)}, ", a span that goes in one body, hold "},
		"a row's bytes":  {Limits{Bytes: limitOf(10)}, "the row of anthropic apikey_01 2026-10-14 holds "},
	} {
		t.Run(name, func(t *testing.T) {
			bodies, err := Bodies(rows, check.limits)
			if err == nil || bodies != nil || !strings.Contains(err.Error(), check.want) {
				t.Errorf("%d bodies, %v; want a refusal that reads %q", len(bodies), err, check.want)
			}
		})
	}
}

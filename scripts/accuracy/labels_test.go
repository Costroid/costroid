package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// keptRows keeps one synthetic day's rows with each tag set by retag, as a
// kept day readLabels reads.
func keptRows(t *testing.T, retag func(tags map[string]any)) keptDay {
	t.Helper()
	d := passingDay(firstDay)
	var out bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(d.rows(t)), []byte("\n")) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		retag(row["Tags"].(map[string]any))
		changed, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(changed, '\n'))
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, rowsFile), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return keptDay{directory: directory, record: dayRecord{Day: d.date, Credential: credential}}
}

func TestReadLabelsReadsANumericTagAsAbsent(t *testing.T) {
	for _, key := range []string{focus.TagAgent, focus.TagAgentVersion, focus.TagCostBasis, "team"} {
		t.Run(key, func(t *testing.T) {
			numeric := keptRows(t, func(tags map[string]any) { tags[key] = 7 })
			absent := keptRows(t, func(tags map[string]any) { delete(tags, key) })
			read, err := readLabels(numeric)
			if err != nil {
				t.Fatalf("a numeric tag refused the day: %v", err)
			}
			want, err := readLabels(absent)
			if err != nil {
				t.Fatal(err)
			}
			got, err := read.of("", "", credential)
			if err != nil {
				t.Fatal(err)
			}
			wantNamed, err := want.of("", "", credential)
			if err != nil {
				t.Fatal(err)
			}
			if got != wantNamed {
				t.Errorf("a numeric tag reads as %+v, an absent one as %+v", got, wantNamed)
			}
			if strings.Contains(got.agent, "7") {
				t.Errorf("the number was read as a string: agent %q", got.agent)
			}
		})
	}
}

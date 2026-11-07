package focus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two examples are focus-row/v0.3/examples/request-rows.jsonl and
// request-rows.csv of Costroid/costroid-schemas, copied. The digest is the
// CSV's there, so a copy that drifts fails before it is compared.
const schemasCSVDigest = "149b1b031f21348cdd5b48d7377945ab5d1b90493c91cd02ffe518547ce93b27"

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCSVMatchesTheSchemasExample(t *testing.T) {
	want := readTestdata(t, "request-rows.csv")
	if sum := sha256.Sum256(want); hex.EncodeToString(sum[:]) != schemasCSVDigest {
		t.Fatalf("testdata/request-rows.csv is not the schemas' example: sha256 %x", sum)
	}
	var got bytes.Buffer
	rows, err := WriteCSV(&got, bytes.NewReader(readTestdata(t, "request-rows.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	if rows != 11 {
		t.Errorf("%d rows, want 11", rows)
	}
	if !bytes.Equal(got.Bytes(), want) {
		gotLines, wantLines := strings.Split(got.String(), "\n"), strings.Split(string(want), "\n")
		for i := range min(len(gotLines), len(wantLines)) {
			if gotLines[i] != wantLines[i] {
				t.Fatalf("line %d differs:\ngot  %s\nwant %s", i+1, gotLines[i], wantLines[i])
			}
		}
		t.Fatalf("%d lines, want %d", len(gotLines), len(wantLines))
	}
}

func TestCSVTakesTheRowsTheWriterWrites(t *testing.T) {
	var lines bytes.Buffer
	if err := NewWriter(&lines).Write(NewRow(sampleCharge())); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rows, err := WriteCSV(&out, &lines)
	if err != nil || rows != 1 {
		t.Fatalf("%d rows: %v", rows, err)
	}
	header, record, _ := strings.Cut(out.String(), "\n")
	if header != strings.Join(csvColumns, ",") || len(csvColumns) != 40 {
		t.Fatalf("header %s", header)
	}
	if !strings.Contains(record, `,"{""costroid/agent"":""claude-code""}",0.1.0,,`) {
		t.Errorf("record %s", record)
	}
}

func TestCSVRefusesALineThatIsNotARow(t *testing.T) {
	example := strings.SplitN(string(readTestdata(t, "request-rows.jsonl")), "\n", 2)[0]
	for name, line := range map[string]string{
		"an extra column":   strings.Replace(example, `{"BilledCost"`, `{"Provider":"x","BilledCost"`, 1),
		"a missing column":  strings.Replace(example, `"BilledCost":"0",`, "", 1),
		"a JSON number":     strings.Replace(example, `"ListUnitPrice":"3"`, `"ListUnitPrice":3`, 1),
		"an empty string":   strings.Replace(example, `"SubAccountName":"CI"`, `"SubAccountName":""`, 1),
		"tags not strings":  strings.Replace(example, `"costroid/pr":"1423"`, `"costroid/pr":1423`, 1),
		"not a JSON object": "[]",
	} {
		t.Run(name, func(t *testing.T) {
			if line == example {
				t.Fatal("the change did not apply")
			}
			rows, err := WriteCSV(&bytes.Buffer{}, strings.NewReader(example+"\n"+line+"\n"))
			if err == nil || !strings.Contains(err.Error(), "line 2") {
				t.Fatalf("%d rows: %v", rows, err)
			}
		})
	}
}

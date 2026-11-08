package focus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

// profileCharges are a measured, a list-equivalent and an informational
// charge, so a strict row is read for each kind of EffectiveCost.
func profileCharges() []Charge {
	measured := sampleCharge()
	listEquivalent := sampleCharge()
	listEquivalent.Basis, listEquivalent.Class = ListEquivalent, CacheRead
	informational := sampleCharge()
	informational.Price, informational.Class = nil, ReasoningOutput
	return []Charge{measured, listEquivalent, informational}
}

func writeRows(t *testing.T, shape Shape, charges []Charge) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := NewWriter(Shaped(&out, shape))
	for _, charge := range charges {
		if err := writer.Write(NewRow(charge)); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func columnsOf(t *testing.T, line []byte) map[string]json.RawMessage {
	t.Helper()
	var columns map[string]json.RawMessage
	if err := json.Unmarshal(line, &columns); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	return columns
}

// TestStrictProfile writes the same charges in both profiles. Every strict row
// reads "0" in BilledCost and EffectiveCost and carries the default row's
// EffectiveCost in x_CostroidMeasuredCost, byte for byte. Every other column is
// the default row's own, and a default row has no x_CostroidMeasuredCost.
func TestStrictProfile(t *testing.T) {
	charges := profileCharges()
	defaults := bytes.Split(bytes.TrimSuffix(writeRows(t, Shape{}, charges), []byte{'\n'}), []byte{'\n'})
	stricts := bytes.Split(bytes.TrimSuffix(writeRows(t, Shape{Profile: ProfileStrict}, charges), []byte{'\n'}), []byte{'\n'})
	if len(defaults) != len(charges) || len(stricts) != len(charges) {
		t.Fatalf("%d default rows and %d strict rows, want %d of each", len(defaults), len(stricts), len(charges))
	}
	measuredSeen := false
	for i := range charges {
		plain, strict := columnsOf(t, defaults[i]), columnsOf(t, stricts[i])
		if _, found := plain[ColumnMeasuredCost]; found {
			t.Errorf("row %d: the default profile writes %s", i+1, ColumnMeasuredCost)
		}
		if string(plain["EffectiveCost"]) != zeroCost {
			measuredSeen = true
		}
		for _, column := range []string{"BilledCost", "EffectiveCost"} {
			if got := string(strict[column]); got != zeroCost {
				t.Errorf("row %d: strict %s reads %s, want %s", i+1, column, got, zeroCost)
			}
		}
		if got, want := strict[ColumnMeasuredCost], plain["EffectiveCost"]; !bytes.Equal(got, want) {
			t.Errorf("row %d: %s reads %s, the default EffectiveCost %s", i+1, ColumnMeasuredCost, got, want)
		}
		for column, value := range plain {
			if column != "EffectiveCost" && !bytes.Equal(strict[column], value) {
				t.Errorf("row %d: strict %s reads %s, the default %s", i+1, column, strict[column], value)
			}
		}
		if len(strict) != len(plain)+1 {
			t.Errorf("row %d: %d strict columns, %d default", i+1, len(strict), len(plain))
		}
	}
	if !measuredSeen {
		t.Fatal("no default row has an EffectiveCost above 0, so the test proves nothing")
	}
}

// TestShapedTagsReadAsTheWriterWritesThem shapes rows with tags. Each line is
// the one Writer writes for a row that holds the tags itself, so a shaped row
// keeps the column order and form of every other row.
func TestShapedTagsReadAsTheWriterWritesThem(t *testing.T) {
	added := map[string]string{TagWorkItem: "ENG-142", TagArm: "b", TagTeam: "platform"}
	charges := profileCharges()
	shaped := writeRows(t, Shape{Tags: added}, charges)
	for i := range charges {
		tags := maps.Clone(charges[i].Tags)
		maps.Copy(tags, added)
		charges[i].Tags = tags
	}
	if want := writeRows(t, Shape{}, charges); !bytes.Equal(shaped, want) {
		t.Errorf("shaped rows:\n%s\nwant:\n%s", shaped, want)
	}
}

func TestADefaultShapeWritesToTheFileItself(t *testing.T) {
	var out bytes.Buffer
	if writer := Shaped(&out, Shape{Profile: ProfileDefault}); writer != &out {
		t.Errorf("a shape that changes nothing wraps the file in %T", writer)
	}
}

func TestShapedRowsRefuseWhatTheyCannotShape(t *testing.T) {
	var row bytes.Buffer
	if err := NewWriter(&row).Write(NewRow(sampleCharge())); err != nil {
		t.Fatal(err)
	}
	line := row.String()
	strict := Shape{Profile: ProfileStrict}
	edit := func(old, new string) string {
		edited := strings.Replace(line, old, new, 1)
		if edited == line {
			t.Fatalf("%q is not in the row", old)
		}
		return edited
	}
	for name, tc := range map[string]struct {
		shape Shape
		write string
		want  string
	}{
		"part of a row":         {strict, strings.TrimSuffix(line, "\n"), "ends inside a row"},
		"a tag the row holds":   {Shape{Tags: map[string]string{TagAgent: "codex"}}, line, "already holds the tag costroid/agent"},
		"a strict row again":    {strict, edit(`{"BilledCost"`, `{"x_CostroidMeasuredCost":"1","BilledCost"`), "already has x_CostroidMeasuredCost"},
		"no EffectiveCost":      {strict, edit(`"EffectiveCost":"5",`, ""), "no EffectiveCost"},
		"a line that is no row": {strict, "[]\n", "shape a row"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := Shaped(&out, tc.shape).Write([]byte(tc.write))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want one naming %q", err, tc.want)
			}
			if out.Len() != 0 {
				t.Errorf("wrote %q", out.String())
			}
		})
	}
}

// The strict example is focus-row/v0.4/examples/request-rows-strict.jsonl of
// Costroid/costroid-schemas, copied. The digest is the file's there.
const schemasStrictDigest = "ce29b976abd6dfbe0d4f95f1e54f805c5d5495fce24240e8cb6ee14628239604"

// TestStrictRowsMatchTheSchemasExample shapes the contract's default example
// rows in the strict profile. The result is the contract's strict example,
// byte for byte.
func TestStrictRowsMatchTheSchemasExample(t *testing.T) {
	want := readTestdata(t, "request-rows-strict.jsonl")
	if sum := sha256.Sum256(want); hex.EncodeToString(sum[:]) != schemasStrictDigest {
		t.Fatalf("testdata/request-rows-strict.jsonl is not the schemas' example: sha256 %x", sum)
	}
	var got bytes.Buffer
	if _, err := Shaped(&got, Shape{Profile: ProfileStrict}).Write(readTestdata(t, "request-rows.jsonl")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("strict rows:\n%s\nwant:\n%s", got.Bytes(), want)
	}
}

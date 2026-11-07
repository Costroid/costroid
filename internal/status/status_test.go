package status

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// schemaPath is status/v0.1 of Costroid/costroid-schemas, copied. A published
// version never changes in place, so the copy cannot go stale.
const schemaPath = "testdata/status.schema.json"

const agentsDoc = "../../docs/agents.md"

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("status.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("status.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validate(schema *jsonschema.Schema, data []byte) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

// wideStatus is a status of many entries, so that writing it takes long
// enough for a reader to meet a file half written, were it written in place.
func wideStatus(serial int) Status {
	measured := focus.Measured
	spend := make([]Spend, 0, 200)
	for entry := range 200 {
		spend = append(spend, Spend{Currency: fmt.Sprintf("X%02d", entry%100), CostBasis: &measured, ListCost: decimal.FromInt(int64(serial))})
	}
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	return Status{
		State: Running, Agent: "codex", Updates: EachRequest,
		StartedAt: at, UpdatedAt: at.Add(time.Duration(serial) * time.Millisecond),
		Spend: spend, Requests: &serial, Cap: NewCap(decimal.FromInt(5), spend), EnforcementGrade: "wrapper-watchdog",
	}
}

func TestStatusFileIsWrittenAtomically(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "costroid-status.json")
	const writes = 1000
	failed := make(chan error, 1)
	go func() {
		defer close(failed)
		for serial := range writes {
			if err := Write(path, wideStatus(serial)); err != nil {
				failed <- err
				return
			}
		}
	}()
	reads, last := 0, -1
	for running := true; running; {
		select {
		case err := <-failed:
			if err != nil {
				t.Fatal(err)
			}
			running = false
		default:
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var read struct {
			Requests *int              `json:"requests"`
			Spend    []json.RawMessage `json:"spend"`
		}
		if err := json.Unmarshal(data, &read); err != nil {
			t.Fatalf("read %d met a file that is not one whole status: %v", reads+1, err)
		}
		if read.Requests == nil || len(read.Spend) != 200 || *read.Requests < last {
			t.Fatalf("read %d met %d entries, request count %v after %d", reads+1, len(read.Spend), read.Requests, last)
		}
		last = *read.Requests
		reads++
	}
	if reads < 10 {
		t.Fatalf("only %d reads met the file while it was written", reads)
	}
	if last != writes-1 {
		t.Errorf("the file holds write %d, want the last, %d", last, writes-1)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "costroid-status.json" {
		t.Errorf("the directory holds %v, want the status file alone", entries)
	}
}

func TestAFailedStatusWriteLeavesNoFileBehind(t *testing.T) {
	directory := t.TempDir()
	taken := filepath.Join(directory, "costroid-status.json")
	if err := os.MkdirAll(filepath.Join(taken, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(taken, wideStatus(1)); err == nil {
		t.Fatal("a status renamed over a directory was written")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %v after the failed write", entries)
	}
}

func TestStatusPathReadsTheVariableThenRunnerTemp(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{FileVariable: "/a/status.json", "RUNNER_TEMP": "/temp"}, "/a/status.json"},
		{map[string]string{"RUNNER_TEMP": "/temp"}, "/temp/costroid-status.json"},
		{map[string]string{}, ""},
	} {
		if got := Path(func(name string) string { return tc.env[name] }); got != tc.want {
			t.Errorf("%v gave %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestStatusCapComparesOneFigureInUSD(t *testing.T) {
	measured, subscription := focus.Measured, focus.ListEquivalent
	five := decimal.MustParse("5")
	for _, tc := range []struct {
		name                 string
		spend                []Spend
		remainder, overshoot string
	}{
		{"spend not known", nil, "", ""},
		{"nothing spent", []Spend{}, "5", "0"},
		{"within the cap", []Spend{{Currency: "USD", CostBasis: &measured, ListCost: decimal.MustParse("0.0369328")}}, "4.9630672", "0"},
		{"past the cap", []Spend{{Currency: "USD", ListCost: decimal.MustParse("5.25")}}, "0", "0.25"},
		{"at the cap", []Spend{{Currency: "USD", CostBasis: &measured, ListCost: five}}, "0", "0"},
		{"two bases in USD", []Spend{{Currency: "USD", CostBasis: &measured, ListCost: five}, {Currency: "USD", CostBasis: &subscription, ListCost: five}}, "", ""},
		{"another currency only", []Spend{{Currency: "EUR", CostBasis: &measured, ListCost: five}}, "5", "0"},
	} {
		held := NewCap(five, tc.spend)
		if got, want := text(held.Remainder), tc.remainder; got != want {
			t.Errorf("%s: remainder %q, want %q", tc.name, got, want)
		}
		if got, want := text(held.Overshoot), tc.overshoot; got != want {
			t.Errorf("%s: overshoot %q, want %q", tc.name, got, want)
		}
	}
}

func text(amount *decimal.Decimal) string {
	if amount == nil {
		return ""
	}
	return amount.String()
}

// statusSection is the agents page's section on the status file.
var statusSection = regexp.MustCompile("(?s)\n## Read the run's status\n(.*?)(\n## |$)")

var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

func TestAgentsPageStatusExamplesMatchTheSchema(t *testing.T) {
	page, err := os.ReadFile(agentsDoc)
	if err != nil {
		t.Fatal(err)
	}
	section := statusSection.FindSubmatch(page)
	if section == nil {
		t.Fatal("docs/agents.md has no section on the status file")
	}
	blocks := jsonBlock.FindAllSubmatch(section[1], -1)
	if len(blocks) == 0 {
		t.Fatal("the status section shows no status")
	}
	schema := compileSchema(t)
	for i, block := range blocks {
		if err := validate(schema, block[1]); err != nil {
			t.Errorf("example %d: %v", i+1, err)
		}
	}
}

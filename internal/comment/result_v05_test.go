package comment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/run"
)

// resultSchemaPath is result/v0.5 of Costroid/costroid-schemas, copied.
var resultSchemaPath = filepath.Join("testdata", "result.schema.json")

func compileResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(resultSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("result.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("result.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func resultSchemaErrors(schema *jsonschema.Schema, object string) error {
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(object))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

// emittedBlocks is every kind of object a report or a run writes: the check
// run's, the comment's copy shown and withheld, the run's own before any
// check, and each state without figures.
func emittedBlocks(t *testing.T) map[string]string {
	t.Helper()
	blocks := map[string]string{}
	add := func(name string, input Input, check bool) {
		var (
			block string
			err   error
		)
		if check {
			block, err = Result(input)
		} else {
			block, err = input.block()
		}
		if err != nil {
			t.Fatal(err)
		}
		blocks[name] = block
	}
	add("check run", exampleInput(t), true)
	add("comment copy", exampleInput(t), false)
	withheld := exampleInput(t)
	withheld.Amounts = AmountsOff
	add("withheld comment copy", withheld, false)
	before := realInput(t)
	before.BeforeCheck = true
	add("run before a check", before, true)
	refused := Input{State: NoRows, Reason: EmptyRowsFile, RunReason: run.ReasonReaderRefused, ReaderLine: "the result object could not be read", BeforeCheck: true, Amounts: AmountsShown}
	add("reader refused", refused, true)
	add("no rows file", Input{State: Unreadable, Reason: NoRowsFile, Amounts: AmountsShown}, true)
	add("fork with no rows", Input{State: Unreadable, Reason: ForkNoRows, Amounts: AmountsShown}, true)
	add("invalid row", Input{State: Unreadable, Reason: InvalidRow, ReasonLine: 3, Amounts: AmountsShown}, true)
	return blocks
}

func TestResultV05Validates(t *testing.T) {
	schema := compileResultSchema(t)
	for name, block := range emittedBlocks(t) {
		var head struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal([]byte(block), &head); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if head.Schema != "costroid.result/0.5" {
			t.Errorf("%s: schema = %q, want costroid.result/0.5", name, head.Schema)
		}
		if err := resultSchemaErrors(schema, block); err != nil {
			t.Errorf("%s breaks result/v0.5: %v\n%s", name, err, block)
		}
	}
}

func TestResultV05NewFieldsAreNullUntilStated(t *testing.T) {
	for name, block := range emittedBlocks(t) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(block), &fields); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, key := range []string{"agent_error", "agent_skipped", "ci_run"} {
			value, present := fields[key]
			if !present {
				t.Errorf("%s: no %s key", name, key)
			} else if string(value) != "null" {
				t.Errorf("%s: %s = %s, want null", name, key, value)
			}
		}
	}
}

func TestResultV05FilledFieldsMatchTheSchema(t *testing.T) {
	schema := compileResultSchema(t)
	encoded, err := Result(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	var block Block
	if err := json.Unmarshal([]byte(encoded), &block); err != nil {
		t.Fatal(err)
	}
	ran := false
	block.AgentError = &BlockAgentError{Class: "rate_limit"}
	block.AgentSkipped = &ran
	block.CIRun = &BlockCIRun{ID: 19203384756, Attempt: 2}
	filled, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	if err := resultSchemaErrors(schema, string(filled)); err != nil {
		t.Errorf("a filled object breaks result/v0.5: %v\n%s", err, filled)
	}
	block.AgentError.Class = "the agent's own words"
	refused, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	if resultSchemaErrors(schema, string(refused)) == nil {
		t.Errorf("a class outside the closed list passed result/v0.5:\n%s", refused)
	}
}

// TestResultV05BlockFieldsMatchTheSchema holds Block to the copied schema in
// both directions, so neither gains a field the other lacks.
func TestResultV05BlockFieldsMatchTheSchema(t *testing.T) {
	data, err := os.ReadFile(resultSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(document.ID, "/result/v0.5/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.5", document.ID)
	}
	var fromSchema []string
	for name := range document.Properties {
		fromSchema = append(fromSchema, name)
	}
	var fromBlock []string
	blockType := reflect.TypeFor[Block]()
	for index := range blockType.NumField() {
		name, _, _ := strings.Cut(blockType.Field(index).Tag.Get("json"), ",")
		fromBlock = append(fromBlock, name)
	}
	slices.Sort(fromSchema)
	slices.Sort(fromBlock)
	if !slices.Equal(fromSchema, fromBlock) {
		t.Errorf("Block's keys %v, the schema's %v", fromBlock, fromSchema)
	}
	var constant struct {
		Const string `json:"const"`
	}
	if err := json.Unmarshal(document.Properties["schema"], &constant); err != nil {
		t.Fatal(err)
	}
	if constant.Const != schema {
		t.Errorf("the schema names %q, Block emits %q", constant.Const, schema)
	}
}

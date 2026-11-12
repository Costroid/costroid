package comment

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/focusfile"
)

// resultV010SchemaPath is result/v0.10 of Costroid/costroid-schemas, copied.
var resultV010SchemaPath = filepath.Join("testdata", "result-v0.10.schema.json")

// resultV010SchemaSHA256 is the SHA-256 of result/v0.10/result.schema.json in
// Costroid/costroid-schemas at df18465, the commit that held an estimate's
// basis to estimate:
//
//	git show df18465:result/v0.10/result.schema.json | sha256sum
const resultV010SchemaSHA256 = "5f70e2f42484ae21581710cf2f315a9aff24a9fe60f2a34d244a8d94fb402ba0"

func compileSchemaFile(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(filepath.Base(path), document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// otherSource is a second estimator whose file was not there.
const otherSource = "cluster-estimate"

// estimatedInput is statedInput, with the rows' digests, a pull request's
// total, gh-aw's credits and a verdict, plus a FOCUS cost file's rows in two
// currencies after the agent's, and the note of a second estimate that was
// not there.
func estimatedInput(t *testing.T) Input {
	t.Helper()
	input := withEstimate(t, statedInput(t), twoCurrencies)
	note := focusfile.NotAvailable(otherSource)
	input.EstimateUnavailable = &note
	return input
}

// withoutEstimate is the same report on the agent's rows alone, with no note.
func withoutEstimate(t *testing.T, input Input) Input {
	t.Helper()
	input.Ledger = realLedger(t)
	input.EstimateUnavailable = nil
	return input
}

// copies are the objects a report writes for one input: the check run's, the
// comment's shown and withheld, and the run's own before any check.
func copies(t *testing.T, input Input) map[string]string {
	t.Helper()
	objects := map[string]string{}
	check, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	objects["check run"] = check
	if objects["comment copy"], err = input.block(); err != nil {
		t.Fatal(err)
	}
	if objects["withheld comment copy"], err = withheld(input).block(); err != nil {
		t.Fatal(err)
	}
	before := input
	before.BeforeCheck = true
	if objects["run before a check"], err = Result(before); err != nil {
		t.Fatal(err)
	}
	return objects
}

// TestMachineBlockStatesTheEstimateApart states an estimate in two
// currencies, and an estimate that was not available, in result/v0.10's
// estimates. Every copy holds to the schema. With estimates and
// estimates_omitted taken out and the schema string set back, each copy is the
// result/v0.9 object the same report writes without the estimate, byte for
// byte: no figure, cap, total, verdict or agent field moves. The action's
// outputs other than result do not change either.
func TestMachineBlockStatesTheEstimateApart(t *testing.T) {
	schema := compileSchemaFile(t, resultV010SchemaPath)
	input := estimatedInput(t)
	plain := copies(t, withoutEstimate(t, input))
	shown := []BlockEstimate{
		{Source: "infrastructure", Currency: "EUR", ChargePeriodStart: "2026-12-01T00:00:00Z", ChargePeriodEnd: "2027-01-01T00:00:00Z", CostBasis: "estimate", ListCost: "25.488", Rows: 1},
		{Source: "infrastructure", Currency: "USD", ChargePeriodStart: "2026-12-01T00:00:00Z", ChargePeriodEnd: "2027-01-01T00:00:00Z", CostBasis: "estimate", ListCost: "1000", Rows: 1},
		{Source: otherSource, Reason: focusfile.ReasonNoFile},
	}
	for name, object := range copies(t, input) {
		if err := resultSchemaErrors(schema, object); err != nil {
			t.Errorf("%s breaks result/v0.10: %v\n%s", name, err, object)
		}
		var block Block
		if err := json.Unmarshal([]byte(object), &block); err != nil {
			t.Fatal(err)
		}
		want := slices.Clone(shown)
		if name == "withheld comment copy" {
			for index := range want {
				want[index].ListCost = ""
			}
		}
		if block.Schema != "costroid.result/0.10" || !reflect.DeepEqual(block.Estimates, want) || block.EstimatesOmitted != 0 {
			t.Errorf("%s: schema %s, estimates %+v (%d omitted), want %+v", name, block.Schema, block.Estimates, block.EstimatesOmitted, want)
		}
		block.Schema, block.Estimates, block.EstimatesOmitted = schemaV09, nil, 0
		apart, err := block.encode()
		if err != nil {
			t.Fatal(err)
		}
		if apart != plain[name] {
			t.Errorf("%s: without its estimates the object is\n%s\nand the report without the estimate writes\n%s", name, apart, plain[name])
		}
	}
	outputs := func(input Input) map[string]string {
		t.Helper()
		listed, err := Outputs(input)
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, output := range listed {
			if output.Name != "result" {
				values[output.Name] = output.Value
			}
		}
		return values
	}
	if got, want := outputs(input), outputs(withoutEstimate(t, input)); !maps.Equal(got, want) {
		t.Errorf("the estimate moved the outputs:\n%v\nwant\n%v", got, want)
	}
}

// TestMachineBlockStatesEveryEstimateFigureOrCountsIt holds the estimates to
// the comment's cap of 20 figures: the rest are counted, never dropped
// without a count.
func TestMachineBlockStatesEveryEstimateFigureOrCountsIt(t *testing.T) {
	file := focusHeader
	for day := 1; day <= maxFigures+2; day++ {
		file += fmt.Sprintf("EUR,2027-01-%02dT00:00:00Z,2027-01-%02dT00:00:00Z,1,aws_instance.web,Amazon EC2,example-account-a\n", day, day+1)
	}
	object, err := Result(withEstimate(t, realInput(t), file))
	if err != nil {
		t.Fatal(err)
	}
	if err := resultSchemaErrors(compileSchemaFile(t, resultV010SchemaPath), object); err != nil {
		t.Errorf("the object breaks result/v0.10: %v", err)
	}
	var block Block
	if err := json.Unmarshal([]byte(object), &block); err != nil {
		t.Fatal(err)
	}
	if len(block.Estimates) != maxFigures || block.EstimatesOmitted != 2 {
		t.Errorf("%d estimates, %d omitted, want %d and 2", len(block.Estimates), block.EstimatesOmitted, maxFigures)
	}
}

// TestResultWithoutAnEstimateIsWhatItWas holds every golden state that states
// no estimate to result/v0.9: its schema string, no estimates field, and the
// copied 0.9 schema. A note whose reason is off the closed list states no
// line, so it states no estimate either.
func TestResultWithoutAnEstimateIsWhatItWas(t *testing.T) {
	schema := compileResultSchema(t)
	states := goldenStates(t)
	unknown := realInput(t)
	unknown.EstimateUnavailable = &focusfile.Unavailable{Estimate: "not_available", Reason: "estimator_failed", Source: "infrastructure"}
	states["a note off the closed list"] = unknown
	stated := 0
	for name, input := range states {
		if len(input.Ledger.Infrastructure.Figures) > 0 || input.estimateUnavailable() != "" {
			stated++
			continue
		}
		for file, object := range machineBlocks(t, input) {
			object = strings.TrimSuffix(strings.TrimPrefix(object, "```json\n"), "\n```")
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(object), &fields); err != nil {
				t.Fatalf("%s/%s: %v", name, file, err)
			}
			_, estimates := fields["estimates"]
			_, omitted := fields["estimates_omitted"]
			if string(fields["schema"]) != `"costroid.result/0.9"` || estimates || omitted {
				t.Errorf("%s/%s: schema %s, estimates %t, estimates_omitted %t", name, file, fields["schema"], estimates, omitted)
			}
			if err := resultSchemaErrors(schema, object); err != nil {
				t.Errorf("%s/%s breaks result/v0.9: %v", name, file, err)
			}
		}
	}
	if stated != 4 {
		t.Errorf("%d golden states state an estimate, want the 4 infrastructure states", stated)
	}
}

// olderObject rewrites a current object as a release that wrote an older
// version wrote it: without the fields that version does not hold.
func olderObject(t *testing.T, current, version string, drop ...string) string {
	t.Helper()
	older := strings.Replace(current, `"costroid.result/0.9"`, `"`+version+`"`, 1)
	for _, field := range drop {
		older = strings.Replace(older, ",\n  \""+field+"\": null", "", 1)
	}
	if older == current {
		t.Fatalf("no %s object was made", version)
	}
	return older
}

// TestOlderResultsStillRead reads an object of each version from 0.6 to 0.10,
// as the release that wrote it wrote it, and writes it again byte for byte,
// as costroid approve and costroid result do. It refuses a 0.9 object that
// holds estimates, and a 0.10 object that states none. A check that repeats a
// 0.10 object with another conclusion keeps its estimates.
func TestOlderResultsStillRead(t *testing.T) {
	v09, err := Result(withoutEstimate(t, estimatedInput(t)))
	if err != nil {
		t.Fatal(err)
	}
	unstated := withoutEstimate(t, estimatedInput(t))
	unstated.AgentStated = nil
	v09Unstated, err := Result(unstated)
	if err != nil {
		t.Fatal(err)
	}
	v010, err := Result(estimatedInput(t))
	if err != nil {
		t.Fatal(err)
	}
	for version, object := range map[string]string{
		"0.6":  olderObject(t, v09Unstated, schemaV06, "agent_stated", "recorded"),
		"0.7":  olderObject(t, v09, schemaV07, "recorded"),
		"0.8":  olderObject(t, v09, schemaV08),
		"0.9":  v09,
		"0.10": v010,
	} {
		block, err := ParseResult(fenced(object))
		if err != nil {
			t.Errorf("%s: %v", version, err)
			continue
		}
		if again, err := block.encode(); err != nil || again != object {
			t.Errorf("%s: written again as\n%s\nwant\n%s (%v)", version, again, object, err)
		}
		if block.Schema != "costroid.result/"+version {
			t.Errorf("%s: read as %s", version, block.Schema)
		}
	}
	const omitted = `"estimates_omitted": 0`
	estimates := v010[strings.Index(v010, ",\n  \"estimates\": [") : strings.Index(v010, omitted)+len(omitted)]
	for name, refused := range map[string]struct{ object, says string }{
		"a 0.9 object that holds estimates": {
			strings.Replace(v010, `"costroid.result/0.10"`, `"costroid.result/0.9"`, 1),
			"the field estimates, which only costroid.result/0.10 defines",
		},
		"a 0.10 object that states none": {
			strings.Replace(v010, estimates, "", 1),
			"states no estimate",
		},
		"a 0.10 object with an empty list": {
			strings.Replace(v010, estimates, ",\n  \"estimates\": [],\n  \"estimates_omitted\": 0", 1),
			"states no estimate",
		},
	} {
		if _, err := ParseResult(fenced(refused.object)); err == nil || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	block, err := ParseResult(fenced(v010))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Reconclude(block, nil, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Text, estimates) || !strings.Contains(output.Text, `"costroid.result/0.10"`) {
		t.Errorf("the repeated check lost its estimates:\n%s", output.Text)
	}
}

// TestResultV010SchemaCopyMatchesItsDigest holds the copied result/v0.10
// schema to the published file byte for byte, and Block's keys to its
// properties.
func TestResultV010SchemaCopyMatchesItsDigest(t *testing.T) {
	data, err := os.ReadFile(resultV010SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != resultV010SchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and result/v0.10 as published is %s", got, resultV010SchemaSHA256)
	}
	var document struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(document.ID, "/result/v0.10/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.10", document.ID)
	}
	if fromSchema, fromBlock := slices.Sorted(maps.Keys(document.Properties)), blockKeys(); !slices.Equal(fromSchema, fromBlock) {
		t.Errorf("Block's keys %v, the schema's %v", fromBlock, fromSchema)
	}
	if string(document.Properties["schema"]) == "" || !strings.Contains(string(document.Properties["schema"]), `"`+schema+`"`) {
		t.Errorf("the schema names %s, Block emits %q", document.Properties["schema"], schema)
	}
}

// blockKeys are Block's JSON keys, sorted.
func blockKeys() []string {
	var keys []string
	blockType := reflect.TypeFor[Block]()
	for index := range blockType.NumField() {
		name, _, _ := strings.Cut(blockType.Field(index).Tag.Get("json"), ",")
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return keys
}

// TestWithheldBlockCarriesNoEstimateAmount withholds amounts as
// comment.show_cost false does. The comment's copy of the block drops each
// estimate's list_cost as it drops each figure's, and keeps what a figure
// keeps: currency, basis and rows, with the estimate's source and period. No
// amount of the estimate is anywhere in the comment. The check run's block
// keeps every amount, and the comment's sentence says where the amounts are:
// the rows and, on GitHub, the check run's machine block.
func TestWithheldBlockCarriesNoEstimateAmount(t *testing.T) {
	input := withheld(withEstimate(t, realInput(t), twoCurrencies))
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	copied := strings.TrimSuffix(strings.TrimPrefix(fencedBlock.FindString(body), "```json\n"), "\n```")
	var block Block
	if err := json.Unmarshal([]byte(copied), &block); err != nil {
		t.Fatal(err)
	}
	if block.Amounts != "withheld" || len(block.Estimates) != 2 || len(block.Figures) != 1 {
		t.Fatalf("the withheld copy reads %s with %d estimates and %d figures", block.Amounts, len(block.Estimates), len(block.Figures))
	}
	figure := block.Figures[0]
	if figure.ListCost != "" || figure.Currency == "" || figure.Rows == 0 {
		t.Fatalf("the withheld rule for figures moved: %+v", figure)
	}
	for _, estimate := range block.Estimates {
		if estimate.ListCost != "" || estimate.Currency == "" || estimate.CostBasis != "estimate" || estimate.Rows != 1 ||
			estimate.Source != "infrastructure" || estimate.ChargePeriodStart == "" || estimate.ChargePeriodEnd == "" {
			t.Errorf("a withheld estimate reads %+v", estimate)
		}
	}
	if err := resultSchemaErrors(compileSchemaFile(t, resultV010SchemaPath), copied); err != nil {
		t.Errorf("the withheld copy breaks result/v0.10: %v", err)
	}
	for _, amount := range []string{"25.488", "1000"} {
		if strings.Contains(body, amount) {
			t.Errorf("the withheld comment states %s", amount)
		}
	}
	check, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(check, `"list_cost": "25.488"`) || !strings.Contains(check, `"list_cost": "1000"`) {
		t.Errorf("the check run's block lost an estimate's amount:\n%s", check)
	}
	if !strings.Contains(body, " "+withheldLineCheckRuns+"\n") {
		t.Errorf("the comment does not say the check run's block carries the amounts:\n%s", readable(body))
	}
	input.AccountHost = AccountGitLab
	note, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, " "+withheldLine+"\n") || strings.Contains(note, withheldLineCheckRuns) {
		t.Errorf("a GitLab note names a check run, or not where the amounts are:\n%s", readable(note))
	}
}

// TestMachineBlockStatesAnEstimateAtItsBasisAlone reads rows that state a cost
// source and no agent at the basis measured beside an estimate. The comment
// states both; the block's estimates hold the estimate alone, at the basis
// estimate, and the object holds to result/v0.10.
func TestMachineBlockStatesAnEstimateAtItsBasisAlone(t *testing.T) {
	estimate := estimateLines(t, twoCurrencies)
	measured := strings.Replace(estimateLines(t, twoPeriods), `"costroid/cost_basis":"estimate"`, `"costroid/cost_basis":"measured"`, -1)
	if measured == estimateLines(t, twoPeriods) {
		t.Fatal("no measured row was made")
	}
	agent, err := os.ReadFile(goldenRowsFile)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadRows(strings.NewReader(string(agent) + estimate + measured))
	if err != nil {
		t.Fatal(err)
	}
	input := realInput(t)
	input.Ledger = ledger
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Infrastructure, measured:") {
		t.Fatalf("the comment does not state the measured rows:\n%s", readable(body))
	}
	object, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	var block Block
	if err := json.Unmarshal([]byte(object), &block); err != nil {
		t.Fatal(err)
	}
	if len(block.Estimates) != 2 || block.Estimates[0].CostBasis != "estimate" || block.Estimates[1].CostBasis != "estimate" {
		t.Errorf("estimates %+v, want the two estimate figures alone", block.Estimates)
	}
	if err := resultSchemaErrors(compileSchemaFile(t, resultV010SchemaPath), object); err != nil {
		t.Errorf("the object breaks result/v0.10: %v", err)
	}
}

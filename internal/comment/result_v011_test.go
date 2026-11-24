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

	"github.com/Costroid/costroid/internal/policy"
)

// resultV011SchemaPath is result/v0.11 of Costroid/costroid-schemas, copied.
var resultV011SchemaPath = filepath.Join("testdata", "result-v0.11.schema.json")

// resultV011SchemaSHA256 is the SHA-256 of result/v0.11/result.schema.json as
// Costroid/costroid-schemas publishes it:
//
//	git show main:result/v0.11/result.schema.json | sha256sum
const resultV011SchemaSHA256 = "db358b90a150e08705056028ea549ec3df3f40766c12811436cbf544ade184f9"

// holdsP95 reports an input whose verdict holds a result of run_p95, the one
// value of result/v0.11 this release writes.
func holdsP95(input Input) bool {
	return input.Verdict != nil && slices.ContainsFunc(input.Verdict.Results, func(result policy.Result) bool {
		return result.Rule == policy.RuleRunP95
	})
}

// withoutP95 is the input with its verdict's run_p95 result taken out and
// every other field of the verdict kept.
func withoutP95(input Input) Input {
	verdict := *input.Verdict
	verdict.Results = slices.DeleteFunc(slices.Clone(verdict.Results), func(result policy.Result) bool {
		return result.Rule == policy.RuleRunP95
	})
	input.Verdict = &verdict
	return input
}

// TestResultV011SchemaCopyMatchesItsDigest holds the copied result/v0.11
// schema to the file that adds it byte for byte, Block's keys to its
// properties, and each key of a policy result to a property it states.
func TestResultV011SchemaCopyMatchesItsDigest(t *testing.T) {
	data, err := os.ReadFile(resultV011SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != resultV011SchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and result/v0.11 is %s", got, resultV011SchemaSHA256)
	}
	var document struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       struct {
			Policy struct {
				Properties struct {
					Results struct {
						Items struct {
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"items"`
					} `json:"results"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(document.ID, "/result/v0.11/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.11", document.ID)
	}
	if fromSchema, fromBlock := slices.Sorted(maps.Keys(document.Properties)), blockKeys(); !slices.Equal(fromSchema, fromBlock) {
		t.Errorf("Block's keys %v, the schema's %v", fromBlock, fromSchema)
	}
	if !strings.Contains(string(document.Properties["schema"]), `"`+schemaV011+`"`) {
		t.Errorf("the schema names %s, Block emits %q", document.Properties["schema"], schemaV011)
	}
	resultType := reflect.TypeFor[policy.Result]()
	for index := range resultType.NumField() {
		name, _, _ := strings.Cut(resultType.Field(index).Tag.Get("json"), ",")
		if _, stated := document.Defs.Policy.Properties.Results.Items.Properties[name]; !stated && !slices.Contains(resultKeysAddedIn012, name) && !slices.Contains(resultKeysAddedIn014, name) {
			t.Errorf("a policy result's key %s is not one result/v0.11 states", name)
		}
	}
}

// TestAnObjectWithNothingNewIsNotWrittenAsV011 holds every golden state's
// objects, as the comment, the check run and the result output carry them and
// as a withheld copy and a run before its check write them, to one rule: an
// object without a pack or a receipt is result/v0.11 exactly when its
// verdict holds a run_p95 result, and holds to that version's copied schema. Every other
// object without a pack is the result/v0.9 or result/v0.10 it was, holds no
// key only result/v0.11 defines, and holds to its own version's copied
// schema. An object with a pack is result/v0.12, which
// TestAnObjectWithAPackIsWrittenAsV012 holds. The goldens hold each object
// byte for byte.
func TestAnObjectWithNothingNewIsNotWrittenAsV011(t *testing.T) {
	compiled := map[string]string{schemaV09: resultSchemaPath, schema: resultV010SchemaPath, schemaV011: resultV011SchemaPath, schemaV012: resultV012SchemaPath, schemaV013: resultV013SchemaPath, schemaV014: resultV014SchemaPath}
	newer := []string{`"run_p95"`, `"p95_usd"`, `"limit_factor"`, `"baseline_runs"`, `"pack"`, `"customer_pack"`, `"line"`}
	written := map[string]int{}
	for name, input := range goldenStates(t) {
		objects := copies(t, input)
		for file, object := range machineBlocks(t, input) {
			objects[file] = strings.TrimSuffix(strings.TrimPrefix(object, "```json\n"), "\n```")
		}
		for copied, object := range objects {
			var head struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal([]byte(object), &head); err != nil {
				t.Fatalf("%s, %s: %v", name, copied, err)
			}
			written[head.Schema]++
			holds := slices.ContainsFunc(newer, func(key string) bool { return strings.Contains(object, key) })
			newest := head.Schema == schemaV011 || head.Schema == schemaV012 || head.Schema == schemaV013 || head.Schema == schemaV014
			if (head.Schema == schemaV011) != (holdsP95(input) && !holdsPack(input) && !holdsReceipt(input)) || (!newest && holds) {
				t.Errorf("%s, %s: written as %s, holding a run_p95 result %t, holding a key of 0.11 or 0.12 %t", name, copied, head.Schema, holdsP95(input), holds)
			}
			if err := resultSchemaErrors(compileSchemaFile(t, compiled[head.Schema]), object); err != nil {
				t.Errorf("%s, %s breaks %s: %v", name, copied, head.Schema, err)
			}
		}
	}
	if written[schemaV09] == 0 || written[schema] == 0 || written[schemaV011] == 0 {
		t.Errorf("the golden states write %v, want every one of the three versions", written)
	}
}

// TestP95ResultIsTheOnlyChangeInAV011Object takes the run_p95 result out of
// each P95 golden state's objects and sets the schema back: each is then the
// object the same report writes without the rule, byte for byte, beside an
// estimate too. No figure, cap, total or other result moves, and the action's
// outputs other than result do not either.
func TestP95ResultIsTheOnlyChangeInAV011Object(t *testing.T) {
	states := p95States(t)
	states["p95 beside an estimate"] = withEstimate(t, states["p95-over"], twoCurrencies)
	for name, input := range states {
		plain := copies(t, withoutP95(input))
		for copied, object := range copies(t, input) {
			if err := resultSchemaErrors(compileSchemaFile(t, resultV011SchemaPath), object); err != nil {
				t.Errorf("%s, %s breaks result/v0.11: %v\n%s", name, copied, err, object)
			}
			block, err := ParseResult(fenced(object))
			if err != nil {
				t.Fatalf("%s, %s: %v", name, copied, err)
			}
			block.Schema = schemaV09
			if len(block.Estimates) > 0 {
				block.Schema = schema
			}
			block.Policy.Results = slices.DeleteFunc(block.Policy.Results, func(result policy.Result) bool { return result.Rule == policy.RuleRunP95 })
			apart, err := block.encode()
			if err != nil {
				t.Fatal(err)
			}
			if apart != plain[copied] {
				t.Errorf("%s, %s: without its run_p95 result the object is\n%s\nand the report without the rule writes\n%s", name, copied, apart, plain[copied])
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
		if got, want := outputs(input), outputs(withoutP95(input)); !maps.Equal(got, want) {
			t.Errorf("%s: the rule moved the outputs:\n%v\nwant\n%v", name, got, want)
		}
	}
}

// TestV011ObjectsReadAndWriteAgain reads each P95 golden state's object as
// costroid approve and costroid result do, writes it again byte for byte, and
// repeats it with another conclusion as result/v0.11. It refuses a result/v0.11
// object that holds nothing only that version defines, and an older object
// that holds a run_p95 result or one of its keys.
func TestV011ObjectsReadAndWriteAgain(t *testing.T) {
	states := p95States(t)
	states["p95 beside an estimate"] = withEstimate(t, states["p95-over"], twoCurrencies)
	for name, input := range states {
		object, err := Result(input)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ParseResult(fenced(object))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if again, err := block.encode(); err != nil || again != object || block.Schema != schemaV011 {
			t.Errorf("%s: read as %s and written again as\n%s\nwant\n%s (%v)", name, block.Schema, again, object, err)
		}
		output, err := Reconclude(block, nil, AmountsShown, exampleCheckRun)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.Text, `"schema": "`+schemaV011+`"`) || !strings.Contains(output.Text, `"rule": "run_p95"`) {
			t.Errorf("%s: the repeated check lost its run_p95 result:\n%s", name, output.Text)
		}
	}
	over, err := Result(states["p95-over"])
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Result(withoutP95(states["p95-over"]))
	if err != nil {
		t.Fatal(err)
	}
	for name, refused := range map[string]struct{ object, says string }{
		"a 0.11 object that holds nothing new": {
			strings.Replace(plain, `"`+schemaV09+`"`, `"`+schemaV011+`"`, 1),
			"holds nothing only costroid.result/0.11 defines",
		},
		"a 0.9 object that holds run_p95": {
			strings.Replace(over, `"`+schemaV011+`"`, `"`+schemaV09+`"`, 1),
			"which only costroid.result/0.11 defines",
		},
		"a 0.10 object that holds p95_usd": {
			strings.Replace(strings.Replace(over, `"`+schemaV011+`"`, `"`+schema+`"`, 1), `"rule": "run_p95"`, `"rule": "run_increase"`, 1),
			"the field policy.results.p95_usd, which only costroid.result/0.11 defines",
		},
	} {
		if _, err := ParseResult(fenced(refused.object)); err == nil || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

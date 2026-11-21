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

// resultV012SchemaPath is result/v0.12 of Costroid/costroid-schemas, copied.
var resultV012SchemaPath = filepath.Join("testdata", "result-v0.12.schema.json")

// resultV012SchemaSHA256 is the SHA-256 of result/v0.12/result.schema.json as
// the schemas' branch for that version holds it, before it is published:
//
//	git show result-v0.12:result/v0.12/result.schema.json | sha256sum
const resultV012SchemaSHA256 = "96c65ca08395e7c86f33b628600c6bf168639d76f574195493a1c33ef39734ae"

// resultKeysAddedIn012 are the keys of a policy result that result/v0.12
// added.
var resultKeysAddedIn012 = []string{"line"}

// jsonKeys are a struct's JSON keys, sorted.
func jsonKeys(of reflect.Type) []string {
	var keys []string
	for index := range of.NumField() {
		name, _, _ := strings.Cut(of.Field(index).Tag.Get("json"), ",")
		if name != "-" {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	return keys
}

// TestResultV012SchemaCopyMatchesItsDigest holds the copied result/v0.12
// schema to the file that adds it byte for byte, Block's keys to its
// properties, a policy result's keys to the properties it states, and the
// pack's keys and a pack result's keys to theirs, each exactly.
func TestResultV012SchemaCopyMatchesItsDigest(t *testing.T) {
	data, err := os.ReadFile(resultV012SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != resultV012SchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and result/v0.12 is %s", got, resultV012SchemaSHA256)
	}
	type properties struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	var document struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       struct {
			Policy struct {
				Properties struct {
					Results struct {
						Items properties `json:"items"`
					} `json:"results"`
				} `json:"properties"`
			} `json:"policy"`
			Pack       properties `json:"pack"`
			PackResult properties `json:"packResult"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(document.ID, "/result/v0.12/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.12", document.ID)
	}
	if fromSchema, fromBlock := slices.Sorted(maps.Keys(document.Properties)), blockKeys(); !slices.Equal(fromSchema, fromBlock) {
		t.Errorf("Block's keys %v, the schema's %v", fromBlock, fromSchema)
	}
	if !strings.Contains(string(document.Properties["schema"]), `"`+schemaV012+`"`) {
		t.Errorf("the schema names %s, Block emits %q", document.Properties["schema"], schemaV012)
	}
	for name, pair := range map[string]struct {
		code   reflect.Type
		schema map[string]json.RawMessage
	}{
		"a policy result": {reflect.TypeFor[policy.Result](), document.Defs.Policy.Properties.Results.Items.Properties},
		"the pack":        {reflect.TypeFor[policy.PackRun](), document.Defs.Pack.Properties},
		"a pack result":   {reflect.TypeFor[policy.PackResult](), document.Defs.PackResult.Properties},
	} {
		if fromCode, fromSchema := jsonKeys(pair.code), slices.Sorted(maps.Keys(pair.schema)); !slices.Equal(fromCode, fromSchema) {
			t.Errorf("%s: the code writes %v, result/v0.12 states %v", name, fromCode, fromSchema)
		}
	}
}

// holdsPack reports an input whose verdict holds a customer pack, the one
// value of result/v0.12 this release writes.
func holdsPack(input Input) bool {
	return input.Verdict != nil && input.Verdict.Pack != nil
}

// withoutPack is the input with its verdict's pack and customer_pack result
// taken out and every other field of the verdict kept, the conclusion
// included.
func withoutPack(input Input) Input {
	verdict := *input.Verdict
	verdict.Pack = nil
	verdict.Results = slices.DeleteFunc(slices.Clone(verdict.Results), func(result policy.Result) bool {
		return result.Rule == policy.RuleCustomerPack
	})
	input.Verdict = &verdict
	return input
}

// TestAnObjectWithAPackIsWrittenAsV012 holds every golden state's objects, as
// the comment, the check run and the result output carry them and as a
// withheld copy and a run before its check write them, to one rule: an object
// is result/v0.12 exactly when its verdict holds a pack, and then it holds to
// that version's copied schema, beside a run_p95 result too. With the pack
// and its customer_pack result taken out and the schema set back, each object
// is the one the same report writes without the pack, byte for byte, as
// result/v0.11 beside a run_p95 result and as result/v0.9 otherwise. A
// withheld copy holds no pack message.
func TestAnObjectWithAPackIsWrittenAsV012(t *testing.T) {
	schema := compileSchemaFile(t, resultV012SchemaPath)
	held := 0
	for name, input := range goldenStates(t) {
		objects := copies(t, input)
		for file, object := range machineBlocks(t, input) {
			objects[file] = strings.TrimSuffix(strings.TrimPrefix(object, "```json\n"), "\n```")
		}
		for copied, object := range objects {
			var block Block
			if err := json.Unmarshal([]byte(object), &block); err != nil {
				t.Fatalf("%s, %s: %v", name, copied, err)
			}
			if (block.Schema == schemaV012) != holdsPack(input) {
				t.Errorf("%s, %s: written as %s, holding a pack %t", name, copied, block.Schema, holdsPack(input))
			}
			if block.Schema != schemaV012 {
				continue
			}
			held++
			if err := resultSchemaErrors(schema, object); err != nil {
				t.Errorf("%s, %s breaks result/v0.12: %v\n%s", name, copied, err, object)
			}
			if block.Amounts != amountsShown && strings.Contains(object, `"message"`) {
				t.Errorf("%s, %s: a copy that withholds amounts holds a pack's message", name, copied)
			}
		}
		if !holdsPack(input) {
			continue
		}
		plain := copies(t, withoutPack(input))
		for copied, object := range copies(t, input) {
			block, err := ParseResult(fenced(object))
			if err != nil {
				t.Fatal(err)
			}
			block.Schema = schemaV09
			if holdsP95(input) {
				block.Schema = schemaV011
			}
			block.Policy.Pack = nil
			block.Policy.Results = slices.DeleteFunc(block.Policy.Results, func(result policy.Result) bool { return result.Rule == policy.RuleCustomerPack })
			apart, err := block.encode()
			if err != nil {
				t.Fatal(err)
			}
			if apart != plain[copied] {
				t.Errorf("%s, %s: without its pack the object is\n%s\nand the report without the pack writes\n%s", name, copied, apart, plain[copied])
			}
		}
	}
	if held == 0 {
		t.Fatal("no golden state writes result/v0.12")
	}
}

// TestV012ObjectsReadAndWriteAgain reads each pack state's object as
// costroid approve and costroid result do, writes it again byte for byte, and
// repeats it with another conclusion as result/v0.12. It refuses a
// result/v0.12 object that holds no pack, and an older object that holds a
// pack, a customer_pack result or a line.
func TestV012ObjectsReadAndWriteAgain(t *testing.T) {
	states := packStates(t)
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
		if again, err := block.encode(); err != nil || again != object || block.Schema != schemaV012 {
			t.Errorf("%s: read as %s and written again as\n%s\nwant\n%s (%v)", name, block.Schema, again, object, err)
		}
		output, err := Reconclude(block, nil, AmountsShown, exampleCheckRun)
		if err != nil {
			t.Fatal(err)
		}
		shown := input
		shown.Amounts = AmountsShown
		if !strings.Contains(output.Text, `"schema": "`+schemaV012+`"`) || !strings.Contains(output.Text, `"pack": {`) || output.Title != VerdictSentence(shown) {
			t.Errorf("%s: the repeated check lost its pack, or its title %q:\n%s", name, output.Title, output.Text)
		}
	}
	invalid, err := Result(states["pack-not-valid"])
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Result(withoutPack(states["pack-rule-fails"]))
	if err != nil {
		t.Fatal(err)
	}
	for name, refused := range map[string]struct{ object, says string }{
		"a 0.12 object that holds no pack": {
			strings.Replace(plain, `"`+schemaV09+`"`, `"`+schemaV012+`"`, 1),
			"holds nothing only costroid.result/0.12 defines",
		},
		"a 0.11 object that holds a pack": {
			strings.Replace(invalid, `"`+schemaV012+`"`, `"`+schemaV011+`"`, 1),
			"which only costroid.result/0.12 defines",
		},
		"a 0.9 object that holds a line": {
			strings.Replace(strings.Replace(invalid, `"`+schemaV012+`"`, `"`+schemaV09+`"`, 1), `"rule": "customer_pack"`, `"rule": "run_budget"`, 1),
			"which only costroid.result/0.12 defines",
		},
	} {
		if _, err := ParseResult(fenced(refused.object)); err == nil || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

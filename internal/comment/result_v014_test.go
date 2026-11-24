package comment

import (
	"context"
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
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
)

// resultV014SchemaPath is result/v0.14 of Costroid/costroid-schemas, copied.
var resultV014SchemaPath = filepath.Join("testdata", "result-v0.14.schema.json")

// resultV014SchemaSHA256 is the SHA-256 of result/v0.14/result.schema.json as
// the schemas' branch for that version holds it, before it is published:
//
//	git show result-v0.14:result/v0.14/result.schema.json | sha256sum
const resultV014SchemaSHA256 = "090c2a6c46c95a28a6f3174aa3b4684b2f7aa8b07b3f5525c14874be0a5dbdd8"

// TestResultV014SchemaCopyMatchesItsDigest holds the copied result/v0.14
// schema to the file that adds it byte for byte, Block's keys to its
// properties, a policy result's keys to the properties it states, and a
// demotion's keys to its own.
func TestResultV014SchemaCopyMatchesItsDigest(t *testing.T) {
	data, err := os.ReadFile(resultV014SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != resultV014SchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and result/v0.14 is %s", got, resultV014SchemaSHA256)
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
			Demoted properties `json:"demoted"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(document.ID, "/result/v0.14/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.14", document.ID)
	}
	if fromSchema, fromBlock := slices.Sorted(maps.Keys(document.Properties)), allBlockKeys(); !slices.Equal(fromSchema, fromBlock) {
		t.Errorf("Block's keys %v, the schema's %v", fromBlock, fromSchema)
	}
	if !strings.Contains(string(document.Properties["schema"]), `"`+schemaV014+`"`) {
		t.Errorf("the schema names %s, Block emits %q", document.Properties["schema"], schemaV014)
	}
	for name, pair := range map[string]struct {
		code   reflect.Type
		schema map[string]json.RawMessage
	}{
		"a policy result": {reflect.TypeFor[policy.Result](), document.Defs.Policy.Properties.Results.Items.Properties},
		"a demotion":      {reflect.TypeFor[policy.Demoted](), document.Defs.Demoted.Properties},
	} {
		if fromCode, fromSchema := jsonKeys(pair.code), slices.Sorted(maps.Keys(pair.schema)); !slices.Equal(fromCode, fromSchema) {
			t.Errorf("%s: the code writes %v, result/v0.14 states %v", name, fromCode, fromSchema)
		}
	}
}

// ledgerRecord is a ledger's override record that has kept no demotion yet,
// whose every window holds failing pull requests, approved of them by
// approvers accounts.
type ledgerRecord struct {
	failing, approved, approvers int
}

func (ledgerRecord) AddFailure(context.Context, string, decimal.Decimal, time.Time) error {
	return nil
}

func (r ledgerRecord) Overrides(context.Context, string, decimal.Decimal, time.Time, time.Time) (int, int, int, error) {
	return r.failing, r.approved, r.approvers, nil
}

func (ledgerRecord) Demotion(context.Context, string) (policy.Demotion, bool, error) {
	return policy.Demotion{}, false, nil
}

func (ledgerRecord) KeepDemotion(_ context.Context, _ string, demotion policy.Demotion) (policy.Demotion, error) {
	return demotion, nil
}

// ledgerVerdict is a ledger's verdict at afterPeriod on a pull request whose
// rows cost what the golden run cost, under a costroid.yml read at the base
// commit, beside a record of 7 of the latest 20 failing pull requests
// approved by 3 accounts.
func ledgerVerdict(t *testing.T, config string) *policy.Verdict {
	t.Helper()
	verdict, err := policy.EvaluateLedger(context.Background(), policy.LedgerRequest{
		Lines:  []policy.LedgerLine{{Currency: "USD", ListCost: decimal.MustParse(realTotal), Rows: 19, Model: "claude-haiku-5-5"}},
		Config: policy.ConfigFile{Data: []byte(config), Found: true, Ref: exampleBase, FromBase: true},
		Start:  policy.Start{At: goldenStart},
		At:     afterPeriod,
		Record: ledgerRecord{failing: 20, approved: 7, approvers: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &verdict
}

// demotedRunBudget is fail-shown's verdict with its run budget demoted by 7
// of the latest 20 failing pull requests, as result/v0.14's own example
// states it. A ledger's verdict covers a pull request and decides no run
// rule, so this verdict is built here: it is the form the result object and
// the verdict sentence take for a demoted rule that holds one run's cost.
func demotedRunBudget(t *testing.T) *policy.Verdict {
	t.Helper()
	verdict := *goldenVerdict(t, overBudget, policy.Unverified, afterPeriod)
	verdict.Results = slices.Clone(verdict.Results)
	for index := range verdict.Results {
		result := &verdict.Results[index]
		if result.Rule == policy.RuleRunBudget && result.Outcome == policy.Fail {
			result.Outcome = policy.Warn
			result.Demoted = &policy.Demoted{Reason: policy.DemotedByOverrideRate, Approved: 7, Failing: 20, Until: afterPeriod.Add(policy.DemotionTerm).Format(time.RFC3339)}
			verdict.Conclusion = policy.Neutral
		}
	}
	if verdict.Conclusion != policy.Neutral {
		t.Fatalf("the run budget did not fail: %+v", verdict)
	}
	return &verdict
}

// demotedStates are the states of a rule a ledger demoted, each shown and
// withheld: the run budget, the pull request's budget on a ledger's verdict,
// and that budget beside a model off the list, which still fails.
func demotedStates(t *testing.T) map[string]Input {
	t.Helper()
	budget := "policy:\n  pr_budget_usd: \"0.001\"\n  enforce: true\n"
	shown := map[string]Input{
		"demoted-run-budget":            withVerdict(t, demotedRunBudget(t)),
		"demoted-request-budget":        withVerdict(t, ledgerVerdict(t, budget)),
		"demoted-beside-a-failing-rule": withVerdict(t, ledgerVerdict(t, budget+"  models: [\"gpt-*\"]\n")),
	}
	states := map[string]Input{}
	for name, input := range shown {
		states[name], states[name+"-withheld"] = input, withheld(input)
	}
	return states
}

// holdsDemotion reports an input whose verdict holds a demoted result, the
// one value of result/v0.14 this release writes.
func holdsDemotion(input Input) bool {
	return input.Verdict != nil && slices.ContainsFunc(input.Verdict.Results, func(result policy.Result) bool { return result.Demoted != nil })
}

// TestAnObjectWithADemotedResultIsWrittenAsV014 holds every golden state's
// objects, as the comment, the check run and the result output carry them and
// as a withheld copy and a run before its check write them, to one rule: an
// object is result/v0.14 exactly when its verdict holds a demoted result, and
// then it holds to that version's copied schema, beside a receipt and an
// estimate too, with its until after the verdict's evaluated_at. A withheld
// copy keeps the demotion. Every other object is the version it was.
func TestAnObjectWithADemotedResultIsWrittenAsV014(t *testing.T) {
	schema := compileSchemaFile(t, resultV014SchemaPath)
	states := goldenStates(t)
	states["demoted beside a receipt"] = withReceipt(states["demoted-request-budget"], readReceipt())
	states["demoted beside an estimate"] = withEstimate(t, states["demoted-run-budget"], twoCurrencies)
	held := 0
	for name, input := range states {
		objects := copies(t, input)
		for file, object := range machineBlocks(t, input) {
			objects[file] = strings.TrimSuffix(strings.TrimPrefix(object, "```json\n"), "\n```")
		}
		for copied, object := range objects {
			var block Block
			if err := json.Unmarshal([]byte(object), &block); err != nil {
				t.Fatalf("%s, %s: %v", name, copied, err)
			}
			if (block.Schema == schemaV014) != holdsDemotion(input) || strings.Contains(object, `"demoted"`) != holdsDemotion(input) {
				t.Errorf("%s, %s: written as %s, holding a demoted result %t", name, copied, block.Schema, holdsDemotion(input))
			}
			if block.Schema != schemaV014 {
				continue
			}
			held++
			if err := resultSchemaErrors(schema, object); err != nil {
				t.Errorf("%s, %s breaks result/v0.14: %v\n%s", name, copied, err, object)
			}
			evaluated, err := time.Parse(time.RFC3339, block.Policy.EvaluatedAt)
			if err != nil {
				t.Fatal(err)
			}
			for _, result := range block.Policy.Results {
				if result.Demoted == nil {
					continue
				}
				if until, err := time.Parse(time.RFC3339, result.Demoted.Until); err != nil || !until.After(evaluated) {
					t.Errorf("%s, %s: the demotion ends %s, not after the verdict at %s", name, copied, result.Demoted.Until, block.Policy.EvaluatedAt)
				}
			}
		}
	}
	if held < 30 {
		t.Fatalf("%d objects written as result/v0.14", held)
	}
}

// TestV014ObjectsReadAndWriteAgain reads each demoted state's object as
// costroid approve and costroid result do, writes it again byte for byte, and
// repeats it under an approval with the demotion kept and the title the
// report's own. An object of an earlier version that holds a demotion, and a
// result/0.14 object that holds none, are refused.
func TestV014ObjectsReadAndWriteAgain(t *testing.T) {
	states := demotedStates(t)
	for name, input := range states {
		object, err := Result(input)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ParseResult(fenced(object))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if again, err := block.encode(); err != nil || again != object || block.Schema != schemaV014 {
			t.Errorf("%s: read as %s and written again as\n%s\nwant\n%s (%v)", name, block.Schema, again, object, err)
		}
		output, err := Reconclude(block, nil, input.Amounts, exampleCheckRun)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.Text, `"demoted": {`) || output.Title != VerdictSentence(input) {
			t.Errorf("%s: the repeated check lost its demotion, or its title %q", name, output.Title)
		}
	}
	demoted, err := Result(states["demoted-run-budget"])
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Result(goldenStates(t)["neutral-shown"])
	if err != nil {
		t.Fatal(err)
	}
	for name, refused := range map[string]struct{ object, says string }{
		"a 0.13 object that holds a demotion": {strings.Replace(demoted, `"`+schemaV014+`"`, `"`+schemaV013+`"`, 1), "which only costroid.result/0.14 defines"},
		"a 0.9 object that holds a demotion":  {strings.Replace(demoted, `"`+schemaV014+`"`, `"`+schemaV09+`"`, 1), "which only costroid.result/0.14 defines"},
		"a 0.14 object that holds none":       {strings.Replace(plain, `"`+schemaV09+`"`, `"`+schemaV014+`"`, 1), "holds nothing only costroid.result/0.14 defines"},
	} {
		if _, err := ParseResult(fenced(refused.object)); err == nil || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestTheDemotionIsStatedUnderTheTitle reads, on each surface, the line that
// states the record behind a demotion: the rule, the counts, the accounts a
// demotion needs and when it ends, and how to keep every rule as written. It
// holds no amount, so a withheld copy states it too.
func TestTheDemotionIsStatedUnderTheTitle(t *testing.T) {
	want := map[string]string{
		"demoted-run-budget":            "Run budget demoted to a warning: by the ledger's own record, reviewers from at least 2 accounts approved 7 of the last 20 pull requests it failed, more than one in five. It warns until 2026-12-12 09:00 UTC, then fails again. Add `auto_demote: false` under `policy` in `costroid.yml` to keep every rule as written.",
		"demoted-beside-a-failing-rule": "Pull request budget demoted to a warning: by the ledger's own record, reviewers from at least 2 accounts approved 7 of the last 20 pull requests it failed, more than one in five. It warns until 2026-12-12 09:00 UTC, then fails again. Add `auto_demote: false` under `policy` in `costroid.yml` to keep every rule as written.",
	}
	states := demotedStates(t)
	for name, line := range want {
		for _, input := range []Input{states[name], states[name+"-withheld"]} {
			r := renderState(t, input)
			for surface, text := range map[string]string{"comment": r.comment, "summary": r.summary, "check": r.check} {
				if !strings.Contains(text, "\n"+line+"\n") {
					t.Errorf("%s, %s lacks the line %q", name, surface, line)
				}
			}
		}
	}
}

package comment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reason"
)

// resultSchemaPath is result/v0.9 of Costroid/costroid-schemas, copied.
var resultSchemaPath = filepath.Join("testdata", "result.schema.json")

// The digests of testdata/rows.jsonl, computed outside Costroid:
//
//	jq -r .x_CostroidRowId rows.jsonl | LC_ALL=C sort | sha256sum
//	sha256sum rows.jsonl
const (
	realRowsSHA256     = "ffbdfb76589dfdfd44187050fde97f7a44684d0cb9bb588394dc005ff65bc8f4"
	realRowsFileSHA256 = "d715038ef1bd96e53caca4eb1a73496aa81a28ef2ddb5f2ab3effc644e18aa3c"
)

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
	refused := Input{State: NoRows, Reason: EmptyRowsFile, RunReason: reason.ReaderRefused, ReaderLine: "the result object could not be read", BeforeCheck: true, Amounts: AmountsShown}
	add("reader refused", refused, true)
	add("no rows file", Input{State: Unreadable, Reason: NoRowsFile, Amounts: AmountsShown}, true)
	add("fork with no rows", Input{State: Unreadable, Reason: ForkNoRows, Amounts: AmountsShown}, true)
	add("invalid row", Input{State: Unreadable, Reason: InvalidRow, ReasonLine: 3, Amounts: AmountsShown}, true)
	return blocks
}

// statedInput is exampleInput with the rows' digests and a pull request's
// total of three reports, one of which states no digest.
func statedInput(t *testing.T) Input {
	t.Helper()
	input := exampleInput(t)
	input.RowsSHA256 = realRowsSHA256
	input.RowsFileSHA256 = realRowsFileSHA256
	input.Lifetime = &Lifetime{
		Figures: []LifetimeFigure{
			{Currency: "EUR", Basis: "measured", Cost: decimal.MustParse("0.25"), Reports: 1, Undigested: true},
			{Currency: "USD", Basis: "list_equivalent", Cost: decimal.MustParse("0.003619725"), Reports: 2, Undigested: true},
		},
		Capped: true,
		Unread: 1,
	}
	input.AgentStated = &adapter.Stated{Unit: adapter.UnitAICredits, Quantity: "4.830000000000001", Source: "gh-aw"}
	return input
}

func TestResultV09MatchesSchema(t *testing.T) {
	schema := compileResultSchema(t)
	blocks := emittedBlocks(t)
	stated := statedInput(t)
	check, err := Result(stated)
	if err != nil {
		t.Fatal(err)
	}
	blocks["stated check run"] = check
	shown, err := stated.block()
	if err != nil {
		t.Fatal(err)
	}
	blocks["stated comment copy"] = shown
	stated.Amounts = AmountsOff
	withheld, err := stated.block()
	if err != nil {
		t.Fatal(err)
	}
	blocks["stated withheld comment copy"] = withheld
	for name, input := range map[string]Input{"OpenCode's USD": openCodeInput(t), "Copilot CLI's nano-AI units": copilotInput(t)} {
		block, err := Result(input)
		if err != nil {
			t.Fatal(err)
		}
		blocks[name] = block
	}
	empty, err := Result(Input{State: NoRows, Reason: EmptyRowsFile, RowsSHA256: realRowsSHA256, RowsFileSHA256: realRowsFileSHA256})
	if err != nil {
		t.Fatal(err)
	}
	blocks["no rows beside a stated rows digest"] = empty
	for name, block := range blocks {
		var head struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal([]byte(block), &head); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if head.Schema != "costroid.result/0.9" {
			t.Errorf("%s: schema = %q, want costroid.result/0.9", name, head.Schema)
		}
		if err := resultSchemaErrors(schema, block); err != nil {
			t.Errorf("%s breaks result/v0.9: %v\n%s", name, err, block)
		}
	}
}

func TestResultV09NewFieldsAreNullUntilStated(t *testing.T) {
	for name, block := range emittedBlocks(t) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(block), &fields); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, key := range []string{"agent_error", "agent_skipped", "ci_run", "rows_sha256", "rows_file_sha256", "lifetime", "agent_stated", "recorded"} {
			value, present := fields[key]
			if !present {
				t.Errorf("%s: no %s key", name, key)
			} else if string(value) != "null" {
				t.Errorf("%s: %s = %s, want null", name, key, value)
			}
		}
	}
}

// TestResultV07StatesTheLifetimeAsTheCommentDoes holds the block's lifetime to
// the comment's lines: the same amounts, and may_repeat exactly where a line
// says the total may include a repeated report.
func TestResultV07StatesTheLifetimeAsTheCommentDoes(t *testing.T) {
	input := statedInput(t)
	encoded, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	var block Block
	if err := json.Unmarshal([]byte(encoded), &block); err != nil {
		t.Fatal(err)
	}
	if block.RowsSHA256 == nil || *block.RowsSHA256 != realRowsSHA256 || block.RowsFileSHA256 == nil || *block.RowsFileSHA256 != realRowsFileSHA256 {
		t.Errorf("digests %v, %v", block.RowsSHA256, block.RowsFileSHA256)
	}
	want := BlockLifetime{
		Figures: []BlockLifetimeFigure{
			{Currency: "EUR", CostBasis: "measured", ListCost: "0.25", Reports: 1, MayRepeat: false},
			{Currency: "USD", CostBasis: "list_equivalent", ListCost: "0.003619725", Reports: 2, MayRepeat: true},
		},
		Capped: true,
		Unread: 1,
	}
	if block.Lifetime == nil || !reflect.DeepEqual(*block.Lifetime, want) {
		t.Errorf("lifetime %+v, want %+v", block.Lifetime, want)
	}
	lines := input.Lifetime.sentences()
	for index, figure := range want.Figures {
		if says := strings.Contains(lines[index], "may include a repeated report"); says != figure.MayRepeat {
			t.Errorf("line %q, may_repeat %t", lines[index], figure.MayRepeat)
		}
	}
	input.Amounts = AmountsOff
	withheld, err := input.block()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withheld, `"lifetime": null`) || strings.Contains(withheld, "0.003619725") {
		t.Errorf("a withheld copy states the lifetime:\n%s", withheld)
	}
}

func TestResultV09FilledFieldsMatchTheSchema(t *testing.T) {
	schema := compileResultSchema(t)
	encoded, err := Result(statedInput(t))
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
	block.Recorded = &BlockRecorded{RunID: 42368537950, Date: "2026-11-07"}
	if block.AgentStated == nil || *block.AgentStated != (BlockAgentStated{Unit: "AIC", Quantity: "4.830000000000001", Source: "gh-aw"}) {
		t.Errorf("agent_stated %+v, want gh-aw's credits with every digit", block.AgentStated)
	}
	filled, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	if err := resultSchemaErrors(schema, string(filled)); err != nil {
		t.Errorf("a filled object breaks result/v0.9: %v\n%s", err, filled)
	}
	upper := strings.ToUpper(realRowsSHA256)
	for name, change := range map[string]func(Block) Block{
		"a class outside the closed list": func(b Block) Block {
			b.AgentError = &BlockAgentError{Class: "the agent's own words"}
			return b
		},
		"a digest in upper case": func(b Block) Block {
			b.RowsSHA256 = &upper
			return b
		},
		"a rows digest without figures": func(b Block) Block {
			b.State, b.Reason, b.Rows, b.Figures, b.Cap, b.Policy = NoRows, EmptyRowsFile, 0, []BlockFigure{}, nil, nil
			return b
		},
		"a lifetime in a withheld copy": func(b Block) Block {
			b.Amounts, b.Cap, b.Figures, b.AgentStated = "withheld", nil, []BlockFigure{}, nil
			return b
		},
		"credits in a withheld copy": func(b Block) Block {
			b.Amounts, b.Cap, b.Figures, b.Lifetime = "withheld", nil, []BlockFigure{}, nil
			return b
		},
		"credits stated in USD": func(b Block) Block {
			b.AgentStated = &BlockAgentStated{Unit: "USD", Quantity: "0.0483", Source: "gh-aw"}
			return b
		},
		"a recorded run numbered 0": func(b Block) Block {
			b.Recorded = &BlockRecorded{RunID: 0, Date: "2026-11-07"}
			return b
		},
		"a recorded date with a time": func(b Block) Block {
			b.Recorded = &BlockRecorded{RunID: 42368537950, Date: "2026-11-07T11:33:17Z"}
			return b
		},
		"one report that may repeat": func(b Block) Block {
			lifetime := *b.Lifetime
			lifetime.Figures = []BlockLifetimeFigure{{Currency: "USD", CostBasis: "measured", ListCost: "1", Reports: 1, MayRepeat: true}}
			b.Lifetime = &lifetime
			return b
		},
	} {
		refused, err := json.Marshal(change(block))
		if err != nil {
			t.Fatal(err)
		}
		if resultSchemaErrors(schema, string(refused)) == nil {
			t.Errorf("%s passed result/v0.9:\n%s", name, refused)
		}
	}
}

// TestNewPolicyRulesValidateAgainstV09 runs every rule of the default pack,
// with a baseline and a pull request's total, and holds the check run's
// object and both comment copies to result/v0.9. It then holds the schema's
// closed lists to every rule and reason the policy names.
func TestNewPolicyRulesValidateAgainstV09(t *testing.T) {
	schema := compileResultSchema(t)
	file, err := os.Open(filepath.Join("testdata", "rows.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	usage, err := policy.ReadUsage(file)
	if err != nil {
		t.Fatal(err)
	}
	config := "policy:\n  run_budget_usd: \"0.001\"\n  max_run_cost_usd: 5\n  max_increase_usd: \"0.0001\"\n  max_increase_percent: 10\n" +
		"  pr_budget_usd: \"0.002\"\n  monthly_budget_usd: 900\n  models: [\"claude-opus-*\"]\n"
	verdict, err := policy.Evaluate(context.Background(), policy.Request{
		Usage:    usage,
		Trust:    policy.Unverified,
		Config:   policy.ConfigFile{Data: []byte(config), Found: true, Ref: exampleBase, FromBase: true},
		Start:    policy.Start{At: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
		At:       time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
		Baseline: &policy.Baseline{Runs: 12, MedianUSD: decimal.MustParse("0.001"), Trust: policy.Unverified},
		Lifetime: &policy.Lifetime{USD: decimal.MustParse("0.002"), Trust: policy.Unverified},
	})
	if err != nil {
		t.Fatal(err)
	}
	rules := []string{
		policy.RuleRunBudget, policy.RuleRunCostThreshold, policy.RuleRunIncrease, policy.RuleRunIncreasePercent,
		policy.RulePullRequestBudget, policy.RuleMonthlyBudget, policy.RuleModelAllowlist, policy.RuleAttribution,
	}
	var ran []string
	for _, result := range verdict.Results {
		ran = append(ran, result.Rule)
	}
	for _, rule := range rules[:len(rules)-1] {
		if !slices.Contains(ran, rule) {
			t.Errorf("the verdict has no %s result: %+v", rule, verdict.Results)
		}
	}
	input := statedInput(t)
	input.Verdict = &verdict
	check, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := input.block()
	if err != nil {
		t.Fatal(err)
	}
	withheldInput := input
	withheldInput.Amounts = AmountsOff
	withheld, err := withheldInput.block()
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]string{"check run": check, "comment copy": shown, "withheld comment copy": withheld} {
		if err := resultSchemaErrors(schema, block); err != nil {
			t.Errorf("%s breaks result/v0.9: %v\n%s", name, err, block)
		}
	}
	reasons := []string{
		policy.ReasonOverLimit, policy.ReasonWithinLimit, policy.ReasonNoRows, policy.ReasonUnverifiedRows,
		policy.ReasonUnattributedRows, policy.ReasonOtherCurrency, policy.ReasonJoinableRows,
		policy.ReasonOverBaseline, policy.ReasonNoBaseline, policy.ReasonNoLifetime, policy.ReasonRepeatPossible,
		policy.ReasonNoLedger, policy.ReasonOutsideAllowlist, policy.ReasonUnreadableModel, policy.ReasonOnAllowlist,
	}
	for _, rule := range rules {
		for _, reason := range reasons {
			one := verdict
			one.Results = []policy.Result{{Rule: rule, Outcome: policy.Unproven, Reason: reason, LimitPercent: "10", BaselineUSD: "0.001", Models: []string{"gpt-5.6-sol"}}}
			input.Verdict = &one
			block, err := Result(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := resultSchemaErrors(schema, block); err != nil {
				t.Errorf("rule %s with reason %s breaks result/v0.9: %v", rule, reason, err)
			}
		}
	}
}

// TestResultV09BlockFieldsMatchTheSchema holds Block to the copied schema in
// both directions, so neither gains a field the other lacks.
func TestResultV09BlockFieldsMatchTheSchema(t *testing.T) {
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
	if !strings.HasSuffix(document.ID, "/result/v0.9/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.9", document.ID)
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

// resultSchemaSHA256 is the SHA-256 of result/v0.9/result.schema.json in
// Costroid/costroid-schemas at b42529b, the commit that added result/v0.9:
//
//	git show b42529b:result/v0.9/result.schema.json | sha256sum
const resultSchemaSHA256 = "a41214c4fcaa6a33c242e0848b7678fa70ec182d4779d8ff8d8befe1f9711dfa"

// TestResultSchemaCopyMatchesItsDigest holds the copied schema to the
// published file byte for byte, so the tests never validate against a copy
// edited here.
func TestResultSchemaCopyMatchesItsDigest(t *testing.T) {
	data, err := os.ReadFile(resultSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != resultSchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and result/v0.9 as published is %s", got, resultSchemaSHA256)
	}
}

// TestResultV07StatesTheAgentsFigureApart states gh-aw's credits in the check
// run's object and on the comment's own line, with every digit, and in no
// amount; a withheld copy states neither, and a unit or a source outside the
// closed lists is dropped.
func TestResultV07StatesTheAgentsFigureApart(t *testing.T) {
	input := statedInput(t)
	plain := statedInput(t)
	plain.AgentStated = nil
	with, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	without, err := Result(plain)
	if err != nil {
		t.Fatal(err)
	}
	stated := "  \"agent_stated\": {\n    \"unit\": \"AIC\",\n    \"quantity\": \"4.830000000000001\",\n    \"source\": \"gh-aw\"\n  }"
	if strings.Replace(with, stated, `  "agent_stated": null`, 1) != without {
		t.Errorf("the credits changed more than agent_stated:\n%s\n%s", with, without)
	}
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	line := "gh-aw stated 4.830000000000001 AI credits for this run. Costroid does not convert credits."
	if strings.Count(body, line) != 1 {
		t.Errorf("the comment does not state the credits once:\n%s", body)
	}
	plainBody, err := Comment(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(strings.Replace(body, line+"\n\n", "", 1), stated, `  "agent_stated": null`, 1) != plainBody {
		t.Errorf("the credits changed the comment beyond their line and the object")
	}
	input.Amounts = AmountsOff
	withheld, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withheld, "4.830000000000001") {
		t.Errorf("a withheld comment states the credits:\n%s", withheld)
	}
	for _, unknown := range []adapter.Stated{
		{Unit: "USD", Quantity: "1", Source: "gh-aw"},
		{Unit: "AIC", Quantity: "1", Source: "another-agent"},
		{Unit: "AIC", Quantity: "1e3", Source: "gh-aw"},
		{Unit: "AIC", Quantity: "1", Source: "opencode"},
		{Unit: "USD", Quantity: "1", Source: "copilot"},
		{Unit: "nano-AIU", Quantity: "1", Source: "gh-aw"},
		{Unit: "USD", Quantity: "-1", Source: "opencode"},
	} {
		plain.AgentStated = &unknown
		if dropped, err := Result(plain); err != nil || dropped != without {
			t.Errorf("%+v was stated: %v", unknown, err)
		}
	}
}

// TestParseResultReadsAV06ObjectAsItWasWritten reads an object a release that
// wrote result/0.6 posted, and writes it again byte for byte, with no
// agent_stated.
func TestParseResultReadsAV06ObjectAsItWasWritten(t *testing.T) {
	current, err := Result(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	older := strings.Replace(strings.Replace(current, ",\n  \"agent_stated\": null,\n  \"recorded\": null", "", 1), `"costroid.result/0.9"`, `"costroid.result/0.6"`, 1)
	if older == current || strings.Contains(older, "agent_stated") {
		t.Fatalf("no 0.6 object was made:\n%s", older)
	}
	block, err := ParseResult(fenced(older))
	if err != nil {
		t.Fatal(err)
	}
	again, err := block.encode()
	if err != nil || again != older {
		t.Errorf("written again as\n%s\nwant\n%s (%v)", again, older, err)
	}
	if _, err := ParseResult(fenced(strings.Replace(strings.Replace(current, ",\n  \"recorded\": null", "", 1), `"costroid.result/0.9"`, `"costroid.result/0.6"`, 1))); err == nil {
		t.Error("a 0.6 object holding agent_stated was read")
	}
}

// TestParseResultReadsAV07ObjectAsItWasWritten reads an object a release that
// wrote result/0.7 posted, and writes it again byte for byte, with no
// recorded. A 0.7 object that holds recorded was not written by such a
// release, so it is refused.
func TestParseResultReadsAV07ObjectAsItWasWritten(t *testing.T) {
	current, err := Result(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	older := strings.Replace(strings.Replace(current, ",\n  \"recorded\": null", "", 1), `"costroid.result/0.9"`, `"costroid.result/0.7"`, 1)
	if older == current || strings.Contains(older, "recorded") {
		t.Fatalf("no 0.7 object was made:\n%s", older)
	}
	block, err := ParseResult(fenced(older))
	if err != nil {
		t.Fatal(err)
	}
	again, err := block.encode()
	if err != nil || again != older {
		t.Errorf("written again as\n%s\nwant\n%s (%v)", again, older, err)
	}
	_, err = ParseResult(fenced(strings.Replace(current, `"costroid.result/0.9"`, `"costroid.result/0.7"`, 1)))
	if err == nil || !strings.Contains(err.Error(), "the field recorded, which only costroid.result/0.8 defines") {
		t.Errorf("a 0.7 object holding recorded was read: %v", err)
	}
}

// TestStatedFiguresNeverJoinTheCost states each agent's own figure beside
// the same report: gh-aw's AI credits, OpenCode's own price in USD and
// Copilot CLI's nano-AI units. Each changes the result object's agent_stated
// and adds one line to the check and the comment, and nothing else: the
// figures, the cap, the pull request's total and the verdict read the same
// with it and without it, OpenCode's USD included.
func TestStatedFiguresNeverJoinTheCost(t *testing.T) {
	plain := statedInput(t)
	plain.AgentStated = nil
	plain.Verdict = goldenVerdict(t, underBudget, policy.Unverified, insidePeriod)
	without := renderAll(t, plain)
	for _, stated := range []struct {
		figure adapter.Stated
		object string
		line   string
	}{
		{
			adapter.Stated{Unit: "AIC", Quantity: "7.47", Source: "gh-aw"},
			`{"unit":"AIC","quantity":"7.47","source":"gh-aw"}`,
			"gh-aw stated 7.47 AI credits for this run. Costroid does not convert credits.",
		},
		{
			adapter.Stated{Unit: "USD", Quantity: "0.00153317", Source: "opencode"},
			`{"unit":"USD","quantity":"0.00153317","source":"opencode"}`,
			"OpenCode stated USD 0.00153317 for this run by its own prices, which Costroid never adds to its own figures.",
		},
		{
			adapter.Stated{Unit: "nano-AIU", Quantity: "0", Source: "copilot"},
			`{"unit":"nano-AIU","quantity":"0","source":"copilot"}`,
			"Copilot CLI stated 0 nano-AI units for this run, which Costroid never converts to dollars.",
		},
	} {
		t.Run(stated.figure.Source, func(t *testing.T) {
			input := plain
			input.AgentStated = &stated.figure
			with := renderAll(t, input)
			if !strings.Contains(with.object, `"agent_stated":`+stated.object) {
				t.Fatalf("the object does not state %s:\n%s", stated.object, with.object)
			}
			if got := strings.Replace(with.object, `"agent_stated":`+stated.object, `"agent_stated":null`, 1); got != without.object {
				t.Errorf("the figure changed the object beyond agent_stated:\n%s\n%s", got, without.object)
			}
			for name, pair := range map[string][2]string{"comment": {with.comment, without.comment}, "check": {with.check, without.check}} {
				compact := strings.Replace(pair[0], indented(t, stated.object), "null", 1)
				if strings.Count(compact, stated.line) != 1 || strings.Replace(compact, stated.line+"\n\n", "", 1) != pair[1] {
					t.Errorf("the %s changed beyond its line and the object:\n%s", name, pair[0])
				}
			}
			if with.summary != without.summary {
				t.Errorf("the job summary changed:\n%s", with.summary)
			}
			var block Block
			if err := json.Unmarshal([]byte(with.object), &block); err != nil {
				t.Fatal(err)
			}
			if block.Cap == nil || block.Cap.Cost == nil || *block.Cap.Cost != "0.001619725" || block.Lifetime.Figures[1].ListCost != "0.003619725" || block.Policy.Results[0].CostUSD != "0.001619725" {
				t.Errorf("cap %+v, lifetime %+v, verdict %+v", block.Cap, block.Lifetime, block.Policy.Results)
			}
		})
	}
}

// rendering is what a report writes for one input, its object compacted.
type rendering struct {
	object, comment, check, summary string
}

func renderAll(t *testing.T, input Input) rendering {
	t.Helper()
	object, err := Result(input)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(object)); err != nil {
		t.Fatal(err)
	}
	body, err := Comment(input)
	if err != nil {
		t.Fatal(err)
	}
	check, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := Summary(input)
	if err != nil {
		t.Fatal(err)
	}
	return rendering{object: compact.String(), comment: body, check: check.Summary + check.Text, summary: summary}
}

// indented is a compact object as the result object lays it out under
// agent_stated.
func indented(t *testing.T, compact string) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(compact), "  ", "  "); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// TestParseResultReadsAV08ObjectAsItWasWritten reads an object a release that
// wrote result/0.8 posted, gh-aw's credits in it, and writes it again byte
// for byte. A 0.8 object that states OpenCode's or Copilot CLI's figure was
// not written by such a release, so it is refused.
func TestParseResultReadsAV08ObjectAsItWasWritten(t *testing.T) {
	current, err := Result(statedInput(t))
	if err != nil {
		t.Fatal(err)
	}
	older := strings.Replace(current, `"costroid.result/0.9"`, `"costroid.result/0.8"`, 1)
	if older == current {
		t.Fatalf("no 0.8 object was made:\n%s", older)
	}
	block, err := ParseResult(fenced(older))
	if err != nil {
		t.Fatal(err)
	}
	again, err := block.encode()
	if err != nil || again != older {
		t.Errorf("written again as\n%s\nwant\n%s (%v)", again, older, err)
	}
	for _, source := range []string{"opencode", "copilot"} {
		unit := map[string]string{"opencode": "USD", "copilot": "nano-AIU"}[source]
		stated := strings.Replace(strings.Replace(older, `"unit": "AIC"`, `"unit": "`+unit+`"`, 1), `"source": "gh-aw"`, `"source": "`+source+`"`, 1)
		_, err := ParseResult(fenced(stated))
		if err == nil || !strings.Contains(err.Error(), "the agent_stated source "+source+", which only costroid.result/0.9 defines") {
			t.Errorf("a 0.8 object stating %s's figure was read: %v", source, err)
		}
		if _, err := ParseResult(fenced(strings.Replace(stated, `"costroid.result/0.8"`, `"costroid.result/0.9"`, 1))); err != nil {
			t.Errorf("a 0.9 object stating %s's figure was refused: %v", source, err)
		}
	}
}

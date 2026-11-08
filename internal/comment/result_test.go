package comment

import (
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
)

// TestParseResultRefusesNewerValuesInAnOlderVersion refuses an object that
// states an earlier version and holds a value only a later one defines, and
// names the value. A 0.5 object laid out as 0.5 can still hold a rule, a
// reason or a run reason that 0.6 added, which no release of 0.5 wrote.
func TestParseResultRefusesNewerValuesInAnOlderVersion(t *testing.T) {
	current, err := Result(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	written, err := ParseResult(fenced(current))
	if err != nil {
		t.Fatal(err)
	}
	if written.Policy == nil || len(written.Policy.Results) == 0 {
		t.Fatal("the example holds no policy result to change")
	}
	as := func(t *testing.T, version string, change func(*Block)) string {
		t.Helper()
		block := written
		verdict := *written.Policy
		verdict.Results = slices.Clone(verdict.Results)
		block.Policy = &verdict
		block.Schema = version
		if change != nil {
			change(&block)
		}
		text, err := block.encode()
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	result := func(change func(*policy.Result)) func(*Block) {
		return func(block *Block) { change(&block.Policy.Results[0]) }
	}
	withField := func(version, field string) func(*testing.T) string {
		return func(t *testing.T) string {
			object := as(t, version, nil)
			line := "  \"schema\": \"" + version + "\",\n"
			if !strings.Contains(object, line) {
				t.Fatalf("no schema line in\n%s", object)
			}
			return strings.Replace(object, line, line+"  \""+field+"\": null,\n", 1)
		}
	}
	changed := func(version string, change func(*Block)) func(*testing.T) string {
		return func(t *testing.T) string { return as(t, version, change) }
	}

	for _, tc := range []struct {
		name   string
		object func(*testing.T) string
		named  string
		by     string
	}{
		{"a 0.6 rule", changed(schemaV05, result(func(r *policy.Result) { r.Rule = policy.RulePullRequestBudget })), "the rule pr_budget", schemaV06},
		{"a 0.6 reason", changed(schemaV05, result(func(r *policy.Result) { r.Reason = policy.ReasonOverBaseline })), "the reason over_baseline", schemaV06},
		{"a 0.6 limit_percent", changed(schemaV05, result(func(r *policy.Result) { r.LimitPercent = "20" })), "the field policy.results.limit_percent", schemaV06},
		{"a 0.6 baseline_usd", changed(schemaV05, result(func(r *policy.Result) { r.BaselineUSD = "0.5" })), "the field policy.results.baseline_usd", schemaV06},
		{"a 0.6 models", changed(schemaV05, result(func(r *policy.Result) { r.Models = []string{"gpt-5"} })), "the field policy.results.models", schemaV06},
		{"the 0.6 run reason", changed(schemaV05, func(b *Block) { b.RunReason = ArtifactRefused }), "the run reason artifact_refused", schemaV06},
		{"the 0.6 field lifetime", withField(schemaV05, "lifetime"), "the field lifetime", schemaV06},
		{"the 0.6 field rows_sha256", withField(schemaV05, "rows_sha256"), "the field rows_sha256", schemaV06},
		{"the 0.7 field agent_stated", withField(schemaV05, "agent_stated"), "the field agent_stated", schemaV07},
		{"the 0.7 field agent_stated in 0.6", withField(schemaV06, "agent_stated"), "the field agent_stated", schemaV07},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := tc.object(t)
			_, err := ParseResult(fenced(object))
			if err == nil {
				t.Fatalf("read:\n%s", object)
			}
			if want := "holds " + tc.named + ", which only " + tc.by + " defines, so it is not one "; !strings.Contains(err.Error(), want) {
				t.Errorf("the error %q does not say %q", err, want)
			}
		})
	}

	for name, object := range map[string]string{
		"a 0.5 object":             as(t, schemaV05, nil),
		"a 0.6 object with a rule": as(t, schemaV06, result(func(r *policy.Result) { r.Rule = policy.RulePullRequestBudget })),
	} {
		t.Run("reads "+name, func(t *testing.T) {
			if _, err := ParseResult(fenced(object)); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

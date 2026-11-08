package comment

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
		{"a 0.7 reason in 0.6", changed(schemaV06, result(func(r *policy.Result) { r.Reason = policy.ReasonUnmeasuredSpend })), "the reason unmeasured_spend", schemaV07},
		{"a 0.7 reason in 0.5", changed(schemaV05, result(func(r *policy.Result) { r.Reason = policy.ReasonNoRunRows })), "the reason no_run_rows", schemaV07},
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

// TestEveryRunReasonIsInTheSchema holds each reason the source can write to
// the closed list result/v0.7 gives it: the report's reason, the run reason,
// a policy result's reason and the warn-only period's reason. It reads the
// constants from the source, so a reason added later is held too.
func TestEveryRunReasonIsInTheSchema(t *testing.T) {
	data, err := os.ReadFile(resultSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Reason    enumOf `json:"reason"`
			RunReason enumOf `json:"runReason"`
			Policy    struct {
				Properties struct {
					Results struct {
						Items struct {
							Properties struct {
								Reason enumOf `json:"reason"`
							} `json:"properties"`
						} `json:"items"`
					} `json:"results"`
					WarnOnly struct {
						Properties struct {
							Reason enumOf `json:"reason"`
						} `json:"properties"`
					} `json:"warn_only"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	comment := sourceConstants(t, ".")
	run := sourceConstants(t, filepath.Join("..", "run"))
	rules := sourceConstants(t, filepath.Join("..", "policy"))
	for _, list := range []struct {
		name    string
		written []string
		enum    []string
	}{
		{"reason", comment.typed["Reason"], schema.Defs.Reason.Enum},
		{"run_reason", append(run.typed["Reason"], comment.typed["run.Reason"]...), schema.Defs.RunReason.Enum},
		{"policy.results[].reason", rules.prefixed("Reason"), schema.Defs.Policy.Properties.Results.Items.Properties.Reason.Enum},
		{"policy.warn_only.reason", rules.prefixed("Period"), schema.Defs.Policy.Properties.WarnOnly.Properties.Reason.Enum},
	} {
		if len(list.written) == 0 || len(list.enum) == 0 {
			t.Errorf("%s: the source writes %q and the schema lists %q, and neither may be empty", list.name, list.written, list.enum)
			continue
		}
		for _, reason := range list.written {
			if !slices.Contains(list.enum, reason) {
				t.Errorf("%s: the source can write %q, which result/v0.7 does not list", list.name, reason)
			}
		}
	}
}

// enumOf is a schema whose values are a closed list.
type enumOf struct {
	Enum []string `json:"enum"`
}

// constants are the string constants of a package's source, by their
// declared type, and by name for the untyped ones.
type constants struct {
	typed   map[string][]string
	untyped map[string]string
}

// prefixed is every untyped constant whose name starts with prefix.
func (c constants) prefixed(prefix string) []string {
	var values []string
	for name, value := range c.untyped {
		if strings.HasPrefix(name, prefix) {
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return values
}

// sourceConstants reads the string constants of the package in dir, its
// tests left out.
func sourceConstants(t *testing.T, dir string) constants {
	t.Helper()
	found := constants{typed: map[string][]string{}, untyped: map[string]string{}}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value := spec.(*ast.ValueSpec)
				for index, name := range value.Names {
					if index >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					text, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					switch kind := value.Type.(type) {
					case nil:
						found.untyped[name.Name] = text
					case *ast.Ident:
						found.typed[kind.Name] = append(found.typed[kind.Name], text)
					case *ast.SelectorExpr:
						if pkg, ok := kind.X.(*ast.Ident); ok {
							found.typed[pkg.Name+"."+kind.Sel.Name] = append(found.typed[pkg.Name+"."+kind.Sel.Name], text)
						}
					}
				}
			}
		}
	}
	return found
}

package policy

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
)

// The copies of result/v0.9 and result/v0.11 the comment package holds to
// their digests. The list of a policy result's reasons in result/v0.9 is
// every reason the run and the comment can write, and so is result/v0.11's
// without the reasons it keeps for a customer pack.
var (
	resultSchema     = filepath.Join("..", "comment", "testdata", "result.schema.json")
	resultSchemaV011 = filepath.Join("..", "comment", "testdata", "result-v0.11.schema.json")
)

// resultLists are the closed lists a result schema states for a policy
// result: its rules and its reasons, and the reasons it keeps for the rule
// customer_pack.
type resultLists struct {
	rules, reasons, packReasons []string
}

type enum struct {
	Enum []string `json:"enum"`
}

func readResultLists(t *testing.T, path string) resultLists {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Policy struct {
				Properties struct {
					Results struct {
						Items struct {
							Properties struct {
								Rule   enum `json:"rule"`
								Reason enum `json:"reason"`
							} `json:"properties"`
							AllOf []struct {
								If struct {
									Properties struct {
										Rule struct {
											Const string `json:"const"`
										} `json:"rule"`
										Reason *struct{} `json:"reason"`
									} `json:"properties"`
								} `json:"if"`
								Then struct {
									Properties struct {
										Reason enum `json:"reason"`
									} `json:"properties"`
								} `json:"then"`
							} `json:"allOf"`
						} `json:"items"`
					} `json:"results"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	items := schema.Defs.Policy.Properties.Results.Items
	lists := resultLists{rules: items.Properties.Rule.Enum, reasons: items.Properties.Reason.Enum}
	for _, clause := range items.AllOf {
		if clause.If.Properties.Rule.Const == ruleCustomerPack && clause.If.Properties.Reason == nil {
			lists.packReasons = clause.Then.Properties.Reason.Enum
		}
	}
	return lists
}

// TestReasonsListEveryReasonTheResultCanHold holds the reasons the engine
// takes from the pack equal to the reasons result/v0.9 lists for a policy
// result, to the reasons result/v0.11 lists for any rule but customer_pack,
// and to every Reason constant of this package's source, so a reason the Go
// code sets is one the pack may return too.
func TestReasonsListEveryReasonTheResultCanHold(t *testing.T) {
	v09, v011 := readResultLists(t, resultSchema), readResultLists(t, resultSchemaV011)
	if len(v011.packReasons) == 0 {
		t.Fatal("result/v0.11 keeps no reasons for customer_pack")
	}
	costroids := slices.DeleteFunc(slices.Clone(v011.reasons), func(reason string) bool { return slices.Contains(v011.packReasons, reason) })
	held := slices.Sorted(slices.Values(reasons))
	if compacted := slices.Compact(slices.Clone(held)); len(compacted) != len(held) {
		t.Errorf("the reasons list a reason twice: %q", held)
	}
	for _, list := range []struct {
		name string
		want []string
	}{
		{"result/v0.9's policy.results[].reason", v09.reasons},
		{"result/v0.11's policy.results[].reason but customer_pack's", costroids},
		{"the Reason constants of the source", reasonConstants(t)},
	} {
		want := slices.Sorted(slices.Values(list.want))
		if len(want) == 0 {
			t.Errorf("%s is empty", list.name)
			continue
		}
		if !slices.Equal(held, want) {
			t.Errorf("the reasons are %q, and %s are %q", held, list.name, want)
		}
	}
}

// TestRulesAreTheRulesResultV011Lists holds the rules the engine takes from
// the pack, in the order a verdict lists them, and the customer pack's rule
// to the rules result/v0.11 lists for a policy result.
func TestRulesAreTheRulesResultV011Lists(t *testing.T) {
	listed := readResultLists(t, resultSchemaV011).rules
	held := append(slices.Clone(ruleOrder), ruleCustomerPack)
	if !slices.Equal(slices.Sorted(slices.Values(held)), slices.Sorted(slices.Values(listed))) {
		t.Errorf("the rules are %q, and result/v0.11 lists %q", held, listed)
	}
	if index := slices.Index(ruleOrder, RuleRunP95); index < 1 || ruleOrder[index-1] != RuleRunIncreasePercent {
		t.Errorf("a verdict lists run_p95 at %d of %q, want after run_increase_percent", index, ruleOrder)
	}
}

// reasonConstants are the values of the string constants whose names start
// with Reason in this package's source outside its tests.
func reasonConstants(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
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
					if !strings.HasPrefix(name.Name, "Reason") || index >= len(value.Values) {
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
					found = append(found, text)
				}
			}
		}
	}
	return found
}

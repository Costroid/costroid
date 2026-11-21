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

// The copies of result/v0.9, result/v0.11 and result/v0.12 the comment
// package holds to their digests. The list of a policy result's reasons in
// result/v0.9 is every reason the default pack can return, and so is
// result/v0.11's: that version adds a rule and no reason. result/v0.12 adds
// the rule customer_pack and its four reasons, which only the Go code sets.
var (
	resultSchema     = filepath.Join("..", "comment", "testdata", "result.schema.json")
	resultSchemaV011 = filepath.Join("..", "comment", "testdata", "result-v0.11.schema.json")
	resultSchemaV012 = filepath.Join("..", "comment", "testdata", "result-v0.12.schema.json")
)

// customerPackReasons are the reasons of Costroid's customer_pack result.
var customerPackReasons = []string{ReasonPackUnread, ReasonPackInvalid, ReasonPackBound, ReasonPackResult}

// resultLists are the closed lists a result schema states for a policy
// result: its rules and its reasons.
type resultLists struct {
	rules, reasons []string
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
	return resultLists{rules: items.Properties.Rule.Enum, reasons: items.Properties.Reason.Enum}
}

// TestReasonsListEveryReasonTheResultCanHold holds the reasons the engine
// takes from the default pack equal to the reasons result/v0.9 and
// result/v0.11 list for a policy result, and those beside the customer_pack
// reasons equal to the reasons result/v0.12 lists and to every Reason
// constant of this package's source, so a reason the Go code sets is one the
// result can hold.
func TestReasonsListEveryReasonTheResultCanHold(t *testing.T) {
	v09, v011, v012 := readResultLists(t, resultSchema), readResultLists(t, resultSchemaV011), readResultLists(t, resultSchemaV012)
	held := slices.Sorted(slices.Values(reasons))
	every := slices.Sorted(slices.Values(append(slices.Clone(reasons), customerPackReasons...)))
	if compacted := slices.Compact(slices.Clone(every)); len(compacted) != len(every) {
		t.Errorf("the reasons list a reason twice: %q", every)
	}
	for _, list := range []struct {
		name       string
		held, want []string
	}{
		{"result/v0.9's policy.results[].reason", held, v09.reasons},
		{"result/v0.11's policy.results[].reason", held, v011.reasons},
		{"result/v0.12's policy.results[].reason", every, v012.reasons},
		{"the Reason constants of the source", every, reasonConstants(t)},
	} {
		want := slices.Sorted(slices.Values(list.want))
		if len(want) == 0 {
			t.Errorf("%s is empty", list.name)
			continue
		}
		if !slices.Equal(list.held, want) {
			t.Errorf("the reasons are %q, and %s are %q", list.held, list.name, want)
		}
	}
}

// TestRulesAreTheRulesResultV011Lists holds the rules the engine takes from
// the pack, in the order a verdict lists them, to the rules result/v0.11
// lists for a policy result, and those beside customer_pack to the rules
// result/v0.12 lists, which are every rule a pack's result may not take.
func TestRulesAreTheRulesResultV011Lists(t *testing.T) {
	listed := readResultLists(t, resultSchemaV011).rules
	held := slices.Clone(ruleOrder)
	if !slices.Equal(slices.Sorted(slices.Values(held)), slices.Sorted(slices.Values(listed))) {
		t.Errorf("the rules are %q, and result/v0.11 lists %q", held, listed)
	}
	if listed, reserved := readResultLists(t, resultSchemaV012).rules, slices.Sorted(slices.Values(reservedRules)); !slices.Equal(slices.Sorted(slices.Values(listed)), reserved) {
		t.Errorf("a pack's result may not take %q, and result/v0.12 lists %q", reserved, listed)
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

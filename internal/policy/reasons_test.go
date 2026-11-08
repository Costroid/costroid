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

// resultSchema is the copy of result/v0.7 the comment package holds to its
// published digest. Its list of a policy result's reasons is every reason
// the run and the comment can write in a result object.
var resultSchema = filepath.Join("..", "comment", "testdata", "result.schema.json")

// TestReasonsListEveryReasonTheResultCanHold holds the reasons the engine
// takes from the pack equal to the reasons result/v0.7 lists for a policy
// result, and to every Reason constant of this package's source, so a
// reason the Go code sets is one the pack may return too.
func TestReasonsListEveryReasonTheResultCanHold(t *testing.T) {
	data, err := os.ReadFile(resultSchema)
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
								Reason struct {
									Enum []string `json:"enum"`
								} `json:"reason"`
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
	held := slices.Sorted(slices.Values(reasons))
	if compacted := slices.Compact(slices.Clone(held)); len(compacted) != len(held) {
		t.Errorf("the reasons list a reason twice: %q", held)
	}
	for _, list := range []struct {
		name string
		want []string
	}{
		{"result/v0.7's policy.results[].reason", schema.Defs.Policy.Properties.Results.Items.Properties.Reason.Enum},
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

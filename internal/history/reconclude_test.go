package history

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/Costroid/costroid/"

// reconcluders are the two callers that re-conclude the newest check: costroid
// approve, and the webhook's approve button. Each is a package and the name of
// the function in it.
var reconcluders = []string{"cmd/costroid.reconclude", "internal/server.reconclude"}

// callSite is one call outside a test file: the package and the function it
// sits in.
type callSite struct {
	pkg, function string
	position      token.Position
}

func (c callSite) String() string {
	return c.position.String() + " in " + c.pkg + "." + c.function
}

// moduleCalls parses every Go file of the module outside tests and testdata,
// and lists the calls of each package-qualified function it is asked for, and
// of any method named ApprovedConclusion.
func moduleCalls(t *testing.T) map[string][]callSite {
	t.Helper()
	root := filepath.Join("..", "..")
	set := token.NewFileSet()
	calls := map[string][]callSite{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(relative)
		imports := map[string]string{}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			name := filepath.Base(imported)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			imports[name] = strings.TrimPrefix(imported, modulePath)
		}
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				site := callSite{pkg: pkg, function: function.Name.Name, position: set.Position(call.Pos())}
				if selector.Sel.Name == "ApprovedConclusion" {
					calls["ApprovedConclusion"] = append(calls["ApprovedConclusion"], site)
				}
				if ident, ok := selector.X.(*ast.Ident); ok {
					if imported, ok := imports[ident.Name]; ok {
						key := imported + "." + selector.Sel.Name
						calls[key] = append(calls[key], site)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// TestOneReconclude holds the re-conclude flow to one function. Both callers
// call Reconclude, and nothing else in the module renders a re-concluded check
// or decides an approval's conclusion on its own: a copy of the flow inlined
// in either caller fails it.
func TestOneReconclude(t *testing.T) {
	calls := moduleCalls(t)
	var callers []string
	for _, site := range calls["internal/history.Reconclude"] {
		callers = append(callers, site.pkg+"."+site.function)
	}
	for _, want := range reconcluders {
		if !slices.Contains(callers, want) {
			t.Errorf("%s does not call history.Reconclude; callers: %v", want, callers)
		}
	}
	for _, key := range []string{"internal/comment.Reconclude", "ApprovedConclusion"} {
		for _, site := range calls[key] {
			if site.pkg == "internal/history" && site.function == "Reconclude" {
				continue
			}
			if key == "ApprovedConclusion" && site.pkg == "internal/comment" {
				continue
			}
			t.Errorf("%s calls %s outside history.Reconclude", site, key)
		}
	}
	if len(calls["internal/comment.Reconclude"]) == 0 {
		t.Error("the walk found no call of comment.Reconclude at all")
	}
}

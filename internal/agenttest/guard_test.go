package agenttest_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/agenttest"
)

const (
	modulePath    = "github.com/Costroid/costroid"
	agenttestPath = modulePath + "/internal/agenttest"
	codexPath     = modulePath + "/internal/adapter/codex"
)

// starters are the calls that start a process, by package path. Handed an
// agent's name, each starts whichever agent PATH finds first.
var starters = map[string][]string{
	"os/exec":                    {"Command", "CommandContext"},
	"os":                         {"StartProcess"},
	"syscall":                    {"Exec", "ForkExec"},
	modulePath + "/internal/run": {"Run"},
}

// TestMain stubs PATH here too, since TestEveryStubRefusesToRun runs each
// agent by name.
func TestMain(m *testing.M) {
	os.Exit(agenttest.Main(m.Run))
}

// facts are what the guard reads from one directory's Go files.
type facts struct {
	namesAgent    bool
	startsProcess bool
	stubbed       bool
}

func (f facts) open() bool {
	return f.namesAgent && f.startsProcess && !f.stubbed
}

// unstubbed lists, under root, every package whose tests run an agent by
// name and whose TestMain does not call agenttest.Main. A package runs an
// agent by name when a string in its tests names one, and a file of it,
// test or not, starts a process or imports the Codex adapter, whose plan runs
// codex for its version. Every file is read, whatever its build tags.
func unstubbed(root string) ([]string, error) {
	var missing []string
	err := filepath.WalkDir(root, func(dir string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if name := entry.Name(); dir != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return filepath.SkipDir
		}
		found, err := inspect(dir)
		if err != nil {
			return err
		}
		if found.open() {
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return err
			}
			missing = append(missing, filepath.ToSlash(rel))
		}
		return nil
	})
	return missing, err
}

func inspect(dir string) (facts, error) {
	var found facts
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return found, err
	}
	fset := token.NewFileSet()
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return found, err
		}
		imports := importsOf(file)
		if slices.Contains(slices.Collect(maps.Values(imports)), codexPath) {
			found.startsProcess = true
		}
		test := strings.HasSuffix(name, "_test.go")
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				if pkg, fn, ok := callee(node, imports); ok && slices.Contains(starters[pkg], fn) {
					found.startsProcess = true
				}
			case *ast.BasicLit:
				if test && namesAgent(node) {
					found.namesAgent = true
				}
			case *ast.FuncDecl:
				if test && node.Recv == nil && node.Name.Name == "TestMain" && callsMain(node, imports) {
					found.stubbed = true
				}
			}
			return true
		})
	}
	return found, nil
}

// importsOf maps each name a file refers to an import by to its path.
func importsOf(file *ast.File) map[string]string {
	imports := map[string]string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	return imports
}

func callee(call *ast.CallExpr, imports map[string]string) (string, string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	pkg, ok := imports[ident.Name]
	return pkg, selector.Sel.Name, ok
}

// namesAgent is true for a string with a word that is an agent's name or a
// path that ends in one.
func namesAgent(lit *ast.BasicLit) bool {
	if lit.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	for _, word := range strings.Fields(value) {
		if slices.Contains(agenttest.Names, path.Base(word)) {
			return true
		}
	}
	return false
}

func callsMain(decl *ast.FuncDecl, imports map[string]string) bool {
	calls := false
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if pkg, fn, ok := callee(call, imports); ok && pkg == agenttestPath && fn == "Main" {
				calls = true
			}
		}
		return !calls
	})
	return calls
}

// TestEveryAgentExecPackageStubsThePath fails when a package whose tests run
// an agent by name has no TestMain that puts the stubs first on PATH, so a new
// package cannot start the machine's own claude or codex.
func TestEveryAgentExecPackageStubsThePath(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("the module root is not two directories up: %v", err)
	}
	missing, err := unstubbed(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range missing {
		t.Errorf("%s runs an agent by name in its tests, and its TestMain does not call agenttest.Main", dir)
	}
}

// TestTheGuardFindsANewPackageThatRunsAnAgent writes a package whose test
// runs claude by name, and the guard names it until its TestMain stubs PATH.
// A package that names an agent and starts nothing is left alone.
func TestTheGuardFindsANewPackageThatRunsAnAgent(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fresh/fresh_test.go", `package fresh

import (
	"os/exec"
	"testing"
)

func TestAgent(t *testing.T) {
	_ = exec.Command("claude", "-p", "x").Run()
}
`)
	write("labels/labels_test.go", `package labels

import "testing"

func TestLabel(t *testing.T) {
	if "claude" == "" {
		t.Fatal()
	}
}
`)
	write("testdata/fixture/fixture_test.go", `package fixture

import "os/exec"

var _ = exec.Command("codex")
`)
	missing, err := unstubbed(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{"fresh"}) {
		t.Fatalf("unstubbed = %q, want [fresh]", missing)
	}
	write("fresh/main_test.go", `package fresh_test

import (
	"os"
	"testing"

	stubs "`+agenttestPath+`"
)

func TestMain(m *testing.M) {
	os.Exit(stubs.Main(m.Run))
}
`)
	if missing, err = unstubbed(root); err != nil || len(missing) != 0 {
		t.Fatalf("with its TestMain: unstubbed = %q, err %v", missing, err)
	}
}

// TestEveryStubRefusesToRun runs each agent by name under this package's
// TestMain: the stub answers, with its sentence and its exit status.
func TestEveryStubRefusesToRun(t *testing.T) {
	first, _, _ := strings.Cut(os.Getenv("PATH"), string(os.PathListSeparator))
	for _, name := range agenttest.Names {
		found, err := exec.LookPath(name)
		if err != nil || filepath.Dir(found) != first {
			t.Errorf("%s resolves to %q (%v), not to the stub in %s", name, found, err, first)
			continue
		}
		var stderr strings.Builder
		command := exec.Command(name, "-p", "x")
		command.Stderr = &stderr
		err = command.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != agenttest.ExitCode {
			t.Errorf("%s: %v, want exit status %d", name, err, agenttest.ExitCode)
		}
		if want := agenttest.Refusal(name) + "\n"; stderr.String() != want {
			t.Errorf("%s printed %q, want %q", name, stderr.String(), want)
		}
	}
}

// TestStubsCoverEveryAgentTheRunStarts reads the commands costroid run knows,
// its agents and the launchers that start them, and holds each to a stub.
func TestStubsCoverEveryAgentTheRunStarts(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "run", "run.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Name.Name == "AgentFor" {
				commands = append(commands, caseStrings(decl.Body)...)
			}
		case *ast.GenDecl:
			commands = append(commands, launcherKeys(decl)...)
		}
	}
	if len(commands) < 5 {
		t.Fatalf("read %q from internal/run/run.go: the AgentFor switch or the launchers map moved", commands)
	}
	for _, command := range commands {
		if !slices.Contains(agenttest.Names, command) {
			t.Errorf("costroid run starts %q, which has no stub", command)
		}
	}
}

func caseStrings(body *ast.BlockStmt) []string {
	var found []string
	ast.Inspect(body, func(node ast.Node) bool {
		if clause, ok := node.(*ast.CaseClause); ok {
			for _, expr := range clause.List {
				if value, ok := stringOf(expr); ok {
					found = append(found, value)
				}
			}
		}
		return true
	})
	return found
}

func launcherKeys(decl *ast.GenDecl) []string {
	var found []string
	for _, spec := range decl.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || value.Names[0].Name != "launchers" || len(value.Values) != 1 {
			continue
		}
		literal, ok := value.Values[0].(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range literal.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				if key, ok := stringOf(pair.Key); ok {
					found = append(found, key)
				}
			}
		}
	}
	return found
}

func stringOf(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

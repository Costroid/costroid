package egress

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/Costroid/costroid/"
	moduleRoot = "../.."
)

// serveRoots are where the walk starts: the package behind costroid serve, and
// the SCM adapter it will reach the host through.
var serveRoots = []string{"internal/server", "internal/scm"}

// neverCalled are the standard library's ways to reach out with no list.
var neverCalled = map[string][]string{
	"net/http":   {"DefaultClient", "DefaultTransport", "Get", "Head", "Post", "PostForm", "Transport"},
	"net":        {"Dial", "DialTimeout", "DialIP", "DialTCP", "DialUDP", "DialUnix", "Dialer"},
	"crypto/tls": {"Dial", "DialWithDialer", "Dialer"},
}

// finding names one outbound path built outside this package.
type finding struct {
	position token.Position
	what     string
}

// importsOf maps each name a file gives an import to its path.
func importsOf(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = path
	}
	return names
}

func selected(names map[string]string, expr ast.Expr) (string, string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	path, ok := names[ident.Name]
	return path, selector.Sel.Name, ok
}

func isHTTPClient(names map[string]string, expr ast.Expr) bool {
	path, name, ok := selected(names, expr)
	return ok && path == "net/http" && name == "Client"
}

// scanFile finds a built http.Client and every name in neverCalled. A
// *http.Client as a parameter or a field is not one: it arrives from a builder.
func scanFile(set *token.FileSet, file *ast.File) []finding {
	names := importsOf(file)
	var found []finding
	report := func(node ast.Node, what string) {
		found = append(found, finding{set.Position(node.Pos()), what})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CompositeLit:
			if isHTTPClient(names, n.Type) {
				report(n, "http.Client literal")
			}
		case *ast.CallExpr:
			if fun, ok := n.Fun.(*ast.Ident); ok && fun.Name == "new" && len(n.Args) == 1 && isHTTPClient(names, n.Args[0]) {
				report(n, "new(http.Client)")
			}
		case *ast.SelectorExpr:
			if path, name, ok := selected(names, n); ok && slices.Contains(neverCalled[path], name) {
				report(n, path+"."+name)
			}
		}
		return true
	})
	return found
}

// sources parses one package's non-test files.
func sources(t *testing.T, set *token.FileSet, directory string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, filepath.Join(directory, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}

// servePackages walks the module's own imports from the roots.
func servePackages(t *testing.T) (*token.FileSet, map[string][]*ast.File) {
	t.Helper()
	set := token.NewFileSet()
	packages := map[string][]*ast.File{}
	queue := slices.Clone(serveRoots)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if _, seen := packages[next]; seen {
			continue
		}
		packages[next] = sources(t, set, filepath.Join(moduleRoot, next))
		for _, file := range packages[next] {
			for _, path := range importsOf(file) {
				if own, ok := strings.CutPrefix(path, modulePath); ok {
					queue = append(queue, own)
				}
			}
		}
	}
	return set, packages
}

func TestServesCodePathBuildsNoOutboundClientOutsideThisPackage(t *testing.T) {
	set, packages := servePackages(t)
	for name, files := range packages {
		if name == "internal/egress" {
			continue
		}
		for _, file := range files {
			for _, hit := range scanFile(set, file) {
				t.Errorf("%s: %s is built outside internal/egress", hit.position, hit.what)
			}
		}
	}
}

// TestTheWalkReachesEveryPackageServeUses keeps the scan from passing because it
// read nothing, and holds the line docs/install.md states: reconcile runs in CI.
func TestTheWalkReachesEveryPackageServeUses(t *testing.T) {
	_, packages := servePackages(t)
	for _, want := range []string{"internal/server", "internal/oidc", "internal/store", "internal/catalog", "internal/egress", "internal/scm", "migrations"} {
		if _, ok := packages[want]; !ok {
			t.Errorf("the walk missed %s: %v", want, slices.Sorted(maps.Keys(packages)))
		}
	}
	if _, ok := packages["internal/reconcile"]; ok {
		t.Error("costroid serve now reaches internal/reconcile, whose client is off the list")
	}
}

func TestTheScanFindsEveryWayOut(t *testing.T) {
	const source = `package sample

import (
	"crypto/tls"
	"net"
	web "net/http"
)

type holder struct {
	passedIn *web.Client
}

func allowed(client *web.Client) *web.Client { return client }

func denied() {
	_ = &web.Client{}
	_ = web.Client{}
	_ = new(web.Client)
	_ = web.DefaultClient
	_ = web.DefaultTransport
	_ = &web.Transport{}
	_, _ = web.Get("https://example.test")
	_, _ = web.Post("https://example.test", "", nil)
	_, _ = web.Head("https://example.test")
	_, _ = web.PostForm("https://example.test", nil)
	_, _ = net.Dial("tcp", "example.test:443")
	_, _ = net.DialTimeout("tcp", "example.test:443", 0)
	_ = net.Dialer{}
	_, _ = tls.Dial("tcp", "example.test:443", nil)
}
`
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "sample.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, hit := range scanFile(set, file) {
		got = append(got, hit.what)
	}
	want := []string{
		"http.Client literal", "http.Client literal", "new(http.Client)",
		"net/http.DefaultClient", "net/http.DefaultTransport", "net/http.Transport",
		"net/http.Get", "net/http.Post", "net/http.Head", "net/http.PostForm",
		"net.Dial", "net.DialTimeout", "net.Dialer", "crypto/tls.Dial",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

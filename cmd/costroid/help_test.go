package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

// flagSources are the directories whose flag sets back a command of the
// table.
var flagSources = []string{".", filepath.Join("..", "..", "internal", "server")}

// flagless are the commands that define no flag.
var flagless = []string{"approve", "validate", "lint-recipes", "version"}

// computedFlags gives the names of flags a command defines from a table of its
// own, rather than as a literal.
var computedFlags = map[string]func() []string{
	"ingest-run": func() []string {
		var names []string
		for _, kind := range sourceKinds {
			if kind.alias {
				names = append(names, kind.name)
			}
		}
		return names
	},
}

// subcommandHelp are the commands that also answer help as a subcommand.
var subcommandHelp = []string{"gateway", "keys", "egress"}

func TestEveryHelpOpensWithUsageOnStdout(t *testing.T) {
	for _, entry := range commands {
		t.Run(entry.name, func(t *testing.T) {
			if !strings.HasPrefix(entry.help, "Usage:\n  costroid "+entry.name+" ") && !strings.HasPrefix(entry.help, "Usage:\n  costroid "+entry.name+"\n") {
				t.Errorf("help %s does not open with its usage:\n%s", entry.name, entry.help)
			}
			asks := [][]string{{"help", entry.name}, {entry.name, "-h"}, {entry.name, "-help"}, {entry.name, "--help"}}
			if slices.Contains(subcommandHelp, entry.name) {
				asks = append(asks, []string{entry.name, "help"})
			}
			for _, args := range asks {
				code, stdout, stderr := execute(t, args...)
				if code != 0 || stdout != entry.help || stderr != "" {
					t.Errorf("%v: exit %d, stderr %q, stdout:\n%s", args, code, stderr, stdout)
				}
			}
		})
	}
}

// exitRow is one row of an exit code table: the code, then what it means from
// column 20. A row's meaning goes on at column 20 on the lines after it.
var (
	exitRow      = regexp.MustCompile(`^  (\d+|the agent's own) +\S`)
	exitRowWrap  = regexp.MustCompile(`^ {20}\S`)
	exitRowStart = 20
)

func TestEveryHelpEndsWithItsExitCodes(t *testing.T) {
	for _, entry := range commands {
		header := "\nExit codes of " + entry.name + ":\n"
		_, table, found := strings.Cut(entry.help, header)
		if !found {
			t.Errorf("help %s has no %q", entry.name, strings.TrimSpace(header))
			continue
		}
		rows := strings.Split(strings.TrimSuffix(table, "\n"), "\n")
		if !exitRow.MatchString(rows[0]) {
			t.Errorf("help %s: the table opens with %q", entry.name, rows[0])
		}
		for _, line := range rows {
			if len(line) <= exitRowStart || line[exitRowStart-1] != ' ' || line[exitRowStart] == ' ' ||
				!exitRow.MatchString(line) && !exitRowWrap.MatchString(line) {
				t.Errorf("help %s: %q is not a row of the exit code table", entry.name, line)
			}
		}
	}
}

func TestHelpLinesFitEightyColumns(t *testing.T) {
	pages := map[string]string{"the short usage": usage.String()}
	for _, entry := range commands {
		pages["help "+entry.name] = entry.help
	}
	for name, page := range pages {
		for number, line := range strings.Split(page, "\n") {
			if width := utf8.RuneCountInString(line); width > usageWidth {
				t.Errorf("%s, line %d, is %d columns: %s", name, number+1, width, line)
			}
		}
	}
}

func TestNoArgumentsExitsTwo(t *testing.T) {
	code, stdout, stderr := execute(t)
	if code != 2 || stderr != "" || stdout != usage.String() {
		t.Errorf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
}

func TestShortUsageGroupsTheLedgerCommands(t *testing.T) {
	text := usage.String()
	if !strings.Contains(text, "\nLedger commands: gateway, serve, keys, egress.\n") {
		t.Errorf("the short usage does not group the ledger commands:\n%s", text)
	}
	others := text[strings.Index(text, "Other commands:"):strings.Index(text, "Ledger commands:")]
	for _, name := range []string{"gateway", "serve", "keys", "egress"} {
		if regexp.MustCompile(`\b` + name + `\b`).MatchString(others) {
			t.Errorf("%s is named under other commands too", name)
		}
	}
}

func TestEveryFlagIsNamedInItsHelp(t *testing.T) {
	flags := commandFlags(t)
	for _, entry := range commands {
		names := flags[entry.name]
		if len(names) == 0 != slices.Contains(flagless, entry.name) {
			t.Errorf("%s: %d flags found, and flagless lists it: %t", entry.name, len(names), slices.Contains(flagless, entry.name))
		}
		for _, name := range names {
			if !regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `([^a-z0-9-]|$)`).MatchString(entry.help) {
				t.Errorf("help %s does not name --%s", entry.name, name)
			}
		}
		delete(flags, entry.name)
	}
	for name := range flags {
		t.Errorf("a flag set names %q, which is no command", name)
	}
}

// flagDefiners are the methods of flag.FlagSet that define a flag, with the
// index of the argument that names it.
var flagDefiners = map[string]int{
	"Bool": 0, "BoolFunc": 0, "Duration": 0, "Float64": 0, "Func": 0, "Int": 0, "Int64": 0,
	"String": 0, "Uint": 0, "Uint64": 0, "BoolVar": 1, "DurationVar": 1, "Float64Var": 1,
	"Int64Var": 1, "IntVar": 1, "StringVar": 1, "TextVar": 1, "Uint64Var": 1, "UintVar": 1, "Var": 1,
}

// commandFlags reads, from the source, the flags every command's flag sets
// define. A flag set belongs to the command its name opens with. A function
// that takes a flag set, such as addAccountFlags, gives its flags to every
// command whose set is passed to it.
func commandFlags(t *testing.T) map[string][]string {
	t.Helper()
	var funcs []*ast.FuncDecl
	for _, dir := range flagSources {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range entries {
			if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, file.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range parsed.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
					funcs = append(funcs, fn)
				}
			}
		}
	}
	// makers are the functions that return a new flag set, by the command
	// it belongs to.
	makers := map[string]string{}
	for _, fn := range funcs {
		if command := newFlagSetCommand(fn.Body); command != "" && returnsFlagSet(fn) {
			makers[fn.Name.Name] = command
		}
	}
	flags := map[string][]string{}
	helpers := map[string][]string{}
	passes := map[string][]string{}
	for _, fn := range funcs {
		sets := map[string]string{}
		for _, param := range fn.Type.Params.List {
			if isFlagSetPointer(param.Type) {
				for _, name := range param.Names {
					sets[name.Name] = ""
				}
			}
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
					break
				}
				if set, ok := node.Lhs[0].(*ast.Ident); ok {
					if command := setCommand(node.Rhs[0], makers); command != "" {
						sets[set.Name] = command
					}
				}
			case *ast.CallExpr:
				if command, name, ok := flagDefinition(t, node, sets); ok {
					if command == "" {
						helpers[fn.Name.Name] = append(helpers[fn.Name.Name], name)
					} else {
						flags[command] = append(flags[command], name)
					}
				}
				if callee, ok := node.Fun.(*ast.Ident); ok {
					for _, arg := range node.Args {
						if set, ok := arg.(*ast.Ident); ok && sets[set.Name] != "" {
							passes[sets[set.Name]] = append(passes[sets[set.Name]], callee.Name)
						}
					}
				}
			}
			return true
		})
	}
	for command, callees := range passes {
		for _, callee := range callees {
			flags[command] = append(flags[command], helpers[callee]...)
		}
	}
	for command, names := range flags {
		if i := slices.Index(names, computed); i >= 0 {
			compute, ok := computedFlags[command]
			if !ok {
				t.Fatalf("%s defines a flag whose name is not a literal, and computedFlags does not list it", command)
			}
			flags[command] = append(slices.DeleteFunc(names, func(name string) bool { return name == computed }), compute()...)
		}
	}
	return flags
}

// computed stands for a flag whose name the source computes.
const computed = "\x00computed"

// flagDefinition reads a call that defines a flag on one of sets, and answers
// the set's command and the flag's name.
func flagDefinition(t *testing.T, call *ast.CallExpr, sets map[string]string) (string, string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	command, isSet := sets[receiver.Name]
	index, defines := flagDefiners[selector.Sel.Name]
	if !isSet || !defines {
		return "", "", false
	}
	literal, ok := call.Args[index].(*ast.BasicLit)
	if !ok {
		return command, computed, true
	}
	name, err := strconv.Unquote(literal.Value)
	if err != nil {
		t.Fatal(err)
	}
	return command, name, true
}

// setCommand answers the command of a new flag set an expression makes, or
// nothing when it makes none.
func setCommand(expression ast.Expr, makers map[string]string) string {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return ""
	}
	if callee, ok := call.Fun.(*ast.Ident); ok {
		return makers[callee.Name]
	}
	if !isFlagFunc(call.Fun, "NewFlagSet") {
		return ""
	}
	name := call.Args[0]
	for {
		binary, ok := name.(*ast.BinaryExpr)
		if !ok {
			break
		}
		name = binary.X
	}
	literal, ok := name.(*ast.BasicLit)
	if !ok {
		return ""
	}
	text, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	command, _, _ := strings.Cut(text, " ")
	return command
}

// newFlagSetCommand answers the command of the flag set a body makes.
func newFlagSetCommand(body *ast.BlockStmt) string {
	command := ""
	ast.Inspect(body, func(node ast.Node) bool {
		if expression, ok := node.(ast.Expr); ok && command == "" {
			command = setCommand(expression, nil)
		}
		return command == ""
	})
	return command
}

func returnsFlagSet(fn *ast.FuncDecl) bool {
	results := fn.Type.Results
	return results != nil && len(results.List) == 1 && isFlagSetPointer(results.List[0].Type)
}

func isFlagSetPointer(expression ast.Expr) bool {
	star, ok := expression.(*ast.StarExpr)
	return ok && isFlagFunc(star.X, "FlagSet")
}

func isFlagFunc(expression ast.Expr, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "flag" && selector.Sel.Name == name
}

func TestGatewayMintWarnsWhenTheEventFileCannotBeRead(t *testing.T) {
	job := newGatewayJob(t)
	job.request(t)
	request := filepath.Join(job.dir, gateway.RequestFile)
	stub := gatewaytest.New(t, gatewayAdmin)
	if code, _, stderr := job.mint(t, stub, "--request", request); code != 0 || strings.Contains(stderr, "warning") {
		t.Fatalf("a readable event file: exit %d, %s", code, stderr)
	}
	absent := filepath.Join(job.dir, "absent-event.json")
	t.Setenv("GITHUB_EVENT_PATH", absent)
	stub = gatewaytest.New(t, gatewayAdmin)
	code, _, stderr := job.mint(t, stub, "--request", request, "--author-daily-cap-usd", "5")
	if code != 0 {
		t.Fatalf("exit %d, %s", code, stderr)
	}
	if !strings.Contains(stderr, "costroid: warning: the event file "+strconv.Quote(absent)+" could not be read") {
		t.Errorf("the warning does not name the event file:\n%s", stderr)
	}
	got, ok := stub.Key(gatewayAlias)
	if !ok || got.Metadata[gateway.MetaPullRequest] != "" || got.User != "" {
		t.Errorf("registered %+v", got)
	}
}

// TestInitHelpNamesEveryAgentItAccepts holds init's --agent sentence to the
// agents each host's recipes take, so an agent added to init is named there.
func TestInitHelpNamesEveryAgentItAccepts(t *testing.T) {
	text := strings.Join(strings.Fields(initHelp), " ")
	sentence := regexp.MustCompile(`--agent is (.+?) on GitHub, and (.+?) on GitLab\.`).FindStringSubmatch(text)
	if sentence == nil {
		t.Fatalf("init's help has no --agent sentence:\n%s", initHelp)
	}
	for host, named := range map[string]string{githubHost: sentence[1], gitlabHost: sentence[2]} {
		listed := strings.FieldsFunc(strings.ReplaceAll(named, " or ", ", "), func(r rune) bool { return r == ',' || r == ' ' })
		for agent := range initRecipes[host] {
			if !slices.Contains(listed, agent) {
				t.Errorf("init's help does not name the %s agent %s: %q", host, agent, named)
			}
		}
	}
}

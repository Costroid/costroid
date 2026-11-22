package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/comment"
	receipts "github.com/Costroid/costroid/internal/receipt"
	"github.com/Costroid/costroid/internal/server"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// sourceDir is this package's directory, found before any test changes
// directory.
var sourceDir = func() string {
	dir, err := filepath.Abs(".")
	if err != nil {
		panic(err)
	}
	return dir
}()

// objectsDir holds the schema of each object --json prints, and examples of
// each that the schema accepts and rejects.
var objectsDir = filepath.Join(sourceDir, "testdata", "objects")

// The fixtures the walks below read, by paths that hold in any directory:
// rows a recorded run wrote, rows a format refuses nothing of, and the
// execution file of a recorded claude-code-action run.
var (
	recordedRows    = filepath.Join(sourceDir, filepath.FromSlash(realRows))
	requestRows     = filepath.Join(sourceDir, exampleRows)
	recordedExecute = filepath.Join(sourceDir, filepath.FromSlash(executionFile))
)

// outputPage is the page that describes the two streams and the objects.
var outputPage = filepath.Join(filepath.Dir(troubleshootingPage), "output.md")

// objectSchema compiles the schema of one of the objects.
func objectSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(filepath.Join(objectsDir, name+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name+".schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(name + ".schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validObject(schema *jsonschema.Schema, data string) error {
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

// oneObject holds printed to one JSON object on one line that opens with
// schema, and answers it decoded.
func oneObject(t *testing.T, printed, schema string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(printed, `{"schema":"`+schema+`",`) || strings.Count(printed, "\n") != 1 || !strings.HasSuffix(printed, "}\n") {
		t.Fatalf("stdout is not one object on one line that opens with the schema %s:\n%s", schema, printed)
	}
	decoder := json.NewDecoder(strings.NewReader(printed))
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, printed)
	}
	if decoder.More() {
		t.Fatalf("stdout holds more than one object:\n%s", printed)
	}
	return object
}

// lintWorkflows is a repository with one workflow lint has findings and notes
// for, and the directory it is in.
func lintWorkflows(t *testing.T) string {
	t.Helper()
	source := readFile(t, filepath.Join(customerTestdata, "mention.yml"))
	root := t.TempDir()
	writeFile(t, filepath.Join(root, defaultWorkflows, "mention.yml"), source)
	return root
}

// TestJSONOnDoctorLintAndValidateIsOneObjectOnStdout runs each of the three
// with and without --json. Under the flag stdout is one object and nothing
// else, stderr and the exit code are what they are without it, and the object
// holds what the lines hold: each check's line and the count, each finding's
// five parts and the counts, each problem or the file's state.
func TestJSONOnDoctorLintAndValidateIsOneObjectOnStdout(t *testing.T) {
	clearCI(t)
	t.Run("doctor", func(t *testing.T) {
		for name, root := range map[string]string{
			"a warn":            t.TempDir(),
			"a fail":            doctorRepository(t, map[string]string{configFile: "policy:\n  no_such_key: 1\n"}),
			"nothing to fix":    initialized(t, "github"),
			"nothing on GitLab": initialized(t, "gitlab"),
		} {
			host := func() *fakeHost { return ghHost("ANTHROPIC_API_KEY\n", "") }
			code, text, said := runDoctor(t, root, host())
			jsonCode, printed, jsonSaid := runDoctor(t, root, host(), "--json")
			if jsonCode != code || jsonSaid != said {
				t.Errorf("%s: --json exits %d and says %q, and without it doctor exits %d and says %q", name, jsonCode, jsonSaid, code, said)
			}
			object := oneObject(t, printed, doctorSchema)
			var lines []string
			for _, check := range object["checks"].([]any) {
				check := check.(map[string]any)
				line := check["line"].(string)
				if !strings.HasPrefix(line, check["status"].(string)+": ") {
					t.Errorf("%s: the check %q has the status %q", name, line, check["status"])
				}
				lines = append(lines, line)
			}
			count := object["count"].(map[string]any)
			lines = append(lines, fmt.Sprintf("costroid: doctor: %v fail, %v warn, %v ok", count["fail"], count["warn"], count["ok"]))
			if want := strings.Join(lines, "\n") + "\n"; text != want || len(lines) < 3 {
				t.Errorf("%s: the object holds\n%sand doctor prints\n%s", name, want, text)
			}
		}
	})
	t.Run("lint", func(t *testing.T) {
		t.Chdir(lintWorkflows(t))
		code, text, said := execute(t, "lint")
		jsonCode, printed, jsonSaid := execute(t, "lint", "--json")
		if code != exitRejected || jsonCode != code || jsonSaid != said {
			t.Errorf("--json exits %d and says %q, and without it lint exits %d and says %q", jsonCode, jsonSaid, code, said)
		}
		object := oneObject(t, printed, lintSchema)
		var lines []string
		for _, finding := range object["findings"].([]any) {
			finding := finding.(map[string]any)
			lines = append(lines, fmt.Sprintf("%v:%v: %v: %v: %v", finding["file"], finding["line"], finding["severity"], finding["rule"], finding["message"]))
		}
		count := object["count"].(map[string]any)
		lines = append(lines, fmt.Sprintf("costroid: %v file, %v with an agent: %v high, %v medium, %v notes", object["files"], object["agents"], count["high"], count["medium"], count["note"]))
		if want := strings.Join(lines, "\n") + "\n"; text != want || len(lines) != 7 {
			t.Errorf("the object holds\n%sand lint prints\n%s", want, text)
		}
	})
	t.Run("validate", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		for _, file := range []struct{ name, text, state string }{
			{"no file", "", fileAbsent},
			{"a valid file", "policy:\n  run_budget_usd: \"5\"\n", fileValid},
			{"a file that breaks the schema", "policy:\n  no_such_key: 1\n  run_budget_usd: 1e3\n", fileInvalid},
		} {
			if file.text != "" {
				writeFile(t, filepath.Join(dir, configFile), file.text)
			}
			code, text, said := execute(t, "validate")
			jsonCode, printed, jsonSaid := execute(t, "validate", "--json")
			if jsonCode != code || jsonSaid != said {
				t.Errorf("%s: --json exits %d and says %q, and without it validate exits %d and says %q", file.name, jsonCode, jsonSaid, code, said)
			}
			object := oneObject(t, printed, validateSchema)
			var lines []string
			for _, problem := range object["problems"].([]any) {
				problem := problem.(map[string]any)
				lines = append(lines, fmt.Sprintf("%v: %v: %v", object["file"], problem["pointer"], problem["message"]))
			}
			switch object["state"] {
			case fileAbsent:
				lines = append(lines, configFile+": no file, so the defaults apply")
			case fileValid:
				lines = append(lines, configFile+": valid")
			}
			if want := strings.Join(lines, "\n") + "\n"; text != want || object["state"] != file.state {
				t.Errorf("%s: the object reads %v and holds\n%sand validate prints\n%s", file.name, object["state"], want, text)
			}
		}
	})
}

// initialized is a repository init has just written for a host, which doctor
// finds nothing to fix in.
func initialized(t *testing.T, host string) string {
	t.Helper()
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, host, "--agent", "claude"); code != 0 {
		t.Fatalf("init %s exited %d: %s", host, code, stderr)
	}
	return root
}

// outputSection is the part of the output page under an object's heading.
func outputSection(t *testing.T, command string) string {
	t.Helper()
	_, section, found := strings.Cut(readFile(t, outputPage), "\n### `costroid "+command+" --json`\n")
	if !found {
		t.Fatalf("docs/output.md has no section on costroid %s --json", command)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	section, _, _ = strings.Cut(section, "\n### ")
	return section
}

// TestEveryJSONObjectMatchesItsSchema holds each of the four objects to the
// JSON Schema beside these tests: the schema accepts what the command prints,
// the examples named accepted and the example the documentation shows, and
// rejects each example named rejected.
func TestEveryJSONObjectMatchesItsSchema(t *testing.T) {
	clearCI(t)
	docs := regexp.MustCompile("(?s)```json\n(.*?)\n```")
	printed := map[string][]string{}
	for _, root := range []string{t.TempDir(), initialized(t, "github"), doctorRepository(t, map[string]string{configFile: "policy:\n  no_such_key: 1\n"})} {
		_, object, _ := runDoctor(t, root, ghHost("", ""), "--json")
		printed["doctor"] = append(printed["doctor"], object)
	}
	t.Chdir(lintWorkflows(t))
	for _, args := range [][]string{{"lint", "--json"}, {"validate", "--json"}, {"version", "--json"}} {
		_, object, _ := execute(t, args...)
		printed[args[0]] = append(printed[args[0]], object)
	}
	writeFile(t, configFile, "policy:\n  no_such_key: 1\n")
	for _, args := range [][]string{{"validate", "--json"}, {"version", "--json"}} {
		_, object, _ := execute(t, args...)
		printed[args[0]] = append(printed[args[0]], object)
	}
	writeFile(t, configFile, "policy:\n  run_budget_usd: \"5\"\n")
	_, valid, _ := execute(t, "validate", "--json")
	printed["validate"] = append(printed["validate"], valid)

	for _, name := range []string{"doctor", "lint", "validate", "version"} {
		schema := objectSchema(t, name)
		for i, object := range printed[name] {
			if err := validObject(schema, object); err != nil {
				t.Errorf("costroid %s --json, run %d, prints what its schema rejects: %v\n%s", name, i+1, err, object)
			}
		}
		shown := docs.FindAllStringSubmatch(outputSection(t, name), -1)
		if len(shown) == 0 {
			t.Errorf("docs/output.md shows no object of costroid %s --json", name)
		}
		for _, block := range shown {
			if err := validObject(schema, block[1]); err != nil {
				t.Errorf("docs/output.md shows an object of costroid %s --json that its schema rejects: %v", name, err)
			}
		}
		examples, err := filepath.Glob(filepath.Join(objectsDir, name, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		accepted, rejected := 0, 0
		for _, path := range examples {
			err := validObject(schema, readFile(t, path))
			switch base := filepath.Base(path); {
			case strings.HasPrefix(base, "accepted-"):
				accepted++
				if err != nil {
					t.Errorf("%s is rejected: %v", path, err)
				}
			case strings.HasPrefix(base, "rejected-"):
				rejected++
				if err == nil {
					t.Errorf("%s is accepted", path)
				}
			default:
				t.Errorf("%s is named neither accepted nor rejected", path)
			}
		}
		if accepted < 2 || rejected < 3 {
			t.Errorf("costroid %s --json has %d accepted and %d rejected examples, which is too few to hold its schema", name, accepted, rejected)
		}
	}
}

// droppable says whether --quiet drops a line of stderr: one that is neither
// a warning nor part of an error. Every line of the walk below that stays is
// named here by how it opens.
func droppable(line string, kept []string) bool {
	return !strings.HasPrefix(line, "costroid: warning: ") && !slices.ContainsFunc(kept, func(opens string) bool { return strings.HasPrefix(line, opens) })
}

// quietWalk are command lines that say something on stderr, with the lines
// of an error each ends on, by how they open.
type quietLine struct {
	name   string
	args   func(t *testing.T) []string
	errors []string
	// after is how many arguments stand before the command's flags, beyond
	// its name: init reads its host first.
	after int
}

// quietWalk is called from this package's directory, and each of its lines
// then moves to a directory of its own.
func quietWalk() []quietLine {
	fixed := func(args ...string) func(*testing.T) []string {
		return func(t *testing.T) []string {
			t.Chdir(t.TempDir())
			return args
		}
	}
	return []quietLine{
		{name: "demo", args: fixed("demo")},
		{name: "validate, a valid file", args: func(t *testing.T) []string {
			t.Chdir(t.TempDir())
			writeFile(t, configFile, "policy:\n  run_budget_usd: \"5\"\n")
			return []string{"validate"}
		}},
		{name: "init", after: 1, args: func(t *testing.T) []string {
			t.Chdir(gitRepository(t))
			return []string{"init", "github", "--agent", "claude", "--action-ref", testCommit}
		}},
		{name: "init --existing", after: 1, args: func(t *testing.T) []string {
			t.Chdir(gitRepository(t))
			writeFile(t, "review.yml", existingReview)
			return []string{"init", "github", "--agent", "claude-code-action", "--action-ref", testCommit, "--existing", "review.yml"}
		}},
		{name: "run", args: func(t *testing.T) []string {
			claude := replayClaude(t)
			t.Chdir(t.TempDir())
			return []string{"run", "--budget-usd", "5", "--", claude, "-p", "work"}
		}},
		{name: "run in a job, which warns", args: func(t *testing.T) []string {
			claude := replayClaude(t)
			t.Setenv("GITHUB_ACTIONS", "true")
			t.Chdir(t.TempDir())
			return []string{"run", "--budget-usd", "5", "--out", "rows.jsonl", "--", claude, "-p", "work"}
		}},
		{name: "ingest-run", args: fixed("ingest-run", "--execution-file", recordedExecute, "--out", "rows.jsonl")},
		{name: "report --preview", args: fixed("report", "--preview", "--rows", recordedRows)},
		{name: "export --format, which warns", args: fixed("export", "--rows", requestRows, "--format", "datadog", "--out", "costs.csv")},
		{name: "a file that is not there", args: fixed("validate", "absent.yml"), errors: []string{"costroid: absent.yml does not exist", "See "}},
		{name: "a wrong command line", args: fixed("demo", "extra"), errors: []string{"costroid: demo takes no argument", "Run costroid help"}},
		{name: "report outside a job", args: fixed("report", "--rows", recordedRows), errors: []string{"costroid: report refused", "report posts from", "Run costroid report --preview"}},
		{name: "receipt", args: func(t *testing.T) []string {
			onAReceiptMachine(t)
			return []string{"receipt"}
		}},
		{name: "receipt in a job", args: func(t *testing.T) []string {
			onAReceiptMachine(t)
			t.Setenv("CI", "true")
			return []string{"receipt"}
		}, errors: []string{"costroid: receipt refused", "A receipt states", "Run costroid receipt"}},
	}
}

// TestQuietKeepsWarningsAndErrorsAndDropsTheRest runs command lines that say
// something on stderr, without --quiet and under each of its names. Under it
// stderr is what it was without, less every line that is neither a warning
// nor part of an error, and the walk holds a line of each kind. No line that
// --quiet drops is written as a warning in the source.
func TestQuietKeepsWarningsAndErrorsAndDropsTheRest(t *testing.T) {
	dropped, warnings, errors := 0, 0, 0
	for _, line := range quietWalk() {
		t.Run(line.name, func(t *testing.T) {
			var said []string
			for _, flag := range []string{"", "-q", "--quiet"} {
				clearCI(t)
				t.Chdir(sourceDir)
				args := line.args(t)
				if flag != "" {
					args = slices.Insert(args, 1+line.after, flag)
				}
				_, _, stderr := execute(t, args...)
				said = append(said, stderr)
			}
			var want []string
			for text := range strings.SplitSeq(strings.TrimSuffix(said[0], "\n"), "\n") {
				switch {
				case droppable(text, line.errors):
					dropped++
					continue
				case strings.HasPrefix(text, "costroid: warning: "):
					warnings++
				default:
					errors++
				}
				want = append(want, text+"\n")
			}
			if said[0] == "" {
				t.Fatalf("%s says nothing on stderr, so it holds nothing", line.name)
			}
			for i, flag := range []string{"-q", "--quiet"} {
				if got := said[i+1]; got != strings.Join(want, "") {
					t.Errorf("under %s stderr is\n%swant\n%swithout it\n%s", flag, got, strings.Join(want, ""), said[0])
				}
			}
		})
	}
	if dropped < 20 || warnings < 2 || errors < 5 {
		t.Errorf("the walk holds %d lines that --quiet drops, %d warnings and %d lines of an error, which is too few of one kind", dropped, warnings, errors)
	}
	for _, said := range saidLiterals(t) {
		if strings.Contains(said, "warning") {
			t.Errorf("a line that --quiet drops reads as a warning: %q", said)
		}
	}
}

// commandSources are the files that hold the commands.
func commandSources(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(sourceDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = slices.DeleteFunc(files, func(name string) bool { return strings.HasSuffix(name, "_test.go") })
	for _, name := range []string{"serve.go", "keys.go", "egress.go"} {
		files = append(files, filepath.Join(sourceDir, "..", "..", "internal", "server", name))
	}
	return files
}

// printCalls reads every call in the commands' source that prints a string
// written there, and gives each to visit: the function called, as the source
// writes it, the writer it is given and the string.
func printCalls(t *testing.T, visit func(function, writer, text string)) {
	t.Helper()
	for _, name := range commandSources(t) {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			literal, ok := call.Args[1].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			writer := ""
			switch to := call.Args[0].(type) {
			case *ast.Ident:
				writer = to.Name
			case *ast.SelectorExpr:
				writer = to.Sel.Name
			}
			visit(pkg.Name+"."+selector.Sel.Name, writer, text)
			return true
		})
	}
}

// saidLiterals are the lines the commands print through cli.Say, which
// --quiet drops.
func saidLiterals(t *testing.T) []string {
	t.Helper()
	var said []string
	printCalls(t, func(function, _, text string) {
		if function == "cli.Say" {
			said = append(said, text)
		}
	})
	if len(said) < 40 {
		t.Fatalf("read %d lines the commands say, too few to be all of them", len(said))
	}
	return said
}

// quietForms gives each command that takes a subcommand every form it has.
// --quiet stands among a form's own flags, after the subcommand.
var quietForms = map[string][][]string{
	"keys":    {{"keys", "create"}, {"keys", "revoke"}},
	"egress":  {{"egress", "report"}},
	"gateway": {{"gateway", "keygen"}, {"gateway", "public-key"}, {"gateway", "request"}, {"gateway", "mint"}, {"gateway", "close"}},
	"app":     {{"app", "manifest"}, {"app", "create"}},
}

// TestQuietNeverChangesStdoutOrTheExitCode runs command lines that print on
// stdout, that fail and that are wrong, without --quiet and under each of
// its names: stdout and the exit code are the same. Every command, and every
// subcommand, takes both names, and every help page lists them.
func TestQuietNeverChangesStdoutOrTheExitCode(t *testing.T) {
	rows := recordedRows
	workflows := lintWorkflows(t)
	lines := []struct {
		args []string
		code int
	}{
		{[]string{"demo"}, 0},
		{[]string{"demo", "--markdown"}, 0},
		{[]string{"demo", "--state", "capped"}, 0},
		{[]string{"lint"}, exitRejected},
		{[]string{"lint", "--json"}, exitRejected},
		{[]string{"validate"}, 0},
		{[]string{"validate", "--json"}, 0},
		{[]string{"validate", "absent.yml"}, exitFailure},
		{[]string{"version"}, 0},
		{[]string{"version", "--verbose"}, 0},
		{[]string{"version", "--json"}, 0},
		{[]string{"policy", "--rows", rows}, 0},
		{[]string{"policy", "--rows", rows, "--json"}, 0},
		{[]string{"report", "--preview", "--rows", rows}, 0},
		{[]string{"report", "--rows", rows}, exitFailure},
		{[]string{"export", "--rows", requestRows, "--format", "focus-csv"}, 0},
		{[]string{"demo", "extra"}, exitUsage},
		{[]string{"doctor", "--frob"}, exitUsage},
	}
	for _, line := range lines {
		clearCI(t)
		t.Chdir(workflows)
		code, stdout, _ := execute(t, line.args...)
		if code != line.code {
			t.Errorf("%v exits %d, want %d", line.args, code, line.code)
		}
		for _, flag := range []string{"-q", "--quiet"} {
			quietCode, quietStdout, _ := execute(t, slices.Insert(slices.Clone(line.args), 1, flag)...)
			if quietCode != code || quietStdout != stdout {
				t.Errorf("%v: under %s the exit code is %d and stdout\n%swithout it %d and\n%s", line.args, flag, quietCode, quietStdout, code, stdout)
			}
		}
	}
	for _, entry := range commands {
		forms, grouped := quietForms[entry.name]
		if grouped != slices.Contains(subcommandHelp, entry.name) {
			t.Errorf("%s: quietForms and subcommandHelp do not agree on whether it takes a subcommand", entry.name)
		}
		if !grouped {
			forms = [][]string{{entry.name}}
		}
		for _, form := range forms {
			for _, flag := range []string{"-q", "--quiet", "--quiet=false"} {
				args := append(slices.Clone(form), flag, "--help")
				if code, stdout, stderr := execute(t, args...); code != 0 || stdout+stderr != entry.help {
					t.Errorf("%v does not read %s as its own flag: exit %d, stderr %q", form, flag, code, stderr)
				}
			}
		}
		if !strings.Contains(entry.help, "\n  "+cli.QuietRow[0]+"  ") {
			t.Errorf("help %s does not list %s", entry.name, cli.QuietRow[0])
		}
	}
	onAReceiptMachine(t)
	code, stdout, stderr := execute(t, "receipt")
	if code != 0 || !strings.HasPrefix(stdout, receipts.Marker+"\n") || stderr == "" {
		t.Fatalf("receipt: exit %d, stderr %q", code, stderr)
	}
	for _, flag := range []string{"-q", "--quiet"} {
		if quietCode, quietStdout, quietStderr := execute(t, "receipt", flag); quietCode != code || quietStdout != stdout || quietStderr != "" {
			t.Errorf("receipt %s: exit %d, stderr %q, and stdout\n%swithout it\n%s", flag, quietCode, quietStderr, quietStdout, stdout)
		}
	}
}

// refusedForms are the eight forms that read no argument and took one
// without a word: each with a command line that is right but for the
// argument, and the name the refusal gives it.
var refusedForms = []struct {
	form string
	args []string
}{
	{"serve", []string{"serve", "--addr", ":0", "extra"}},
	{"keys create", []string{"keys", "create", "--owner", "github.com:example", "extra"}},
	{"egress report", []string{"egress", "report", "--json", "extra"}},
	{"gateway keygen", []string{"gateway", "keygen", "--out", "sealing-key", "extra"}},
	{"gateway public-key", []string{"gateway", "public-key", "extra"}},
	{"gateway request", []string{"gateway", "request", "--out", "request", "extra"}},
	{"gateway mint", []string{"gateway", "mint", "--url", "https://gateway.example", "--budget-usd", "5", "--lifetime-minutes", "30", "--max-request-tokens", "200000", "extra"}},
	{"gateway close", []string{"gateway", "close", "--url", "https://gateway.example", "extra"}},
}

// TestEightFormsRefuseAnArgumentTooMany gives each of the eight forms an
// argument it does not read. Each names itself, exits 2 and does none of its
// work: it writes no file, and reaches for no variable, database or gateway.
// Without the argument each goes on to its work. report and follow-up still
// ignore an argument, so a job that passes one keeps running, and their help
// says so.
func TestEightFormsRefuseAnArgumentTooMany(t *testing.T) {
	clearCI(t)
	dir := t.TempDir()
	t.Chdir(dir)
	for _, variable := range []string{"COSTROID_DATABASE_URL", "COSTROID_MIGRATION_DATABASE_URL", sealingKeyVariable, publicKeyVariable, adminKeyVariable, gatewayURLVar} {
		t.Setenv(variable, "")
	}
	for _, refused := range refusedForms {
		command, _, _ := strings.Cut(refused.form, " ")
		want := "costroid: " + refused.form + " takes no argument\n" + seeUsage(command)
		if code, stdout, stderr := execute(t, refused.args...); code != exitUsage || stdout != "" || stderr != want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, want %q", refused.form, code, stdout, stderr, want)
		}
	}
	if left := tree(t, dir); len(left) != 0 {
		t.Errorf("a form that refused its command line wrote %v", left)
	}
	t.Chdir(t.TempDir())
	for _, refused := range refusedForms {
		right := refused.args[:len(refused.args)-1]
		if code, _, stderr := execute(t, right...); code == exitUsage || strings.Contains(stderr, "takes no argument") {
			t.Errorf("%s: without the argument it is still a wrong command line: exit %d, stderr %q", refused.form, code, stderr)
		}
	}
	rows := recordedRows
	for _, name := range []string{"report", "follow-up"} {
		code, stdout, stderr := execute(t, name, "--rows", rows)
		ignoredCode, ignoredStdout, ignoredStderr := execute(t, name, "--rows", rows, "extra")
		if ignoredCode != code || ignoredStdout != stdout || ignoredStderr != stderr || code == exitUsage {
			t.Errorf("%s reads an argument: exit %d, stderr %q, and without it exit %d, stderr %q", name, ignoredCode, ignoredStderr, code, stderr)
		}
		entry, _ := lookup(name)
		if want := name + " reads no argument, and ignores one it is given"; !strings.Contains(strings.Join(strings.Fields(entry.help), " "), want) {
			t.Errorf("help %s does not say %q", name, want)
		}
	}
}

// TestServesHelpIsOnStdoutWhereverItIsAsked asks serve for its help first on
// the command line, after other flags, under --quiet, and through costroid
// help. Each prints the same page on stdout, says nothing on stderr, exits 0,
// and starts no service: the variables a service needs are unset.
func TestServesHelpIsOnStdoutWhereverItIsAsked(t *testing.T) {
	for _, variable := range []string{"COSTROID_DATABASE_URL", "COSTROID_MIGRATION_DATABASE_URL", "COSTROID_OIDC_AUDIENCE", "COSTROID_PSEUDONYM_KEY"} {
		t.Setenv(variable, "")
	}
	for _, args := range [][]string{
		{"help", "serve"},
		{"serve", "--help"},
		{"serve", "-h"},
		{"serve", "--addr", ":0", "--help"},
		{"serve", "--addr", ":0", "--bind", "github.com:example", "-h"},
		{"serve", "--quiet", "--egress-allow", "gitlab.example.com", "--help"},
	} {
		if code, stdout, stderr := execute(t, args...); code != 0 || stdout != server.ServeUsage || stderr != "" {
			t.Errorf("costroid %s: exit %d, stderr %q, and stdout does not hold the page alone:\n%s", strings.Join(args, " "), code, stderr, stdout)
		}
	}
}

// TestVersionVerboseStatesNoSecretAndNoOutsidePath runs version in a
// directory under a home of its own, with keys and tokens in the
// environment. The plain form prints one line, as it did. --verbose adds
// four: the commit, the system, the result versions written and whether
// costroid.yml is here. --json holds the same facts. None holds a value of
// the environment or a path.
func TestVersionVerboseStatesNoSecretAndNoOutsidePath(t *testing.T) {
	previous := [2]string{version, commit}
	t.Cleanup(func() { version, commit = previous[0], previous[1] })
	home := t.TempDir()
	here := filepath.Join(home, "work", "repository")
	if err := os.MkdirAll(here, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(here)
	secrets := map[string]string{
		"HOME": home, "ANTHROPIC_API_KEY": "sk-ant-api03-not-a-real-key", "OPENAI_API_KEY": "sk-proj-not-a-real-key",
		"GITHUB_TOKEN": "ghp_notARealToken0123456789", platformTokenVariable: "costroid_it_notARealToken", "COSTROID_PLATFORM_URL": "https://platform.example",
		billingAccountVariable: "acct_billing_example", credentialVariable: "apikey_01Example", "SOME_OTHER_VARIABLE": "a-value-of-the-environment",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	for _, build := range []struct {
		release, commit, wantCommit string
		config                      bool
	}{
		{testRelease, testCommit, testCommit, false},
		{"dev", "", "not recorded", true},
	} {
		version, commit = build.release, build.commit
		found := "none in this directory"
		if build.config {
			writeFile(t, configFile, "policy:\n  run_budget_usd: \"5\"\n")
			found = "found in this directory"
		}
		code, plain, stderr := execute(t, "version")
		if code != 0 || stderr != "" || !regexp.MustCompile(`^costroid `+regexp.QuoteMeta(build.release)+`, catalog [0-9.]+, prices read \d{4}-\d{2}-\d{2}\n$`).MatchString(plain) {
			t.Fatalf("version: exit %d, stderr %q, stdout %q", code, stderr, plain)
		}
		if _, flagged, _ := execute(t, "--version"); flagged != plain {
			t.Errorf("--version prints %q, and version %q", flagged, plain)
		}
		code, verbose, stderr := execute(t, "version", "--verbose")
		want := plain + "commit: " + build.wantCommit + "\nsystem: linux/"
		if code != 0 || stderr != "" || !strings.HasPrefix(verbose, want[:len(want)-len("linux/")]) || strings.Count(verbose, "\n") != 5 {
			t.Errorf("--verbose: exit %d, stderr %q, stdout:\n%s", code, stderr, verbose)
		}
		lines := strings.Split(strings.TrimSuffix(verbose, "\n"), "\n")
		if !regexp.MustCompile(`^system: [a-z0-9]+/[a-z0-9]+$`).MatchString(lines[2]) ||
			!regexp.MustCompile(`^result versions written: \d+\.\d+(, \d+\.\d+)*$`).MatchString(lines[3]) || lines[4] != configFile+": "+found {
			t.Errorf("--verbose adds lines of another form:\n%s", verbose)
		}
		code, printed, stderr := execute(t, "version", "--json")
		object := oneObject(t, printed, versionSchema)
		if code != 0 || stderr != "" {
			t.Errorf("--json: exit %d, stderr %q", code, stderr)
		}
		keys := slices.Sorted(func(yield func(string) bool) {
			for key := range object {
				if !yield(key) {
					return
				}
			}
		})
		if want := []string{"arch", "catalog", "commit", "config", "os", "release", "result_schemas", "schema"}; !slices.Equal(keys, want) {
			t.Errorf("--json holds %q, want %q", keys, want)
		}
		config := object["config"].(map[string]any)
		wantCommit := any(nil)
		if build.commit != "" {
			wantCommit = build.commit
		}
		if object["release"] != build.release || object["commit"] != wantCommit || config["file"] != configFile || config["found"] != build.config ||
			lines[2] != fmt.Sprintf("system: %v/%v", object["os"], object["arch"]) {
			t.Errorf("--json and --verbose state different facts:\n%s%s", printed, verbose)
		}
		for name, output := range map[string]string{"version": plain, "version --verbose": verbose, "version --json": printed} {
			for _, pair := range os.Environ() {
				variable, value, _ := strings.Cut(pair, "=")
				if len(value) >= 8 && strings.Contains(output, value) {
					t.Errorf("%s prints the value of %s", name, variable)
				}
			}
			if strings.Contains(output, home) || regexp.MustCompile(`(^|[\s":])(/|~|[A-Za-z]:\\)`).MatchString(output) {
				t.Errorf("%s prints a path:\n%s", name, output)
			}
		}
	}
}

// runsMain makes the test binary run costroid's main, as the binary a
// release ships does, when a test starts it as a process of its own.
const runsMain = "COSTROID_TEST_RUNS_MAIN"

// alive says whether a process is still there.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestRunEndsAtOnceOnAnInterruptAndLeavesNoChild starts costroid run as a
// process of its own, with an agent that starts a process and waits, and
// interrupts costroid alone, as a job's runner does. run says it is
// stopping the agent, stops the agent and what it started, and exits 130
// with the rows file written. An agent that ignores SIGTERM is killed when
// its grace has passed, and run still exits 130 and leaves nothing running.
func TestRunEndsAtOnceOnAnInterruptAndLeavesNoChild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agents := []struct {
		name, script string
		within       time.Duration
	}{
		{"an agent that ends on SIGTERM", "#!/bin/sh\nsleep 300 &\necho \"$$ $!\" > \"$COSTROID_TEST_PIDS\"\nwait\n", 3 * time.Second},
	}
	if !testing.Short() {
		agents = append(agents, struct {
			name, script string
			within       time.Duration
		}{"an agent that ignores SIGTERM", "#!/bin/sh\ntrap '' TERM\nsleep 300 &\necho \"$$ $!\" > \"$COSTROID_TEST_PIDS\"\nwhile :; do sleep 1; done\n", agentGrace + recordWait})
	}
	for _, agent := range agents {
		t.Run(agent.name, func(t *testing.T) {
			dir := t.TempDir()
			claude := filepath.Join(dir, "claude")
			writeFile(t, claude, agent.script)
			if err := os.Chmod(claude, 0o755); err != nil {
				t.Fatal(err)
			}
			pids, rows := filepath.Join(dir, "pids"), filepath.Join(dir, "rows.jsonl")
			command := exec.Command(self, "run", "--budget-usd", "5", "--out", rows, "--", claude, "-p", "work")
			command.Dir = dir
			command.Env = append(os.Environ(), runsMain+"=1", "COSTROID_TEST_PIDS="+pids, "CLAUDE_CONFIG_DIR="+t.TempDir(), "GITHUB_ACTIONS=", "GITLAB_CI=")
			var stderr bytes.Buffer
			command.Stdout, command.Stderr = io.Discard, &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = command.Process.Kill() })
			var started []int
			for deadline := time.Now().Add(20 * time.Second); len(started) != 2; time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("the agent did not start:\n%s", stderr.String())
				}
				for _, field := range strings.Fields(readIfThere(pids)) {
					if pid, err := strconv.Atoi(field); err == nil {
						started = append(started, pid)
					}
				}
				if len(started) != 2 {
					started = nil
				}
			}
			t.Cleanup(func() {
				for _, pid := range started {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			interrupted := time.Now()
			if err := command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			ended := make(chan error, 1)
			go func() { ended <- command.Wait() }()
			select {
			case <-ended:
			case <-time.After(agent.within + 5*time.Second):
				t.Fatalf("run was still there %s after the interrupt:\n%s", time.Since(interrupted), stderr.String())
			}
			if took := time.Since(interrupted); command.ProcessState.ExitCode() != cli.ExitInterrupted || took > agent.within {
				t.Errorf("run exited %d, %s after the interrupt, want %d within %s:\n%s", command.ProcessState.ExitCode(), took.Round(time.Millisecond), cli.ExitInterrupted, agent.within, stderr.String())
			}
			if !strings.Contains(stderr.String(), "costroid: interrupted, stopping claude\n") {
				t.Errorf("run did not say it was stopping the agent:\n%s", stderr.String())
			}
			for deadline := time.Now().Add(2 * time.Second); alive(started[0]) || alive(started[1]); time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("run left a process running: the agent %d is there: %t, what it started, %d: %t", started[0], alive(started[0]), started[1], alive(started[1]))
				}
			}
			if _, err := os.Stat(rows); err != nil {
				t.Errorf("run did not leave its rows file: %v", err)
			}
		})
	}
}

// readIfThere reads a file that may not be written yet.
func readIfThere(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// countLines are the only lines that open with costroid's name on stdout:
// the count a report of findings or checks ends on.
var countLines = []string{
	"costroid: %d files, %d rejected lines\n",
	"costroid: %d %s, %d with an agent: %d high, %d medium, %d %s\n",
	"costroid: doctor: %d fail, %d warn, %d ok\n",
}

// TestAResultIsOnStdoutAndAMessageOnStderr holds the two streams apart.
// Stdout holds what a command was asked to print: findings, checks, a
// verdict, a comment, a patch, a file, an object. Stderr holds what costroid
// says about its work: what it wrote, sent or stored, and what to run next.
// A command asked to do something and not to print leaves stdout empty. In
// the source, no line that opens with costroid's name goes to stdout but the
// three counts.
func TestAResultIsOnStdoutAndAMessageOnStderr(t *testing.T) {
	clearCI(t)
	rows, execution := recordedRows, recordedExecute
	claude := replayClaude(t)
	t.Chdir(lintWorkflows(t))
	writeFile(t, configFile, "policy:\n  run_budget_usd: \"5\"\n")
	writeFile(t, "bad.yml", rejectedWorkflow)
	type streams struct{ stdout, stderr string }
	ran := func(args ...string) streams {
		t.Helper()
		_, stdout, stderr := execute(t, args...)
		return streams{stdout, stderr}
	}
	said := regexp.MustCompile(`(?m)^(costroid: |Next: ).*$`)
	count := regexp.MustCompile(`^costroid: (\d+ files?, |doctor: )`)
	results := []struct {
		name string
		got  streams
		// stdout opens the result. stderr is what the command says beside
		// it, whole, or how that opens when it ends in three dots.
		stdout, stderr string
	}{
		{"lint", ran("lint"), ".github/workflows/mention.yml:13: high: ", ""},
		{"lint-recipes", ran("lint-recipes", "bad.yml"), "bad.yml:1: pull-request-target: ", ""},
		{"demo", ran("demo"), "Within budget, by the job's own figures: ", "\n" + nextAfterDemo + "\n"},
		{"demo --markdown", ran("demo", "--markdown"), comment.Marker, ""},
		{"validate", ran("validate"), configFile + ": valid\n", nextAfterValidate + "\n"},
		{"version", ran("version"), "costroid " + version + ", catalog ", ""},
		{"policy", ran("policy", "--rows", rows), "", ""},
		{"report --preview", ran("report", "--preview", "--rows", rows), comment.Marker, "Within budget..."},
		{"export --format", ran("export", "--rows", requestRows, "--format", "focus-csv"), "", ""},
		{"run", ran("run", "--budget-usd", "5", "--out", "run.jsonl", "--", claude, "-p", "work"), "{", "costroid: running claude-code under a cap of USD 5.00\ncostroid: claude-code, ..."},
		{"run --json", ran("run", "--budget-usd", "5", "--out", "object.jsonl", "--json", "--", claude, "-p", "work"), "{\n  \"schema\": \"costroid.result/", "costroid: running claude-code under a cap of USD 5.00\n{..."},
	}
	for _, result := range results {
		if !strings.HasPrefix(result.got.stdout, result.stdout) || result.got.stdout == "" {
			t.Errorf("%s: stdout does not open with its result %q:\n%s", result.name, result.stdout, result.got.stdout)
		}
		if opens, open := strings.CutSuffix(result.stderr, "..."); open && !strings.HasPrefix(result.got.stderr, opens) || !open && result.got.stderr != result.stderr {
			t.Errorf("%s: stderr %q, want %q", result.name, result.got.stderr, result.stderr)
		}
		for _, line := range said.FindAllString(result.got.stdout, -1) {
			if !count.MatchString(line) {
				t.Errorf("%s: stdout holds a line costroid says about its work: %q", result.name, line)
			}
		}
	}
	root := gitRepository(t)
	t.Chdir(root)
	writeFile(t, "review.yml", existingReview)
	doing := []struct {
		name string
		got  streams
		said string
	}{
		{"init", ran("init", "github", "--agent", "claude", "--action-ref", testCommit), "costroid: wrote " + initOut[githubHost] + " from recipes/"},
		{"ingest-run", ran("ingest-run", "--execution-file", execution, "--out", "ingested.jsonl"), "costroid: claude-code, "},
		{"export --format --out", ran("export", "--rows", requestRows, "--format", "focus-csv", "--out", "costs.csv"), "costroid: wrote 11 rows as focus-csv to costs.csv\n"},
	}
	for _, did := range doing {
		if did.got.stdout != "" || !strings.HasPrefix(did.got.stderr, did.said) {
			t.Errorf("%s: stdout %q, and stderr does not open with what it did, %q:\n%s", did.name, did.got.stdout, did.said, did.got.stderr)
		}
	}
	patch := ran("init", "github", "--agent", "claude-code-action", "--action-ref", testCommit, "--existing", "review.yml")
	if !strings.HasPrefix(patch.stdout, "--- a/review.yml\n") || said.MatchString(patch.stdout) || !strings.HasPrefix(patch.stderr, "costroid: printed a patch for review.yml") {
		t.Errorf("init --existing: stdout is not the patch alone, or stderr does not say so:\n%s%s", patch.stdout, patch.stderr)
	}
	onAReceiptMachine(t)
	if printed := ran("receipt"); !strings.HasPrefix(printed.stdout, receipts.Marker+"\n") || said.MatchString(printed.stdout) ||
		!strings.HasPrefix(printed.stderr, "costroid: counted 1 request of 1 Claude Code session on branch "+receiptBranch) {
		t.Errorf("receipt: stdout is not the comment's body alone, or stderr does not say what it counted:\n%s%s", printed.stdout, printed.stderr)
	}
	seen := 0
	printCalls(t, func(function, writer, text string) {
		if !strings.HasPrefix(function, "fmt.Fprint") || writer != "stdout" {
			return
		}
		seen++
		if (strings.HasPrefix(text, "costroid: ") || strings.Contains(text, "Next:")) && !slices.Contains(countLines, text) {
			t.Errorf("the source prints on stdout a line costroid says about its work: %q", text)
		}
	})
	if seen < 10 {
		t.Fatalf("read %d lines the source prints on stdout, too few to be all of them", seen)
	}
}

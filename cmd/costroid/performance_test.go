package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// performancePage states the performance bars and the readings beside them.
var performancePage = filepath.Join(repositoryRoot, "docs", "performance.md")

// performanceBars says where the code holds each bar the performance page
// states: the constant a run of the bar's make target fails at, in the file
// that holds it.
var performanceBars = []struct {
	opens, file, constant string
}{
	{"`costroid --version` exits in under ", "start_time_test.go", "versionBar"},
	{"`costroid policy` exits in under ", "start_time_test.go", "policyBar"},
	{"The per-PR view, `GET /v1/pulls/{pr}`, answers in under ", filepath.Join(repositoryRoot, "internal", "server", "view_bench_test.go"), "benchTarget"},
}

var (
	barCount   = regexp.MustCompile("Costroid states ([a-z]+) performance bars")
	barFigure  = regexp.MustCompile(`in under ([0-9]+) (ms|seconds?) `)
	barTarget  = regexp.MustCompile("^`make ([a-z-]+)`$")
	barJob     = regexp.MustCompile("^the `([a-z-]+)` job$")
	readingsOn = regexp.MustCompile("The readings below were taken on [0-9]{4}-[0-9]{2}-[0-9]{2}, of the code at commit `[0-9a-f]{7,40}`, on an? [^.]+ with [0-9]+ cores")
	// benchRecipe reads a bench target of the Makefile: its name, the -run or
	// -bench pattern of the functions it runs, and their package.
	benchRecipe = regexp.MustCompile(`(?m)^(bench-[a-z-]+):.*\n\t.*go test .*-(?:run|bench) '(\^[^']+\$\$)' .*(\./[a-z/]+)$`)
)

// TestEveryPerformanceFigureIsABarAJobHoldsOrADatedReading holds the
// performance page to what holds its figures. Each bar in its table names the
// make target that measures it and the CI job that runs the target, and its
// figure is the one the code fails at. Every other table of figures is a
// reading: the page dates its readings, names the commit and the machine, and
// each table follows the line that says it is one. No figure is a floor
// outside the table, and no benchmark a make target runs fails at a figure
// the page does not state as a bar.
func TestEveryPerformanceFigureIsABarAJobHoldsOrADatedReading(t *testing.T) {
	page := readFile(t, performancePage)
	bars, readings := performanceTables(page)
	words := []string{"one", "two", "three", "four", "five", "six"}
	stated := barCount.FindStringSubmatch(page)
	if stated == nil || slices.Index(words, stated[1])+1 != len(bars) {
		t.Errorf("the page states %q bars, and its table holds %d", stated, len(bars))
	}
	if len(bars) != len(performanceBars) {
		t.Fatalf("the page states %d bars, and the code holds %d", len(bars), len(performanceBars))
	}
	jobs := ciWorkflowJobs(t)
	makefile := readFile(t, filepath.Join(repositoryRoot, "Makefile"))
	for i, bar := range bars {
		held := performanceBars[i]
		if len(bar) != 3 || !strings.HasPrefix(bar[0], held.opens) {
			t.Errorf("bar %d is %q, and the code holds %q", i+1, bar, held.opens)
			continue
		}
		if stated, code := statedDuration(t, bar[0]), constantDuration(t, held.file, held.constant); stated != code {
			t.Errorf("the page states %q at %s, and %s fails at %s", bar[0], stated, held.constant, code)
		}
		target, job := barTarget.FindStringSubmatch(bar[1]), barJob.FindStringSubmatch(bar[2])
		if target == nil || job == nil {
			t.Errorf("%q names no make target or no CI job: %q, %q", bar[0], bar[1], bar[2])
			continue
		}
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target[1]) + `:`).MatchString(makefile) {
			t.Errorf("%q is measured by make %s, which the Makefile does not define", bar[0], target[1])
		}
		if !runsMake(jobs[job[1]], target[1]) {
			t.Errorf("%q is held in CI by the %s job, which does not run make %s", bar[0], job[1], target[1])
		}
	}
	if !readingsOn.MatchString(page) {
		t.Error("the page does not date its readings, with the commit and the machine they were taken of")
	}
	for _, reading := range readings {
		if !strings.HasPrefix(reading, "Last local reading") {
			t.Errorf("a table of figures follows %q, which does not say it is a reading", reading)
		}
	}
	if strings.Contains(strings.ToLower(page), "floor") {
		t.Error("the page states a floor, and a figure no job holds is a reading")
	}
	barred := map[string]bool{}
	for _, bar := range bars {
		if target := barTarget.FindStringSubmatch(bar[1]); target != nil {
			barred[target[1]] = true
		}
	}
	for _, recipe := range benchRecipe.FindAllStringSubmatch(makefile, -1) {
		if barred[recipe[1]] {
			continue
		}
		pattern := regexp.MustCompile(strings.ReplaceAll(recipe[2], "$$", "$"))
		for _, function := range figureFailures(t, filepath.Join(repositoryRoot, recipe[3]), pattern) {
			t.Errorf("make %s runs %s, which fails when a figure crosses a line, and the page states no bar for it", recipe[1], function)
		}
	}
}

// figureFailures names each test or benchmark function of the package in dir
// that pattern matches and that fails the run where a comparison of order
// holds: a floor or a ceiling on a figure it measured.
func figureFailures(t *testing.T, dir string, pattern *regexp.Regexp) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var failing []string
	for _, name := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && pattern.MatchString(function.Name.Name) && failsOnAFigure(function.Body) {
				failing = append(failing, function.Name.Name)
			}
		}
	}
	return failing
}

// failsOnAFigure reports an if statement in body whose condition orders two
// values and whose branch fails the run.
func failsOnAFigure(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok {
			return !found
		}
		if comparison, ok := statement.Cond.(*ast.BinaryExpr); ok && slices.Contains([]token.Token{token.LSS, token.GTR, token.LEQ, token.GEQ}, comparison.Op) {
			ast.Inspect(statement.Body, func(inner ast.Node) bool {
				if call, ok := inner.(*ast.CallExpr); ok {
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok && slices.Contains([]string{"Fatal", "Fatalf", "Error", "Errorf", "FailNow", "Fail"}, selector.Sel.Name) {
						found = true
					}
				}
				return !found
			})
		}
		return !found
	})
	return found
}

// performanceTables reads the page's bar table, a row of cells each, and for
// every other table the line of prose above it.
func performanceTables(page string) (bars [][]string, readings []string) {
	lines := strings.Split(page, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "|") {
			continue
		}
		start := i
		for i < len(lines) && strings.HasPrefix(lines[i], "|") {
			i++
		}
		if strings.HasPrefix(lines[start], "| Bar |") {
			for _, row := range lines[start+2 : i] {
				bars = append(bars, strings.Split(strings.Trim(row, "| "), " | "))
			}
			continue
		}
		above := start - 1
		for above > 0 && lines[above] == "" {
			above--
		}
		readings = append(readings, lines[above])
	}
	return bars, readings
}

// statedDuration reads the figure a bar states, such as "in under 100 ms ".
func statedDuration(t *testing.T, bar string) time.Duration {
	t.Helper()
	figure := barFigure.FindStringSubmatch(bar)
	if figure == nil {
		t.Fatalf("%q states no figure in ms or seconds", bar)
	}
	amount, err := strconv.Atoi(figure[1])
	if err != nil {
		t.Fatal(err)
	}
	if figure[2] == "ms" {
		return time.Duration(amount) * time.Millisecond
	}
	return time.Duration(amount) * time.Second
}

// constantDuration reads a duration constant from the Go source it is
// declared in, written as time.Unit or a whole number times time.Unit.
func constantDuration(t *testing.T, file, name string) time.Duration {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]time.Duration{"Millisecond": time.Millisecond, "Second": time.Second}
	unit := func(expression ast.Expr) time.Duration {
		if selector, ok := expression.(*ast.SelectorExpr); ok {
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "time" {
				return units[selector.Sel.Name]
			}
		}
		return 0
	}
	var value ast.Expr
	ast.Inspect(parsed, func(node ast.Node) bool {
		if spec, ok := node.(*ast.ValueSpec); ok {
			for i, ident := range spec.Names {
				if ident.Name == name && i < len(spec.Values) {
					value = spec.Values[i]
				}
			}
		}
		return value == nil
	})
	if product, ok := value.(*ast.BinaryExpr); ok && product.Op == token.MUL {
		if number, ok := product.X.(*ast.BasicLit); ok && number.Kind == token.INT {
			if amount, err := strconv.Atoi(number.Value); err == nil && unit(product.Y) != 0 {
				return time.Duration(amount) * unit(product.Y)
			}
		}
	}
	if duration := unit(value); duration != 0 {
		return duration
	}
	t.Fatalf("%s declares no constant %s of the form N * time.Unit", file, name)
	return 0
}

type ciWorkflowJob struct {
	Steps []ciWorkflowStep `yaml:"steps"`
}

type ciWorkflowStep struct {
	Run string `yaml:"run"`
}

// ciWorkflowJobs reads the jobs of the CI workflow by name.
func ciWorkflowJobs(t *testing.T) map[string]ciWorkflowJob {
	t.Helper()
	var workflow struct {
		Jobs map[string]ciWorkflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(repositoryRoot, ".github", "workflows", "ci.yml"))), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

// runsMake reports a job with a step that runs the make target, under any
// command that runs it, such as taskset.
func runsMake(job ciWorkflowJob, target string) bool {
	runs := regexp.MustCompile(`(^|\s)make ` + regexp.QuoteMeta(target) + `(\s|$)`)
	return slices.ContainsFunc(job.Steps, func(step ciWorkflowStep) bool { return runs.MatchString(step.Run) })
}

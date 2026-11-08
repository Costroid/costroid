package recipelint

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/decimal"
)

// RuleTwoCaps names steps that state different caps for one run. The run is
// held to one, the read step grades it against the one it is told, and the
// report states the one the run recorded, so a second figure makes one of
// them read another cap than the run had.
const RuleTwoCaps = "two-caps"

var (
	// capWord finds a cap flag and its figure: one expression, a quoted word
	// or a bare one. --budget-usd is Costroid's, --max-budget-usd Claude Code's.
	capWord = regexp.MustCompile(`(?:^|\s)--(?:max-)?budget-usd(?:=|\s+)(\$\{\{[^}]*\}\}|"[^"]*"|'[^']*'|\S+)`)
	// envExpression is ${{ env.NAME }}, which the workflow's env can resolve.
	envExpression = regexp.MustCompile(`\$\{\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	// shellVariable is $NAME or ${NAME} in a script.
	shellVariable = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)
)

// statedCap is one figure a file states as a run's cap, read as an amount.
type statedCap struct {
	node   *yaml.Node
	where  string
	text   string
	amount decimal.Decimal
}

// twoCaps compares the caps a workflow states, among jobs that needs links,
// so two agents that never share a run may hold different caps. A severity
// ranks the finding under the customer profile; the recipe profile gives
// none.
func (l *linter) twoCaps(root *yaml.Node, severity Severity) {
	jobs := entries(field(root, "jobs"))
	groups := linkedJobs(jobs)
	stated := map[string][]statedCap{}
	for _, job := range jobs {
		group := groups[job.key.Value]
		stated[group] = append(stated[group], jobCaps(root, job)...)
	}
	for _, job := range jobs {
		if group := groups[job.key.Value]; group == job.key.Value {
			l.differentCaps(stated[group], severity)
		}
	}
}

// gitlabCaps compares the caps every job of a GitLab template states.
func (l *linter) gitlabCaps(root *yaml.Node) {
	var stated []statedCap
	for _, item := range entries(root) {
		if gitLabReserved[item.key.Value] || strings.HasPrefix(item.key.Value, ".") {
			continue
		}
		members := chain(root, item.value)
		variables := mergeVariables(variableLayers(root, members)...)
		for _, script := range []*yaml.Node{firstField(members, "before_script"), firstField(members, "script")} {
			for _, line := range sequence(script) {
				stated = appendScript(stated, line, "the script", variables)
			}
		}
	}
	l.differentCaps(stated, "")
}

// differentCaps finds where each figure other than the first cap stated is
// first stated.
func (l *linter) differentCaps(stated []statedCap, severity Severity) {
	slices.SortStableFunc(stated, func(a, b statedCap) int { return cmp.Compare(a.node.Line, b.node.Line) })
	var seen []statedCap
	for _, other := range stated {
		if slices.ContainsFunc(seen, func(held statedCap) bool { return held.amount.Cmp(other.amount) == 0 }) {
			continue
		}
		seen = append(seen, other)
		if len(seen) == 1 {
			continue
		}
		first := seen[0]
		const format = "%s states a cap of USD %s, and %s at line %d states USD %s: state one cap, so the run, its grade and the report hold the same one"
		args := []any{other.where, other.text, first.where, first.node.Line, first.text}
		if severity == "" {
			l.add(other.node, RuleTwoCaps, format, args...)
		} else {
			l.flag(other.node, severity, RuleTwoCaps, format, args...)
		}
	}
}

// linkedJobs names, for each job, one job of the set that needs links it to.
func linkedJobs(jobs []entry) map[string]string {
	parent := map[string]string{}
	for _, job := range jobs {
		parent[job.key.Value] = job.key.Value
	}
	find := func(name string) string {
		for parent[name] != name {
			name = parent[name]
		}
		return name
	}
	for _, job := range jobs {
		for _, need := range sequence(field(job.value, "needs")) {
			if _, known := parent[need.Value]; known {
				parent[find(need.Value)] = find(job.key.Value)
			}
		}
	}
	groups := map[string]string{}
	for _, job := range jobs {
		groups[job.key.Value] = find(job.key.Value)
	}
	return groups
}

// jobCaps lists the caps a job's steps state: budget-usd and claude-args on
// Costroid's action, claude_args on claude-code-action, and a cap flag in a
// script.
func jobCaps(root *yaml.Node, job entry) []statedCap {
	var stated []statedCap
	for _, step := range sequence(field(job.value, "steps")) {
		env := mergeVariables(field(root, "env"), field(job.value, "env"), field(step, "env"))
		with := field(step, "with")
		if costroidStage(step) != "" {
			if budget := field(with, "budget-usd"); budget != nil {
				stated = appendCap(stated, budget, "budget-usd", budget.Value, env, false)
			}
			stated = appendArgs(stated, field(with, "claude-args"), "claude-args", env)
		}
		if agentAction(step) == claudeCodeAction {
			stated = appendArgs(stated, field(with, "claude_args"), "claude_args", env)
		}
		stated = appendScript(stated, field(step, "run"), "the script", env)
	}
	return stated
}

// appendArgs adds the cap claude_args or claude-args hold: the last
// --max-budget-usd, the one Claude Code keeps.
func appendArgs(stated []statedCap, node *yaml.Node, where string, env map[string]string) []statedCap {
	figures := capFigures(node)
	if len(figures) == 0 {
		return stated
	}
	return appendCap(stated, node, where, figures[len(figures)-1], env, false)
}

// appendScript adds each cap flag in a script.
func appendScript(stated []statedCap, node *yaml.Node, where string, env map[string]string) []statedCap {
	for _, figure := range capFigures(node) {
		stated = appendCap(stated, node, where, figure, env, true)
	}
	return stated
}

// capFigures lists the figure of each cap flag in a value. A line that starts
// with # is a comment, as claude-code-action reads claude_args and as a shell
// reads a script.
func capFigures(node *yaml.Node) []string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return nil
	}
	var figures []string
	for line := range strings.SplitSeq(node.Value, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, match := range capWord.FindAllStringSubmatch(line, -1) {
			figures = append(figures, match[1])
		}
	}
	return figures
}

// appendCap adds a figure when it reads as an amount. A figure that names a
// variable the file does not set, such as ${{ vars.CAP }}, is not read.
func appendCap(stated []statedCap, node *yaml.Node, where, raw string, env map[string]string, shell bool) []statedCap {
	text, known := resolveCap(raw, env, shell)
	if !known {
		return stated
	}
	amount, err := decimal.Parse(text)
	if err != nil {
		return stated
	}
	return append(stated, statedCap{node: node, where: where, text: text, amount: amount})
}

// resolveCap reads a figure with its quotes taken off, a script's variables
// and then the env context's expressions put in from env.
func resolveCap(raw string, env map[string]string, shell bool) (string, bool) {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	known := true
	put := func(pattern *regexp.Regexp) {
		value = pattern.ReplaceAllStringFunc(value, func(reference string) string {
			match := pattern.FindStringSubmatch(reference)
			setting, set := env[cmp.Or(match[1:]...)]
			known = known && set
			return setting
		})
	}
	if shell {
		put(shellVariable)
	}
	put(envExpression)
	value = strings.TrimSpace(value)
	return value, known && value != "" && !strings.Contains(value, "$")
}

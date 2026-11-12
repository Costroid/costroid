package recipelint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// estimateCSV is a FOCUS cost file an estimator step wrote. Every name and
// figure is synthetic.
const estimateCSV = "BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName\n" +
	"EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2\n"

// costroidOnPath builds the binary from this checkout into the shell's PATH,
// so the action's script runs the command it will run on a runner.
func costroidOnPath(t *testing.T, s *shell) {
	t.Helper()
	build := exec.Command("go", "build", "-o", filepath.Join(s.bin, "costroid"), "../../cmd/costroid")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build costroid: %v: %s", err, output)
	}
}

// onRunner reads a value of action.yml as the runner gives it, with runner.temp
// in root.
func onRunner(value, root string) string {
	return strings.ReplaceAll(value, "${{ runner.temp }}", root)
}

// estimateStep is the action's step that adds the estimate, with the
// environment it gives its script, read on a runner whose temp is root.
func estimateStep(t *testing.T, root string, inputs map[string]string) []string {
	t.Helper()
	steps := actionSteps(t)
	add, keep := steps["Add the infrastructure estimate"], steps["Keep the rows"]
	if add.node == nil || keep.node == nil {
		t.Fatal("action.yml has no step that adds the estimate, or none that keeps the rows")
	}
	if add.index > keep.index || add.index < steps["Run the agent"].index || add.index < steps["Read the agent's run"].index {
		t.Errorf("the estimate is added at step %d, and the rows are kept at step %d", add.index, keep.index)
	}
	condition := scalarField(add.node, "if")
	for _, want := range []string{"always()", "inputs.stage == 'run'", "inputs.stage == 'read'", "inputs.stage == 'all'", "inputs.infrastructure-file != ''"} {
		if !strings.Contains(condition, want) {
			t.Errorf("the step's if lacks %s: %s", want, condition)
		}
	}
	if run := scalarField(add.node, "run"); run != `bash "$GITHUB_ACTION_PATH/add-infrastructure.sh"` {
		t.Errorf("the step runs %q", run)
	}
	var env []string
	for _, item := range entries(field(add.node, "env")) {
		value := onRunner(item.value.Value, root)
		for name, given := range inputs {
			value = strings.ReplaceAll(value, "${{ inputs."+name+" }}", given)
		}
		env = append(env, item.key.Value+"="+value)
	}
	return env
}

// keptFiles are the files the step that keeps the rows uploads, on a runner
// whose temp is root.
func keptFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for line := range strings.Lines(scalarField(field(actionSteps(t)["Keep the rows"].node, "with"), "path")) {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "${{ runner.temp }}/") {
			files = append(files, onRunner(line, root))
		}
	}
	return files
}

func TestActionCarriesTheInfrastructureFileIntoTheRows(t *testing.T) {
	s := newShell(t)
	costroidOnPath(t, s)
	agentRows, err := os.ReadFile("../comment/testdata/rows.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rows := filepath.Join(s.root, "costroid-rows.jsonl")
	write(t, rows, string(agentRows))
	estimate := filepath.Join(s.root, "infrastructure.csv")
	write(t, estimate, estimateCSV)

	env := estimateStep(t, s.root, map[string]string{"infrastructure-file": estimate, "infrastructure-source": "infrastructure", "infrastructure-optional": "false"})
	if code, output := s.run("add-infrastructure.sh", env...); code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	kept := keptFiles(t, s.root)
	if len(kept) == 0 || kept[0] != rows {
		t.Fatalf("the action keeps %v, not the rows file first", kept)
	}
	agent, estimated := 0, 0
	for line := range strings.Lines(read(t, rows)) {
		var row struct {
			Tags map[string]string `json:"Tags"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		switch {
		case row.Tags["costroid/agent"] != "":
			agent++
		case row.Tags["costroid/cost_basis"] == "estimate" && row.Tags["costroid/cost_source"] == "infrastructure":
			estimated++
		}
	}
	if agent != strings.Count(string(agentRows), "\n") || estimated != 1 {
		t.Errorf("the kept rows hold %d agent rows and %d estimate rows", agent, estimated)
	}

	gone := filepath.Join(s.root, "not-written.csv")
	env = estimateStep(t, s.root, map[string]string{"infrastructure-file": gone, "infrastructure-source": "infrastructure", "infrastructure-optional": "true"})
	code, output := s.run("add-infrastructure.sh", env...)
	if code != 0 || !strings.Contains(output, "the infrastructure estimate is not available") {
		t.Fatalf("an optional file that is not there: exit %d: %s", code, output)
	}
	note := filepath.Join(s.root, "costroid-rows.infrastructure.json")
	if !strings.Contains(read(t, note), `"reason":"no_file"`) {
		t.Errorf("the note reads %s", read(t, note))
	}
	found := false
	for _, file := range kept {
		found = found || file == note
	}
	if !found {
		t.Errorf("the action keeps %v, not the note", kept)
	}

	env = estimateStep(t, s.root, map[string]string{"infrastructure-file": gone, "infrastructure-source": "infrastructure", "infrastructure-optional": "yes"})
	if code, output := s.run("add-infrastructure.sh", env...); code != 125 || !strings.Contains(output, "infrastructure-optional must be true or false") {
		t.Errorf("infrastructure-optional yes: exit %d: %s", code, output)
	}
}

// TestEstimateInputsAreHeldToWhatTheStepDoes edits the estimate recipe's read
// step, and holds lint-recipes to a finding and costroid lint to a note for
// each input that would do nothing or fail the step.
func TestEstimateInputsAreHeldToWhatTheStepDoes(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, "github-claude-code-action-infrastructure.yml"))
	const file = "          infrastructure-file: ${{ runner.temp }}/infrastructure.csv\n"
	const optional = "          infrastructure-optional: \"true\"\n"
	edits := []edit{
		{"optional without a file", file, "", RuleInfrastructureFile},
		{"a source without a file", file + optional, "          infrastructure-source: hcp-terraform\n", RuleInfrastructureFile},
		{"a source the command refuses", optional, optional + "          infrastructure-source: HCP Terraform\n", RuleInfrastructureFile},
		{"optional neither true nor false", optional, "          infrastructure-optional: \"yes\"\n", RuleInfrastructureFile},
		{"a file on the report", "          stage: report\n", "          stage: report\n" + file, RuleInfrastructureFile},
	}
	for _, change := range edits {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
			edited := strings.Replace(source, change.from, change.to, 1)
			notes := LintCustomer("x.yml", []byte(edited)).Notes
			if !slices.ContainsFunc(notes, func(f Finding) bool { return f.Rule == RuleInfrastructureFile && f.Severity == Note }) {
				t.Errorf("costroid lint notes %v", notes)
			}
		})
	}
	for _, kept := range []string{"${{ inputs.source }}", "hcp-terraform"} {
		edited := strings.Replace(source, optional, optional+"          infrastructure-source: "+kept+"\n", 1)
		if found := LintBytes("x.yml", []byte(edited)).Findings; slices.ContainsFunc(found, func(f Finding) bool { return f.Rule == RuleInfrastructureFile }) {
			t.Errorf("infrastructure-source %s: %v", kept, found)
		}
	}
}

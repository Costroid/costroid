package recipelint

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	hardenRunnerUses = "      - uses: step-security/harden-runner@46b06cea07f9e64621fdf066ba77ac6915163d43 # v2.22.3\n"
	blockStep        = hardenRunnerUses + `        with:
          egress-policy: block
          disable-telemetry: true
          allowed-endpoints: >-
            api.anthropic.com:443
            github.com:443
`
	gatewayHost = "vars.COSTROID_GATEWAY_HOST"
)

// withFirstStep puts a step first in goodWorkflow's agent job, and returns the
// workflow and the line the step starts on.
func withFirstStep(step string) (string, int) {
	const firstStep = "    steps:\n      - uses: actions/checkout@"
	at := strings.Index(goodWorkflow, firstStep) + len("    steps:\n")
	return goodWorkflow[:at] + step + goodWorkflow[at:], strings.Count(goodWorkflow[:at], "\n") + 1
}

// Audit mode blocks no call, block mode with no list blocks the provider, and
// a list with a line break allows neither host beside the break. Each is a note
// that names its fix, and none rejects the file.
func TestAuditModeWarnsWithItsFix(t *testing.T) {
	for name, change := range map[string]struct {
		step   string
		offset int
		fix    string
	}{
		"audit":          {strings.Replace(blockStep, "egress-policy: block", "egress-policy: audit", 1), 2, "set egress-policy: block and list the hosts the job reaches in allowed-endpoints"},
		"block, no list": {hardenRunnerUses + "        with:\n          egress-policy: block\n          disable-telemetry: true\n", 2, "list the hosts the job reaches in allowed-endpoints"},
		"no inputs":      {hardenRunnerUses, 0, "list the hosts the job reaches in allowed-endpoints"},
		"empty list":     {strings.Replace(blockStep, "allowed-endpoints: >-\n            api.anthropic.com:443\n            github.com:443\n", "allowed-endpoints: \"\"\n", 1), 4, "list the hosts the job reaches in allowed-endpoints"},
		"line breaks":    {strings.Replace(blockStep, "allowed-endpoints: >-", "allowed-endpoints: |", 1), 4, "write the list as a folded block, allowed-endpoints: >-"},
	} {
		t.Run(name, func(t *testing.T) {
			source, line := withFirstStep(change.step)
			report := LintBytes("x.yml", []byte(source))
			if len(report.Findings) != 0 {
				t.Fatalf("rejected: %v", report.Findings)
			}
			if got := rules(report.Notes); !slices.Equal(got, []string{RuleEgressAudit}) {
				t.Fatalf("notes: %v", report.Notes)
			}
			note := report.Notes[0]
			if !strings.HasSuffix(note.Message, ": "+change.fix) {
				t.Errorf("the note does not end with its fix %q: %s", change.fix, note.Message)
			}
			if note.Line != line+change.offset {
				t.Errorf("the note names line %d, want %d", note.Line, line+change.offset)
			}
		})
	}
	source, _ := withFirstStep(blockStep)
	if report := LintBytes("x.yml", []byte(source)); len(report.Notes) != 0 || len(report.Findings) != 0 {
		t.Fatalf("block mode with a list: notes %v, findings %v", report.Notes, report.Findings)
	}
}

// agentJobs lists every job of the shipped GitHub recipes that starts an agent.
func agentJobs(t *testing.T) map[string]entry {
	t.Helper()
	jobs := map[string]entry{}
	for _, file := range recipeFiles(t) {
		documents, err := parse([]byte(read(t, file)))
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range entries(field(documents[0], "jobs")) {
			if slices.ContainsFunc(sequence(field(job.value, "steps")), func(step *yaml.Node) bool { return stepAgent(step) != "" }) {
				jobs[filepath.Base(file)+"/"+job.key.Value] = job
			}
		}
	}
	return jobs
}

// Every agent job in the shipped recipes starts with harden-runner in block
// mode, under a comment that says so, with telemetry off and a list it can
// read, and the shipped files draw no egress note.
func TestShippedRecipesBlockEgress(t *testing.T) {
	jobs := agentJobs(t)
	if len(jobs) != 5 {
		t.Fatalf("%d agent jobs in the shipped recipes, want 5: %v", len(jobs), slices.Sorted(maps.Keys(jobs)))
	}
	for name, job := range jobs {
		steps := sequence(field(job.value, "steps"))
		with := field(steps[0], "with")
		switch {
		case !isHardenRunner(field(steps[0], "uses")):
			t.Errorf("%s: the first step is not harden-runner", name)
		case scalarField(with, "egress-policy") != "block":
			t.Errorf("%s: egress-policy is %q", name, scalarField(with, "egress-policy"))
		case scalarField(with, "disable-telemetry") != "true":
			t.Errorf("%s: disable-telemetry is %q", name, scalarField(with, "disable-telemetry"))
		case len(allowedEndpoints(scalarField(with, "allowed-endpoints"))) == 0:
			t.Errorf("%s: no allowed-endpoints", name)
		}
		comment := steps[0].HeadComment + field(steps[0], "uses").HeadComment
		for _, want := range []string{"Block mode", "docs/recipes.md"} {
			if !strings.Contains(comment, want) {
				t.Errorf("%s: the comment above harden-runner does not say %q: %q", name, want, comment)
			}
		}
	}
	for _, file := range recipeFiles(t) {
		if strings.Contains(read(t, file), "egress-policy: audit") {
			t.Errorf("%s holds a step in audit mode", file)
		}
	}
	shipped, err := Lint([]string{recipesDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range shipped.Notes {
		if note.Rule == RuleEgressAudit || note.Rule == RuleEgressEndpoint {
			t.Errorf("shipped recipe: %s", note)
		}
	}
}

// reaches derives, from a job's own steps, the hosts it calls: the agent's
// provider or the gateway, GitHub, the artifact service, and the hosts that
// installing Costroid, cosign, Bun and the agent need.
func reaches(job *yaml.Node) []string {
	hosts := map[string]bool{"github.com:443": true}
	for _, step := range sequence(field(job, "steps")) {
		uses, _, _ := strings.Cut(strings.ToLower(scalarField(step, "uses")), "@")
		run := scalarField(step, "run")
		switch stepAgent(step) {
		case claudeAgent:
			hosts["api.anthropic.com:443"] = true
		case codexAgent:
			hosts["api.openai.com:443"] = true
		}
		if strings.Contains(text(field(step, "env")), "vars.COSTROID_GATEWAY_URL") {
			hosts[gatewayHost] = true
		}
		if agentAction(step) != "" {
			hosts["api.github.com:443"] = true
			hosts["registry.npmjs.org:443"] = true
		}
		if agentAction(step) == claudeCodeAction {
			hosts["release-assets.githubusercontent.com:443"] = true
		}
		if strings.Contains(run, "npm install") {
			hosts["registry.npmjs.org:443"] = true
		}
		if stage := costroidStage(step); stage != "" {
			hosts["release-assets.githubusercontent.com:443"] = true
			hosts["tuf-repo-cdn.sigstore.dev:443"] = true
			if stage == "run" || stage == "read" {
				uses = "actions/upload-artifact"
			}
		}
		if uses == "actions/upload-artifact" {
			hosts["results-receiver.actions.githubusercontent.com:443"] = true
			hosts["productionresultssa*.blob.core.windows.net:443"] = true
		}
	}
	if hosts[gatewayHost] {
		delete(hosts, "api.anthropic.com:443")
	}
	return slices.Sorted(maps.Keys(hosts))
}

// Each agent job allows exactly the hosts its steps reach, each of them one
// lint-recipes knows. No agent job sends rows, so none allows an ingest URL. A
// host or a telemetry setting outside that draws a note.
func TestAllowedEndpointsNameOnlyTheProviderTheSCMAndTheIngest(t *testing.T) {
	doc := read(t, recipesDoc)
	for name, job := range agentJobs(t) {
		for _, step := range sequence(field(job.value, "steps")) {
			if costroidStage(step) == "ingest" || strings.Contains(text(step), "COSTROID_INGEST_URL") {
				t.Errorf("%s sends rows from the agent's job", name)
			}
		}
		listed := allowedEndpoints(scalarField(field(sequence(field(job.value, "steps"))[0], "with"), "allowed-endpoints"))
		if want := reaches(job.value); !slices.Equal(slices.Sorted(slices.Values(listed)), want) {
			t.Errorf("%s allows %v, and its steps reach %v", name, listed, want)
		}
		for _, host := range listed {
			if !egressHosts[host] {
				t.Errorf("%s allows %s, which lint-recipes does not know", name, host)
			}
			if !strings.Contains(doc, strings.TrimPrefix(strings.TrimSuffix(host, ":443"), "vars.")) {
				t.Errorf("docs/recipes.md does not name %s", host)
			}
		}
	}

	source := read(t, filepath.Join(recipesDir, "github-claude-code.yml"))
	for _, change := range []struct{ name, from, to, says string }{
		{"another host", "            github.com:443\n", "            github.com:443\n            example.com:443\n", "allows example.com:443"},
		{"another variable", "            github.com:443\n", "            github.com:443\n            ${{ vars.ELSEWHERE }}\n", "allows vars.ELSEWHERE"},
		{"telemetry on", "          disable-telemetry: true\n", "", "set disable-telemetry: true"},
		{"telemetry false", "          disable-telemetry: true\n", "          disable-telemetry: false\n", "set disable-telemetry: true"},
	} {
		t.Run(change.name, func(t *testing.T) {
			if !strings.Contains(source, change.from) {
				t.Fatalf("the edit's text is not in the recipe: %q", change.from)
			}
			report := LintBytes("x.yml", []byte(strings.Replace(source, change.from, change.to, 1)))
			if len(report.Findings) != 0 {
				t.Fatalf("rejected: %v", report.Findings)
			}
			if got := rules(report.Notes); !slices.Equal(got, []string{RuleEgressEndpoint}) || !strings.Contains(report.Notes[0].Message, change.says) {
				t.Fatalf("notes: %v", report.Notes)
			}
		})
	}
}

// A report that is not told the agent's job was skipped says the agent printed
// no usage, and that job has no log to read.
func TestReportJobsPassAgentResult(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-codex.yml", "github-claude-code-litellm.yml", "github-claude-code-action.yml"} {
		t.Run(name, func(t *testing.T) {
			documents, err := parse([]byte(read(t, filepath.Join(recipesDir, name))))
			if err != nil {
				t.Fatal(err)
			}
			jobs := field(documents[0], "jobs")
			var agents []string
			for _, job := range entries(jobs) {
				if slices.ContainsFunc(sequence(field(job.value, "steps")), func(step *yaml.Node) bool { return stepAgent(step) != "" }) {
					agents = append(agents, job.key.Value)
				}
			}
			if len(agents) != 1 {
				t.Fatalf("agent jobs %v", agents)
			}
			want := "${{ needs." + agents[0] + ".result }}"
			reports := 0
			for _, job := range entries(jobs) {
				for _, step := range sequence(field(job.value, "steps")) {
					if costroidStage(step) != "report" {
						continue
					}
					reports++
					if !slices.Contains(strings.Fields(text(field(job.value, "needs"))), agents[0]) {
						t.Errorf("job %s does not need %s", job.key.Value, agents[0])
					}
					if got := scalarField(field(step, "with"), "agent-result"); got != want {
						t.Errorf("job %s passes agent-result %q, want %q", job.key.Value, got, want)
					}
				}
			}
			if reports != 1 {
				t.Fatalf("%d report steps", reports)
			}
		})
	}
}

// A reader meets the header before the budget, and each push is a new run.
func TestEachRecipeSaysEachPushRunsTheAgentAgain(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-codex.yml", "github-claude-code-litellm.yml", "github-claude-code-action.yml", gitLabRecipe} {
		var header []string
		for _, line := range strings.Split(read(t, filepath.Join(recipesDir, name)), "\n") {
			comment, found := strings.CutPrefix(line, "#")
			if !found {
				break
			}
			header = append(header, comment)
		}
		if !strings.Contains(strings.Join(strings.Fields(strings.Join(header, " ")), " "), "runs the agent again") {
			t.Errorf("%s's header does not say each push runs the agent again", name)
		}
	}
	if doc := read(t, recipesDoc); strings.Count(doc, "runs the agent again") < 3 {
		t.Error("docs/recipes.md does not say, for each CI system, that each push runs the agent again")
	}
}

func TestUnknownAgentIsAFinding(t *testing.T) {
	for name, named := range map[string]string{"aider": `"aider"`, `"line\nbreak"`: `"line\nbreak"`, "Claude": `"Claude"`} {
		source := strings.Replace(goodWorkflow, "agent: claude", "agent: "+name, 1)
		findings := LintBytes("x.yml", []byte(source)).Findings
		if len(findings) != 1 || findings[0].Rule != RuleUnknownAgent {
			t.Fatalf("%s: want one %s, got %v", name, RuleUnknownAgent, findings)
		}
		message := findings[0].String()
		for _, want := range []string{named, "claude or codex"} {
			if !strings.Contains(message, want) {
				t.Errorf("%q does not name %s", message, want)
			}
		}
		if strings.ContainsAny(message, "\r\n") {
			t.Errorf("the finding spans lines: %q", message)
		}
	}
	seed := "on: push\njobs:\n  agent:\n    steps:\n      - uses: Costroid/costroid/action@v1\n        with:\n          agent: aider\n"
	if !slices.Contains(rules(LintBytes("x.yml", []byte(seed)).Findings), RuleUnknownAgent) {
		t.Error("the fuzz seed's agent is not named")
	}
}

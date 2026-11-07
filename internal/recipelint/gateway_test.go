package recipelint

import (
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const gatewayRecipe = "github-claude-code-litellm.yml"

// agentKeyStep is the gateway recipe's agent step, where each seeded admin key
// lands in turn.
const agentKeyStep = "          COSTROID_GATEWAY_URL: ${{ vars.COSTROID_GATEWAY_URL }}\n          COSTROID_GATEWAY_KEY_FILE:"

// TestTheAdminKeyOnTheAgentsJobIsRefused seeds the shipped gateway recipe with
// each way the agent's job could read the admin key.
func TestTheAdminKeyOnTheAgentsJobIsRefused(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, gatewayRecipe))
	for _, change := range []edit{
		{"the admin secret on the agent's step", agentKeyStep, "          GATEWAY: ${{ secrets.LITELLM_MASTER_KEY }}\n" + agentKeyStep, RuleAgentAdminKey},
		{"the admin variable on the agent's step", agentKeyStep, "          COSTROID_GATEWAY_ADMIN_KEY: ${{ secrets.OTHER }}\n" + agentKeyStep, RuleAgentAdminKey},
		{"every secret on the agent's step", agentKeyStep, "          ALL: ${{ toJSON(secrets) }}\n" + agentKeyStep, RuleAgentAdminKey},
		{"the sealing key on the agent's step", agentKeyStep, "          COSTROID_GATEWAY_SEALING_KEY: ${{ secrets.OTHER }}\n" + agentKeyStep, RuleAgentAdminKey},
		{"the sealing secret on the agent's step", agentKeyStep, "          SEAL: ${{ secrets.COSTROID_GATEWAY_SEALING_KEY }}\n" + agentKeyStep, RuleAgentAdminKey},
		{"the admin secret in the workflow's env", "permissions: {}\n", "permissions: {}\n\nenv:\n  GATEWAY: ${{ secrets.LITELLM_MASTER_KEY }}\n", RuleAgentAdminKey},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
	t.Run("a renamed admin secret on the agent's job", func(t *testing.T) {
		renamed := strings.ReplaceAll(source, "secrets.LITELLM_MASTER_KEY", "secrets.GATEWAY_ADMIN")
		expectRule(t, renamed, edit{"renamed", "    timeout-minutes: 30\n", "    timeout-minutes: 30\n    env:\n      K: ${{ secrets.GATEWAY_ADMIN }}\n", RuleAgentAdminKey})
	})
}

func TestTheGatewayRecipeKeepsTheAdminKeyOffTheAgentsJob(t *testing.T) {
	report := LintBytes(gatewayRecipe, []byte(read(t, filepath.Join(recipesDir, gatewayRecipe))))
	if len(report.Findings) != 0 {
		t.Fatalf("findings: %v", report.Findings)
	}
}

func TestTheAdminKeyIsReadWhereverAJobNamesIt(t *testing.T) {
	root, err := parse([]byte("jobs:\n  mint:\n    steps:\n      - env:\n          COSTROID_GATEWAY_ADMIN_KEY: ${{ secrets.MINE }}\n"))
	if err != nil {
		t.Fatal(err)
	}
	names := adminSecrets(root[0])
	if !names["MINE"] || !names["LITELLM_MASTER_KEY"] {
		t.Fatalf("admin secrets: %v", names)
	}
}

// TestOnlyTheGatewayJobsNameTheAdminKeysEnvironment holds the shipped recipe to
// its trust note: the admin key lives in an environment, and only the jobs that
// mint and close keys reference it.
func TestOnlyTheGatewayJobsNameTheAdminKeysEnvironment(t *testing.T) {
	documents, err := parse([]byte(read(t, filepath.Join(recipesDir, gatewayRecipe))))
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range entries(field(documents[0], "jobs")) {
		holdsKey := strings.Contains(text(job.value), adminKeyVariable)
		named := scalarField(job.value, "environment") == "costroid-gateway"
		if holdsKey != named {
			t.Errorf("job %s: holds the admin key %t, names the environment %t", job.key.Value, holdsKey, named)
		}
	}
}

const (
	mintEnvironment  = "    environment: costroid-gateway\n    permissions:\n      # Downloads"
	closeEnvironment = "    environment: costroid-gateway\n    permissions:\n      # Attests"
	closeCommand     = "        run: costroid gateway close\n"
)

// TestOnlyTheMintJobInItsEnvironmentMayAttest holds id-token: write on the job
// that mints keys to that job while it names an environment.
func TestOnlyTheMintJobInItsEnvironmentMayAttest(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, gatewayRecipe))
	expectRule(t, source, edit{"the mint job outside its environment", mintEnvironment, "    permissions:\n      # Downloads", RuleIDTokenScope})
	expectRule(t, source, edit{"the mint job in an empty environment", mintEnvironment, "    environment: \"\"\n    permissions:\n      # Downloads", RuleIDTokenScope})
}

// TestCloseJobMayAttest holds id-token: write on the job that closes keys to
// that job while it names an environment, and holds the shipped close job to
// the token and the two ingest variables the mint job has.
func TestCloseJobMayAttest(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, gatewayRecipe))
	for _, change := range []edit{
		{"the close job outside its environment", closeEnvironment, "    permissions:\n      # Attests", RuleIDTokenScope},
		{"the close job in an empty environment", closeEnvironment, "    environment: \"\"\n    permissions:\n      # Attests", RuleIDTokenScope},
		{"the close job running something else", closeCommand, "        run: costroid gateway public-key\n", RuleIDTokenScope},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
	if findings := LintBytes(gatewayRecipe, []byte(source)).Findings; len(findings) != 0 {
		t.Errorf("the shipped close job is refused: %v", findings)
	}
	documents, err := parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	jobs := field(documents[0], "jobs")
	mint, closing := field(jobs, "gateway-mint"), field(jobs, "gateway-close")
	if grants(field(closing, "permissions"))["id-token"] != "write" {
		t.Error("the close job cannot attest: it holds no id-token: write")
	}
	for _, variable := range []string{"COSTROID_INGEST_URL", "COSTROID_INGEST_AUDIENCE"} {
		minted, closed := stepVariable(mint, variable), stepVariable(closing, variable)
		if minted == "" || closed != minted {
			t.Errorf("%s: the mint job sets %q, the close job %q", variable, minted, closed)
		}
	}
}

// stepVariable is the value one of a job's steps gives a variable.
func stepVariable(job *yaml.Node, name string) string {
	for _, step := range sequence(field(job, "steps")) {
		if value := scalarField(field(step, "env"), name); value != "" {
			return value
		}
	}
	return ""
}

// TestGatewayKeysNeedAnEnvironment seeds the shipped gateway recipe with each
// way a job could hold the admin key or the sealing key outside an
// environment, and holds the shipped forms of an environment to no finding.
func TestGatewayKeysNeedAnEnvironment(t *testing.T) {
	source := read(t, filepath.Join(recipesDir, gatewayRecipe))
	for _, change := range []edit{
		{"the mint job without its environment", mintEnvironment, "    permissions:\n      # Downloads", RuleGatewayEnvironment},
		{"the close job without its environment", closeEnvironment, "    permissions:\n      # Attests", RuleGatewayEnvironment},
		{"the close job with an empty environment", closeEnvironment, "    environment: \"\"\n    permissions:\n      # Attests", RuleGatewayEnvironment},
		{"the sealing key in the workflow's env", "permissions: {}\n\nconcurrency", "permissions: {}\n\nenv:\n  COSTROID_GATEWAY_SEALING_KEY: ${{ secrets.SEAL }}\n\nconcurrency", RuleGatewayEnvironment},
		{"the sealing key on a job of its own", "  ingest:\n", "  seal:\n    runs-on: ubuntu-24.04\n    permissions: {}\n    env:\n      COSTROID_GATEWAY_SEALING_KEY: ${{ secrets.SEAL }}\n    steps:\n      - run: costroid gateway public-key\n\n  ingest:\n", RuleGatewayEnvironment},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, source, change)
		})
	}
	named := strings.Replace(source, closeEnvironment, "    environment:\n      name: costroid-gateway\n    permissions:\n      # Attests", 1)
	if findings := LintBytes(gatewayRecipe, []byte(named)).Findings; len(findings) != 0 {
		t.Errorf("an environment named in a mapping: %v", findings)
	}
	twice := strings.Replace(source, "  agent:\n", "  agent: &agent\n", 1)
	twice = strings.Replace(twice, "  gateway-mint:\n", "  agent-again: *agent\n\n  gateway-mint:\n", 1)
	twice = strings.Replace(twice, closeEnvironment, "    permissions:\n      # Attests", 1)
	count := 0
	for _, finding := range LintBytes(gatewayRecipe, []byte(twice)).Findings {
		if finding.Rule == RuleGatewayEnvironment {
			count++
		}
	}
	if count != 1 {
		t.Errorf("two agent jobs gave %d findings for one close job outside its environment", count)
	}
}

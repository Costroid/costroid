package recipelint

import (
	"path/filepath"
	"strings"
	"testing"
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

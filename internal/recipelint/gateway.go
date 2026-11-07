package recipelint

import (
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	// RuleAgentAdminKey is a gateway's admin key or sealing key within the
	// agent's reach.
	RuleAgentAdminKey = "agent-admin-key"
	// RuleGatewayEnvironment is a job that holds a gateway's admin key or
	// sealing key and names no environment.
	RuleGatewayEnvironment = "gateway-environment"
)

// adminKeyVariable and sealingKeyVariable are where costroid gateway mint and
// close read the gateway's admin key and the sealing key's private half.
// Whatever secret a workflow puts there is one of them.
const (
	adminKeyVariable   = "COSTROID_GATEWAY_ADMIN_KEY"
	sealingKeyVariable = "COSTROID_GATEWAY_SEALING_KEY"
)

var gatewaySecretVariables = []string{adminKeyVariable, sealingKeyVariable}

// knownAdminSecrets are admin keys by their usual names, refused on the agent's
// job even when no step names them as one.
var knownAdminSecrets = []string{"LITELLM_MASTER_KEY"}

var (
	secretReference = regexp.MustCompile(`secrets\.([A-Za-z_][A-Za-z0-9_]*)`)
	// wholeSecrets hands a job every secret at once.
	wholeSecrets = regexp.MustCompile(`(?i)tojson\(\s*secrets\s*\)|secrets\s*\[`)
)

// adminSecrets lists the secrets a workflow passes as the gateway's admin key
// or as its sealing key.
func adminSecrets(root *yaml.Node) map[string]bool {
	names := map[string]bool{}
	for _, name := range knownAdminSecrets {
		names[name] = true
	}
	walkEntries(root, func(item entry) {
		if slices.Contains(gatewaySecretVariables, item.key.Value) {
			for _, match := range secretReference.FindAllStringSubmatch(text(item.value), -1) {
				names[match[1]] = true
			}
		}
	})
	return names
}

// walkEntries calls visit on every mapping entry under node.
func walkEntries(node *yaml.Node, visit func(entry)) {
	seen := map[*yaml.Node]bool{}
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		node = resolve(node)
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		for _, item := range entries(node) {
			visit(item)
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(node)
}

// agentAdminKey refuses an agent's job that can read the gateway's admin key
// or its sealing key: through its own text, through the workflow's env, which
// every job inherits, or through every secret at once. It then holds every
// job of the workflow to gatewayEnvironments.
func (l *linter) agentAdminKey(root *yaml.Node, job entry) {
	admin := adminSecrets(root)
	reach := text(job.value) + "\n" + text(field(root, "env"))
	for _, variable := range gatewaySecretVariables {
		if strings.Contains(reach, variable) {
			l.add(job.key, RuleAgentAdminKey, "the agent's job sets %s: the gateway's admin key and sealing key belong on the jobs that mint and close keys", variable)
		}
	}
	for _, match := range secretReference.FindAllStringSubmatch(reach, -1) {
		if admin[match[1]] {
			l.add(job.key, RuleAgentAdminKey, "the agent's job can read secrets.%s, the gateway's admin key or sealing key", match[1])
		}
	}
	if wholeSecrets.MatchString(reach) {
		l.add(job.key, RuleAgentAdminKey, "the agent's job reads every secret at once, the gateway's admin key among them")
	}
	l.gatewayEnvironments(root)
}

// gatewayEnvironments refuses a job that sets the gateway's admin key or its
// sealing key and names no environment. Only an environment's protection rules
// decide which runs reach those secrets; a repository secret reaches every job
// a pull request from a branch of the repository can write.
func (l *linter) gatewayEnvironments(root *yaml.Node) {
	for _, item := range entries(field(root, "env")) {
		if slices.Contains(gatewaySecretVariables, item.key.Value) {
			l.addOnce(item.key, RuleGatewayEnvironment, "the workflow's env sets %s, which every job inherits: set it on the jobs that mint and close keys, each naming an environment", item.key.Value)
		}
	}
	for _, job := range entries(field(root, "jobs")) {
		if namesEnvironment(job.value) {
			continue
		}
		walkEntries(job.value, func(item entry) {
			if slices.Contains(gatewaySecretVariables, item.key.Value) {
				l.addOnce(item.key, RuleGatewayEnvironment, "the job %s sets %s and names no environment: name the environment that holds it, so its protection rules decide which runs reach it", job.key.Value, item.key.Value)
			}
		})
	}
}

// namesEnvironment reports whether a job names an environment, as a name or as
// a mapping with a name.
func namesEnvironment(job *yaml.Node) bool {
	environment := resolve(field(job, "environment"))
	if environment == nil {
		return false
	}
	if environment.Kind == yaml.ScalarNode {
		return strings.TrimSpace(environment.Value) != ""
	}
	return strings.TrimSpace(scalarField(environment, "name")) != ""
}

// addOnce adds a finding unless the same one is already there: a workflow
// with two agent jobs checks its environments twice.
func (l *linter) addOnce(node *yaml.Node, rule, format string, args ...any) {
	finding := l.finding(node, rule, format, args...)
	if !slices.Contains(l.report.Findings, finding) {
		l.report.Findings = append(l.report.Findings, finding)
	}
}

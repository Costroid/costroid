package recipelint

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleAgentAdminKey is a gateway's admin key within the agent's reach.
const RuleAgentAdminKey = "agent-admin-key"

// adminKeyVariable is where costroid gateway mint and close read the gateway's
// admin key. Whatever secret a workflow puts there is the admin key.
const adminKeyVariable = "COSTROID_GATEWAY_ADMIN_KEY"

// knownAdminSecrets are admin keys by their usual names, refused on the agent's
// job even when no step names them as one.
var knownAdminSecrets = []string{"LITELLM_MASTER_KEY"}

var (
	secretReference = regexp.MustCompile(`secrets\.([A-Za-z_][A-Za-z0-9_]*)`)
	// wholeSecrets hands a job every secret at once.
	wholeSecrets = regexp.MustCompile(`(?i)tojson\(\s*secrets\s*\)|secrets\s*\[`)
)

// adminSecrets lists the secrets a workflow passes as the gateway's admin key.
func adminSecrets(root *yaml.Node) map[string]bool {
	names := map[string]bool{}
	for _, name := range knownAdminSecrets {
		names[name] = true
	}
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		node = resolve(node)
		if node == nil {
			return
		}
		for _, item := range entries(node) {
			if item.key.Value == adminKeyVariable {
				for _, match := range secretReference.FindAllStringSubmatch(text(item.value), -1) {
					names[match[1]] = true
				}
			}
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(root)
	return names
}

// agentAdminKey refuses an agent's job that can read the gateway's admin key:
// through its own text, through the workflow's env, which every job inherits,
// or through every secret at once.
func (l *linter) agentAdminKey(root *yaml.Node, job entry) {
	admin := adminSecrets(root)
	reach := text(job.value) + "\n" + text(field(root, "env"))
	if strings.Contains(reach, adminKeyVariable) {
		l.add(job.key, RuleAgentAdminKey, "the agent's job sets %s: the gateway's admin key belongs on the jobs that mint and close keys", adminKeyVariable)
	}
	for _, match := range secretReference.FindAllStringSubmatch(reach, -1) {
		if admin[match[1]] {
			l.add(job.key, RuleAgentAdminKey, "the agent's job can read secrets.%s, the gateway's admin key", match[1])
		}
	}
	if wholeSecrets.MatchString(reach) {
		l.add(job.key, RuleAgentAdminKey, "the agent's job reads every secret at once, the gateway's admin key among them")
	}
}

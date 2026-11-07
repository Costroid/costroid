package recipelint

import (
	"maps"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleIngestPlatform names an ingest step with platform-url that cannot send
// its rows to Costroid Platform: an input missing, an integration token
// written into the file, or a job condition that skips the job when only the
// platform is named. RuleConnectJob names a job that runs costroid connect
// and holds more than the permission its proof needs.
const (
	RuleIngestPlatform = "ingest-platform"
	RuleConnectJob     = "connect-job"
)

// platformInputs are the inputs an ingest step with platform-url also needs.
var platformInputs = []string{"installation-id", "platform-token"}

var (
	// secretInput is an input that is one secret and nothing else.
	secretInput = regexp.MustCompile(`^\$\{\{\s*secrets\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}$`)
	// variableInput is an input that is one repository variable, whose name
	// it captures.
	variableInput = regexp.MustCompile(`^\$\{\{\s*(vars\.[A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)
	// githubInstallations are the installation ids a GitHub job's rows come
	// through, the owner's or the GitHub App's.
	githubInstallations = regexp.MustCompile(`^github(-owner)?:\S`)
	connectCommand      = regexp.MustCompile(`\bcostroid\s+connect\b`)
)

// connects reports a step that runs costroid connect, which proves the job's
// owner with its OIDC token.
func connects(step *yaml.Node) bool {
	return connectCommand.MatchString(scalarField(step, "run"))
}

// platform holds a job's ingest step and its connect step to what Costroid
// Platform needs.
func (l *linter) platform(job entry, steps []*yaml.Node, granted map[string]string) {
	for _, step := range steps {
		switch {
		case costroidStage(step) == "ingest":
			l.ingestPlatform(job, step)
		case connects(step):
			l.connectJob(job, granted)
		}
	}
}

// ingestPlatform holds an ingest step that names Costroid Platform to the
// other two platform inputs, a token read from a secret, a GitHub
// installation, and a job condition that runs when the platform alone is
// named. A step that sends to a ledger alone is left as it is.
func (l *linter) ingestPlatform(job entry, step *yaml.Node) {
	with := field(step, "with")
	address := field(with, "platform-url")
	if address == nil || strings.TrimSpace(address.Value) == "" {
		return
	}
	for _, name := range platformInputs {
		if strings.TrimSpace(scalarField(with, name)) == "" {
			l.add(address, RuleIngestPlatform, "the ingest step sets platform-url and not %s, so its rows never reach Costroid Platform: set installation-id and platform-token as docs/export.md shows", name)
			return
		}
	}
	if token := field(with, "platform-token"); !secretInput.MatchString(strings.TrimSpace(token.Value)) {
		l.add(token, RuleIngestPlatform, "platform-token is not one secret, so the integration token is written into the workflow: pass ${{ secrets.COSTROID_PLATFORM_TOKEN }}")
	}
	if installation := field(with, "installation-id"); !githubInstallations.MatchString(installation.Value) {
		l.add(installation, RuleIngestPlatform, "installation-id is not github-owner: or github:, so the platform cannot bind the rows of a GitHub run: pass github-owner:${{ github.repository_owner_id }}")
	}
	variable := variableInput.FindStringSubmatch(strings.TrimSpace(address.Value))
	condition := scalarField(job.value, "if")
	if variable != nil && strings.Contains(condition, "vars.") && !strings.Contains(condition, variable[1]) {
		l.add(job.key, RuleIngestPlatform, "the job's if tests other variables and not %s, so the job is skipped when only Costroid Platform is named: test %s != '' beside them", variable[1], variable[1])
	}
}

// connectJob holds a job that runs costroid connect to id-token: write and
// nothing else. The proof is the job's OIDC token, and the job reads no code.
func (l *linter) connectJob(job entry, granted map[string]string) {
	rest := maps.Clone(granted)
	delete(rest, "id-token")
	if granted["id-token"] != "write" || len(rest) != 0 {
		l.add(job.key, RuleConnectJob, "the job runs costroid connect and must hold id-token: write and nothing else")
	}
}

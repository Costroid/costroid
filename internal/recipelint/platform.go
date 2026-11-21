package recipelint

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleIngestPlatform names an ingest step with platform-url that cannot send
// its rows to Costroid Platform: an input missing, an integration token
// written into the file, or a job condition that skips the job when only the
// platform is named. RuleConnectJob names a job that runs costroid connect
// and holds more than the permission its proof needs. RuleMergedJob names a
// job that runs costroid merged and holds more than its facts need, or runs
// where a pull request's code could decide what it sends.
const (
	RuleIngestPlatform = "ingest-platform"
	RuleConnectJob     = "connect-job"
	RuleMergedJob      = "merged-job"
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
	mergedCommand       = regexp.MustCompile(`\bcostroid\s+merged\b`)
)

// mergedPermissions are what costroid merged needs: the pushed commits and
// the pull requests each one closed, and the run token that names the push.
var mergedPermissions = map[string]string{"contents": "read", "pull-requests": "read", "id-token": "write"}

const (
	// mergedEvent is the one event costroid merged sends on. It runs the
	// default branch's own workflow file.
	mergedEvent = "push"
	// defaultBranchTest holds a job to the default branch, in the form the
	// connect recipe's job uses.
	defaultBranchTest = "github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"
	// platformTokenVariable is where costroid merged reads the integration
	// token, and integrationTokenPrefix starts every such token.
	platformTokenVariable  = "COSTROID_PLATFORM_TOKEN"
	integrationTokenPrefix = "costroid_it_"
)

// connects reports a step that runs costroid connect, which proves the job's
// owner with its OIDC token.
func connects(step *yaml.Node) bool {
	return connectCommand.MatchString(scalarField(step, "run"))
}

// merges reports a step that runs costroid merged, which sends its job's OIDC
// token with each merge fact as the run token.
func merges(step *yaml.Node) bool {
	return mergedCommand.MatchString(scalarField(step, "run"))
}

// platform holds a job's ingest step, its connect step and its merged step to
// what Costroid Platform needs.
func (l *linter) platform(root *yaml.Node, job entry, steps []*yaml.Node, granted map[string]string) {
	for _, step := range steps {
		switch {
		case costroidStage(step) == "ingest":
			l.ingestPlatform(job, step)
		case connects(step):
			l.connectJob(job, granted)
		case merges(step):
			l.mergedJob(root, job, step, granted)
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

// mergedJob holds a job that runs costroid merged to the three permissions its
// facts need, to a push of the default branch, which runs that branch's own
// workflow file, to no code and no agent, and to an integration token that
// reaches the step from a secret through its env.
func (l *linter) mergedJob(root *yaml.Node, job entry, step *yaml.Node, granted map[string]string) {
	if !maps.Equal(granted, mergedPermissions) {
		l.add(job.key, RuleMergedJob, "the job runs costroid merged and must hold contents: read, pull-requests: read and id-token: write and nothing else")
	}
	l.mergedEvents(root)
	if !heldTo(scalarField(job.value, "if"), defaultBranchTest) {
		l.add(job.key, RuleMergedJob, "the job runs costroid merged and its if must hold it to the default branch: join %s to the rest with &&", defaultBranchTest)
	}
	for _, other := range sequence(field(job.value, "steps")) {
		l.mergedStep(other)
	}
	token := field(field(step, "env"), platformTokenVariable)
	if token == nil || !secretInput.MatchString(strings.TrimSpace(token.Value)) {
		l.add(firstOf(token, step), RuleMergedJob, "the step that runs costroid merged must set %s in its env to one secret: pass ${{ secrets.COSTROID_PLATFORM_TOKEN }}", platformTokenVariable)
	}
	if run := field(step, "run"); strings.Contains(run.Value, platformTokenVariable) || strings.Contains(run.Value, integrationTokenPrefix) {
		l.add(run, RuleMergedJob, "the script that runs costroid merged names the integration token: costroid merged reads it from %s in the step's env", platformTokenVariable)
	}
}

// mergedEvents holds a workflow that runs costroid merged to push alone. A
// pull request's event runs the pull request's own workflow file, so its
// author would decide what the job sends, and on any other event the command
// sends nothing.
func (l *linter) mergedEvents(root *yaml.Node) {
	on := field(root, "on")
	if !slices.Equal(eventNames(on), []string{mergedEvent}) {
		l.add(firstOf(on, root), RuleMergedJob, "a workflow that runs costroid merged runs on push alone: a pull request's event runs the pull request's own workflow file, and on any other event the command sends nothing")
	}
}

// mergedStep refuses a step in a merged job that takes code onto the runner or
// starts an agent. costroid merged reads the event file and GitHub's API
// alone, and a ref's name does not say whose code it holds, so the job holds
// none.
func (l *linter) mergedStep(step *yaml.Node) {
	ref, _, _ := strings.Cut(strings.ToLower(scalarField(step, "uses")), "@")
	switch {
	case stepAgent(step) != "":
		l.add(step, RuleMergedJob, "a job that runs costroid merged runs no agent, which could mint the run token and send a fact of its own")
	case ref == checkoutAction:
		l.add(step, RuleMergedJob, "a job that runs costroid merged checks out no code: the command reads GitHub's API alone")
	}
	if match := headRef.FindString(text(step)); match != "" {
		l.add(step, RuleMergedJob, "%s names a pull request's head, and a job that runs costroid merged takes no pull request's code", match)
	}
	if run := field(step, "run"); run != nil && fetchCommand.MatchString(run.Value) {
		l.add(run, RuleMergedJob, "a job that runs costroid merged fetches no code: the command reads GitHub's API alone")
	}
}

// heldTo reports whether a job's if holds every run to test: test is one of
// the terms the condition joins with &&, and no || beside them lets a run
// past it. A term in parentheses is read the same way.
func heldTo(condition, test string) bool {
	condition = strings.Join(strings.Fields(condition), " ")
	if inner, found := strings.CutPrefix(condition, "${{"); found {
		condition = strings.TrimSpace(strings.TrimSuffix(inner, "}}"))
	}
	terms, joined := conjuncts(condition)
	if !joined {
		return false
	}
	for _, term := range terms {
		if term == test {
			return true
		}
		if inner, wrapped := strings.CutPrefix(term, "("); wrapped && strings.HasSuffix(inner, ")") && heldTo(strings.TrimSuffix(inner, ")"), test) {
			return true
		}
	}
	return false
}

// conjuncts splits an expression on each && outside parentheses and quotes.
// It reports false for an expression with || there, which lets a run past
// any one term.
func conjuncts(expression string) ([]string, bool) {
	var terms []string
	depth, quoted, start := 0, false, 0
	for i := 0; i < len(expression); i++ {
		switch c := expression[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(expression[i:], "||"):
			return nil, false
		case depth == 0 && strings.HasPrefix(expression[i:], "&&"):
			terms = append(terms, strings.TrimSpace(expression[start:i]))
			start = i + 2
			i++
		}
	}
	return append(terms, strings.TrimSpace(expression[start:])), true
}

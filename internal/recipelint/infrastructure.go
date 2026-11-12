package recipelint

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleInfrastructureFile names a step of Costroid's action whose estimate
// inputs do nothing or cannot work: infrastructure-source or
// infrastructure-optional without infrastructure-file, infrastructure-file on
// a stage that keeps no rows, a source name the command refuses, or an
// optional setting that is neither true nor false.
const RuleInfrastructureFile = "infrastructure-file"

const (
	infrastructureFileInput     = "infrastructure-file"
	infrastructureSourceInput   = "infrastructure-source"
	infrastructureOptionalInput = "infrastructure-optional"
)

// sourceName is the rule costroid ingest-run --source holds a name to.
var sourceName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// rowStages are the stages that keep the agent's rows, which an estimate joins.
var rowStages = map[string]bool{"run": true, "read": true, "all": true}

// estimateGap is one way a step's estimate inputs fail, with the node to name.
type estimateGap struct {
	node    *yaml.Node
	message string
}

// estimateGaps lists what is wrong with a Costroid action step's estimate
// inputs. A value that is one expression is not read.
func estimateGaps(step *yaml.Node) []estimateGap {
	stage := costroidStage(step)
	if stage == "" {
		return nil
	}
	with := field(step, "with")
	file := field(with, infrastructureFileInput)
	var gaps []estimateGap
	if file == nil || strings.TrimSpace(file.Value) == "" {
		for _, name := range []string{infrastructureSourceInput, infrastructureOptionalInput} {
			if input := field(with, name); input != nil {
				gaps = append(gaps, estimateGap{input, name + " is set and infrastructure-file is not, so no estimate is read: set infrastructure-file to the FOCUS cost file your estimator step writes"})
			}
		}
		return gaps
	}
	if !rowStages[stage] {
		gaps = append(gaps, estimateGap{file, "infrastructure-file is set on stage " + stage + ", which keeps no rows, so the estimate never reaches the report: set it on the step whose stage is run or read"})
	}
	if source := field(with, infrastructureSourceInput); source != nil && !wholeExpression.MatchString(source.Value) && (len(source.Value) > 64 || !sourceName.MatchString(source.Value)) {
		gaps = append(gaps, estimateGap{source, "infrastructure-source takes 1 to 64 lower-case letters, digits and single hyphens, so the step fails: name the estimator, such as hcp-terraform"})
	}
	if optional := field(with, infrastructureOptionalInput); optional != nil && !wholeExpression.MatchString(optional.Value) && optional.Value != "true" && optional.Value != "false" {
		gaps = append(gaps, estimateGap{optional, "infrastructure-optional is neither true nor false, so the step fails: write true to let a fork's pull request run without the file"})
	}
	return gaps
}

// estimateInputs holds each of a job's Costroid action steps to its estimate
// inputs, as a finding.
func (l *linter) estimateInputs(steps []*yaml.Node) {
	for _, step := range steps {
		for _, gap := range estimateGaps(step) {
			l.add(gap.node, RuleInfrastructureFile, "%s", gap.message)
		}
	}
}

// customerEstimateInputs holds a team's Costroid action steps to their
// estimate inputs, as notes: a wrong input costs the estimate, never money.
func (l *linter) customerEstimateInputs(steps []*yaml.Node) {
	for _, step := range steps {
		for _, gap := range estimateGaps(step) {
			l.flag(gap.node, Note, RuleInfrastructureFile, "%s", gap.message)
		}
	}
}

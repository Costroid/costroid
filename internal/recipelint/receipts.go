package recipelint

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// RuleReceiptsInput names a step of Costroid's action whose receipts input
// does nothing: set on a stage that posts no report, or to a value that is
// neither true nor false.
const RuleReceiptsInput = "receipts-input"

const receiptsInput = "receipts"

// reportStages are the stages that post the report, the one step that reads
// a receipt.
var reportStages = map[string]bool{"report": true, "all": true}

// receiptsGaps lists what is wrong with a Costroid action step's receipts
// input. A value that is one expression is not read.
func receiptsGaps(step *yaml.Node) []estimateGap {
	stage := costroidStage(step)
	input := field(field(step, "with"), receiptsInput)
	if stage == "" || input == nil {
		return nil
	}
	var gaps []estimateGap
	value := strings.TrimSpace(input.Value)
	if !reportStages[stage] && value != "false" {
		gaps = append(gaps, estimateGap{input, "receipts is set on stage " + stage + ", which posts no report, so no receipt is read: set it on the step whose stage is report"})
	}
	if !wholeExpression.MatchString(value) && value != "true" && value != "false" {
		gaps = append(gaps, estimateGap{input, "receipts is neither true nor false, so the report reads no receipt: write true to read the receipt the pull request's author posted"})
	}
	return gaps
}

// receiptsInputs holds each of a job's Costroid action steps to its receipts
// input, as a finding.
func (l *linter) receiptsInputs(steps []*yaml.Node) {
	for _, step := range steps {
		for _, gap := range receiptsGaps(step) {
			l.add(gap.node, RuleReceiptsInput, "%s", gap.message)
		}
	}
}

// customerReceiptsInputs holds a team's Costroid action steps to their
// receipts input, as notes: a wrong input costs the receipt's line, never
// money.
func (l *linter) customerReceiptsInputs(steps []*yaml.Node) {
	for _, step := range steps {
		for _, gap := range receiptsGaps(step) {
			l.flag(gap.node, Note, RuleReceiptsInput, "%s", gap.message)
		}
	}
}

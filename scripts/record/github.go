package main

import (
	"bytes"
	"strings"
)

// hostGitHub marks a recording of GitHub's API answers, not of an agent's run.
const hostGitHub = "github"

// resultRoot is the path a result object fenced in a check run's text is
// scrubbed under, so its keys never meet an agent's keys of the same name.
const resultRoot = "result"

// checkRunPaths are the keys a recording of GitHub's list of a commit's check
// runs keeps, the result object in each check run's text included. A check
// run's summary, its URLs, its App's owner and the pull requests it names
// are dropped.
var checkRunPaths = []string{
	"total_count",
	"check_runs",
	"check_runs[]",
	"check_runs[].name",
	"check_runs[].status",
	"check_runs[].conclusion",
	"check_runs[].started_at",
	"check_runs[].completed_at",
	"check_runs[].output",
	"check_runs[].output.title",
	"check_runs[].app",
	"check_runs[].app.slug",
	"result.schema",
	"result.conclusion",
	"result.state",
	"result.reason",
	"result.reason_line",
	"result.run_reason",
	"result.reader_error",
	"result.agent_error",
	"result.agent_error.class",
	"result.agent_skipped",
	"result.ci_run",
	"result.ci_run.attempt",
	"result.amounts",
	"result.rows",
	"result.rows_sha256",
	"result.run_identity",
	"result.figures",
	"result.figures[]",
	"result.figures[].currency",
	"result.figures[].list_cost",
	"result.figures[].cost_basis",
	"result.figures[].attribution_grade",
	"result.figures[].enforcement_grade",
	"result.figures[].rows",
	"result.figures[].fallback_rows",
	"result.figures[].tokens",
	"result.figures[].tokens.input_uncached",
	"result.figures[].tokens.cache_read",
	"result.figures[].tokens.cache_write_5m",
	"result.figures[].tokens.cache_write_1h",
	"result.figures[].tokens.cache_write_30m",
	"result.figures[].tokens.cache_write_12h",
	"result.figures[].tokens.output",
	"result.figures[].tokens.reasoning_output",
	"result.figures[].tokens.cached_input_reported",
	"result.figures_omitted",
	"result.cap",
	"result.cap.currency",
	"result.cap.amount",
	"result.cap.enforcement_grade",
	"result.cap.cost",
	"result.cap.cost_basis",
	"result.cap.remainder",
	"result.cap.overshoot",
	"result.cap.overshoot_tokens",
	"result.data",
	"result.data.agents",
	"result.data.agents[]",
	"result.data.models",
	"result.data.models[]",
	"result.data.models_omitted",
	"result.data.catalog_versions",
	"result.data.catalog_versions[]",
	"result.data.fallback_models",
	"result.data.fallback_models[]",
	"result.data.fallback_models_omitted",
	"result.data.fallback_price_ids",
	"result.data.fallback_price_ids[]",
	"result.data.fallback_price_ids_omitted",
	"result.policy",
	"result.policy.conclusion",
	"result.policy.trust",
	"result.policy.pack_sha256",
	"result.policy.config_sha256",
	"result.policy.config",
	"result.policy.config_from_base",
	"result.policy.evaluated_at",
	"result.policy.warn_only",
	"result.policy.warn_only.active",
	"result.policy.warn_only.reason",
	"result.policy.warn_only.started",
	"result.policy.warn_only.ends",
	"result.policy.results",
	"result.policy.results[]",
	"result.policy.results[].rule",
	"result.policy.results[].outcome",
	"result.policy.results[].reason",
	"result.policy.results[].held_by_warn_only",
	"result.policy.results[].limit_usd",
	"result.policy.results[].cost_usd",
	"result.policy.joinable_rows",
}

// embeddedResults are the kept keys whose string is a fenced result object,
// each scrubbed under its root.
var embeddedResults = map[string]string{
	"check_runs[].output.text": resultRoot,
}

const (
	fenceStart = "```json\n"
	fenceEnd   = "\n```"
)

// fencedObject parses the JSON object a fenced block holds.
func fencedObject(text string) (*value, bool) {
	inner, opened := strings.CutPrefix(text, fenceStart)
	inner, closed := strings.CutSuffix(inner, fenceEnd)
	if !opened || !closed {
		return nil, false
	}
	parsed, err := parseValue([]byte(inner))
	if err != nil || parsed.kind != kindObject {
		return nil, false
	}
	return parsed, true
}

// fenced writes v back as the block the check run's text held.
func fenced(v *value) string {
	var out bytes.Buffer
	out.WriteString(fenceStart)
	v.indent(&out, 0)
	out.WriteString(fenceEnd)
	return out.String()
}

// embedded scrubs the result object a string holds. A string that holds none
// is content, and is replaced.
func (s *scrubber) embedded(v *value, path, root string) *value {
	if v.kind != kindString {
		s.report.dropped[path]++
		return nil
	}
	object, ok := fencedObject(v.text)
	if !ok {
		s.report.replaced++
		return stringValue(placeholderContent)
	}
	scrubbed := stringValue(fenced(s.scrub(object, root)))
	if scrubbed.text != v.text {
		s.report.replaced++
	}
	return scrubbed
}

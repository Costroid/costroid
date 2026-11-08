package recipelint

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

// aliasedWorkflow writes its trigger, a job's settings and a step once each,
// under anchors, and repeats them through aliases and merge keys.
const aliasedWorkflow = `x-events: &events
  pull_request:
    types: [opened, synchronize]
x-job: &job
  runs-on: ubuntu-24.04
  steps:
    - &checkout
      uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
on: *events
jobs:
  first:
    <<: *job
  second:
    runs-on: ubuntu-24.04
    steps:
      - *checkout
      - <<: *checkout
        with:
          fetch-depth: 0
      - uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc
`

func workflowRoot(t *testing.T, source string) *yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	return document.Content[0]
}

func TestEventNamesReadsATriggerBehindAnAlias(t *testing.T) {
	if got := EventNames(workflowRoot(t, aliasedWorkflow)); !slices.Equal(got, []string{"pull_request"}) {
		t.Errorf("on: *events reads %q, want [pull_request]", got)
	}
}

func TestEventNamesExpandsAMergeKeyInTheTrigger(t *testing.T) {
	root := workflowRoot(t, "x-events: &events\n  pull_request: {}\non:\n  <<: *events\n  workflow_dispatch: {}\n")
	if got := EventNames(root); !slices.Equal(got, []string{"pull_request", "workflow_dispatch"}) {
		t.Errorf("the merged trigger reads %q, want [pull_request workflow_dispatch]", got)
	}
}

func TestEventNamesReadsEachWayToWriteTheTrigger(t *testing.T) {
	for source, want := range map[string][]string{
		"on: push\n":                        {"push"},
		"on: [push, pull_request]\n":        {"push", "pull_request"},
		"on:\n  schedule:\n    - cron: x\n": {"schedule"},
		"name: no trigger\n":                nil,
	} {
		if got := EventNames(workflowRoot(t, source)); !slices.Equal(got, want) {
			t.Errorf("%q reads %q, want %q", source, got, want)
		}
	}
}

func TestUsesVisitsAStepAnAliasRepeatsOnce(t *testing.T) {
	var got []string
	Uses(workflowRoot(t, aliasedWorkflow), func(key, value *yaml.Node) {
		if key.Value != "uses" {
			t.Errorf("visited the key %q", key.Value)
		}
		got = append(got, value.Value)
	})
	want := []string{
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc",
	}
	if !slices.Equal(got, want) {
		t.Errorf("visited %q, want %q", got, want)
	}
}

func TestUsesKeepsTheLineCommentOfAPin(t *testing.T) {
	root := workflowRoot(t, "jobs:\n  a:\n    steps:\n      - uses: Costroid/costroid/action@abc # v1.0.0\n")
	var comments []string
	Uses(root, func(_, value *yaml.Node) { comments = append(comments, value.LineComment) })
	if !slices.Equal(comments, []string{"# v1.0.0"}) {
		t.Errorf("the pin's comments read %q", comments)
	}
}

func TestFieldReadsThroughAMergeKey(t *testing.T) {
	jobs := Field(workflowRoot(t, aliasedWorkflow), "jobs")
	runner := Field(Field(jobs, "first"), "runs-on")
	if runner == nil || runner.Value != "ubuntu-24.04" {
		t.Fatalf("jobs.first.runs-on behind <<: *job reads %v", runner)
	}
	if Field(jobs, "third") != nil || Field(nil, "jobs") != nil {
		t.Error("Field found a key that is not there")
	}
}

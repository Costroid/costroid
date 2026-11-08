package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
)

// skillPaths are where Codex and Claude Code read a repository's skills, by
// path from its root.
var skillPaths = []string{".agents/skills/costroid/SKILL.md", ".claude/skills/costroid/SKILL.md"}

// costroidSkill is the skill init --skills writes. Its front matter's name and
// description are what both agents read before they load the rest.
const costroidSkill = `---
name: costroid
description: Reads what an agent's run on a pull request cost and how the costroid check concluded. Use it to answer what a run has spent so far, what is left of its cap, or whether the costroid check passed, from the run's status file while the run works and from the check run's result object after it. It never estimates what a change or a run will cost, and it never reads the pull request comment.
---

# Costroid

Costroid reports what an agent's run on a pull request cost, as a check run named costroid and a comment. This skill reads the two objects Costroid writes for a program: the run's status file while the run works, and the result object the check run carries after it.

## While a run works

costroid run writes a status file while the agent works. It sits at $COSTROID_STATUS_FILE, or at $RUNNER_TEMP/costroid-status.json when that is unset:

    cat "${COSTROID_STATUS_FILE:-$RUNNER_TEMP/costroid-status.json}"

spend holds one entry per currency and cost basis. cap.remainder is what is left of the cap, and cap.overshoot how far the run passed it, both in the cap's currency. enforcement_grade names what holds the run to its cap. No file means no costroid run started an agent in this job.

## After a run

The costroid check run on the pull request's head commit carries the result object. With the costroid binary, and GITHUB_TOKEN set:

    costroid result --pr NUMBER --repo OWNER/NAME --json

conclusion is the check's result: success, neutral or failure. Exit code 1 means no costroid check run is on the head commit yet. Without the binary, the GitHub CLI reads the same check run, whose output.text holds the object in one fenced json block:

    gh api "repos/OWNER/NAME/commits/HEAD_SHA/check-runs?check_name=costroid" --jq '.check_runs | max_by(.completed_at) | {conclusion, text: .output.text}'

With costroid mcp connected, its tools run_status and pull_result return the same two objects.

## How to state a figure

- Quote each amount as the decimal string it is, with its currency and its cost_basis beside it. Never round it or convert it.
- Add two figures only when their currency and cost_basis both match. There is no total across currencies.
- measured is an API key's usage. list_equivalent is a subscription run priced at the API's list rate, which nobody billed, so say "not billed" beside it.
- agent_stated is the agent's own figure in its own unit. Report it apart, and never add it to a figure.

## When not to use it

It never estimates what a change, a prompt or a run will cost before it runs. It never reads the pull request comment, whose copy can withhold amounts on a public repository.
`

// writeSkills writes the skill where each agent reads it, and leaves a file
// that exists as it is. It returns the files it wrote, by path from the root.
func writeSkills(repo repository, stdout io.Writer) ([]string, error) {
	var wrote []string
	for _, name := range skillPaths {
		switch err := createFile(repo.file(filepath.FromSlash(name)), []byte(costroidSkill), false); {
		case err == nil:
			wrote = append(wrote, name)
			fmt.Fprintf(stdout, "costroid: wrote %s, the skill that reads what a run cost\n", name)
		case errors.Is(err, fs.ErrExist):
			fmt.Fprintf(stdout, "costroid: %s exists, and init left it as it is\n", name)
		default:
			return wrote, err
		}
	}
	return wrote, nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// adapterDirectory maps a manifest's agent to the adapter package that reads it.
var adapterDirectory = map[string]string{
	agentClaudeCode: "claudecode",
	agentCodex:      "codex",
}

// placement puts one recording under its adapter's testdata.
type placement struct {
	run      string
	scenario string
	// from is the downloaded artifact's directory.
	from string
	// root is the module's root directory.
	root string
}

// artifactName is the name the recording workflow uploads a scenario under.
func artifactName(scenario string) string { return "recording-" + scenario }

// download fetches the run's recording with the GitHub CLI into a new
// directory and returns it.
func download(ctx context.Context, repository, run, scenario string) (string, error) {
	dir, err := os.MkdirTemp("", "record-artifact-")
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, "gh", "run", "download", run,
		"--repo", repository, "--name", artifactName(scenario), "--dir", dir)
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	if err := command.Run(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("gh run download %s: %w", run, err)
	}
	return dir, nil
}

// place verifies the artifact again, copies it to
// internal/adapter/<adapter>/testdata/<agent version>/<scenario>/ with a
// README, verifies the copy and returns its directory.
func (p placement) place() (string, error) {
	if !safeWord.MatchString(p.run) || !safeWord.MatchString(p.scenario) {
		return "", errors.New("the run id and the scenario must be plain words")
	}
	if problems, err := verifyDirectory(p.from, nil); err != nil || len(problems) > 0 {
		return "", refusal("the artifact", problems, err)
	}
	m, err := readManifest(p.from)
	if err != nil {
		return "", fmt.Errorf("the artifact holds no readable %s: %w", manifestName, err)
	}
	if m.Scenario != p.scenario {
		return "", fmt.Errorf("the artifact records scenario %q, not %q", m.Scenario, p.scenario)
	}
	if m.Run != "" && m.Run != p.run {
		return "", fmt.Errorf("the artifact records run %s, not %s", m.Run, p.run)
	}
	if !versionDirectory.MatchString(m.Version) {
		return "", fmt.Errorf("the artifact's agent version %q cannot name a directory", m.Version)
	}
	m.Run = p.run
	target := filepath.Join(p.root, "internal", "adapter", adapterDirectory[m.Agent], "testdata", m.Version, p.scenario)
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s exists; remove it in its own commit to record it again", target)
	}
	files, err := readRecording(p.from)
	if err != nil {
		return "", err
	}
	if err := writeFixture(p.from, target, m, files); err != nil {
		os.RemoveAll(target)
		return "", err
	}
	return target, nil
}

// writeFixture copies the recording to target with its manifest and README,
// then verifies the copy.
func writeFixture(from, target string, m manifest, files []*recordingFile) error {
	if err := copyTree(from, target); err != nil {
		return err
	}
	if err := writeManifest(target, m); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, readmeName), []byte(readme(m, files)), 0o644); err != nil {
		return err
	}
	if problems, err := verifyDirectory(target, nil); err != nil || len(problems) > 0 {
		return refusal(target, problems, err)
	}
	return nil
}

func refusal(what string, problems []string, err error) error {
	if err != nil {
		return fmt.Errorf("verify %s: %w", what, err)
	}
	return fmt.Errorf("%w: %s failed verify:\n  %s", errRefused, what, joinLines(problems))
}

func joinLines(lines []string) string { return strings.Join(lines, "\n  ") }

// copyTree copies every data file under from to the same name under to,
// leaving out a README the artifact may carry.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(from, path)
		if err != nil || name == readmeName || name == manifestName {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

var agentNames = map[string]string{agentClaudeCode: "Claude Code", agentCodex: "Codex"}

// readme states where the recording came from and the run's own total.
func readme(m manifest, files []*recordingFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", m.Scenario)
	if m.Run == localRunName {
		fmt.Fprintf(&b, "A recording of %s %s, run on a developer's machine", agentNames[m.Agent], m.Version)
	} else {
		fmt.Fprintf(&b, "A recording of %s %s, workflow run %s", agentNames[m.Agent], m.Version, m.Run)
	}
	if m.Date != "" {
		fmt.Fprintf(&b, " on %s", m.Date)
	}
	if m.Costroid != "" {
		fmt.Fprintf(&b, ", under Costroid at `%s`", m.Costroid)
	}
	b.WriteString(". `go run ./scripts/record verify` passed on it before and after it was placed here.\n\n")
	fmt.Fprintf(&b, "%s\n", runTotal(m.Agent, files))
	return b.String()
}

// runTotal is the agent's own figure for the whole run, with its source.
func runTotal(agent string, files []*recordingFile) string {
	if agent == agentClaudeCode {
		for _, name := range []string{"stream.jsonl", "claude-execution-output.json"} {
			if last := lastLine(files, name, func(v *value) bool { return v.fieldText("type") == "result" }); last != nil {
				if total := last.field("total_cost_usd"); total != nil && total.isNumber() {
					return fmt.Sprintf("The run's own total: %s USD, the `total_cost_usd` of the last result in `%s`. Claude Code estimates it at list price.", total.raw, name)
				}
			}
		}
		return "No result line states the run's own total."
	}
	if last := lastLine(files, "stream.jsonl", func(v *value) bool { return v.fieldText("type") == "turn.completed" }); last != nil {
		return "The run's own total, the `usage` of the last `turn.completed` in `stream.jsonl`: " + tokenCounts(last.field("usage")) + "."
	}
	if last := lastLine(files, "rollout.jsonl", func(v *value) bool { return v.field("payload").fieldText("type") == "token_count" }); last != nil {
		return "The run's own total, the `total_token_usage` of the last `token_count` in `rollout.jsonl`: " + tokenCounts(last.field("payload").field("info").field("total_token_usage")) + "."
	}
	return "No line states the run's own total."
}

func lastLine(files []*recordingFile, name string, match func(*value) bool) *value {
	for _, file := range files {
		if file.name != name {
			continue
		}
		for i := len(file.lines) - 1; i >= 0; i-- {
			if match(file.lines[i]) {
				return file.lines[i]
			}
		}
	}
	return nil
}

func tokenCounts(usage *value) string {
	var parts []string
	for _, field := range []struct{ key, words string }{
		{"input_tokens", "input"},
		{"cached_input_tokens", "cached input"},
		{"cache_write_input_tokens", "cache write"},
		{"output_tokens", "output"},
		{"reasoning_output_tokens", "reasoning output"},
	} {
		if count := usage.field(field.key); count != nil && count.isNumber() {
			parts = append(parts, count.raw+" "+field.words)
		}
	}
	if len(parts) == 0 {
		return "none stated"
	}
	return strings.Join(parts, ", ") + " tokens"
}

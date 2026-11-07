package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// localRunName stands in a manifest's run for a run on a developer's machine.
const localRunName = "local"

// localRun runs an agent on this machine and keeps only its scrubbed files.
type localRun struct {
	agent    string
	scenario string
	out      string
	prompts  []string
	command  []string
	// home is the agent's directory: CLAUDE_CONFIG_DIR, CODEX_HOME or their defaults.
	home   fs.FS
	stdin  io.Reader
	stderr io.Writer
	now    func() time.Time
}

// run starts the command with its stdout in a private directory, collects
// the run's own session from there, scrubs it into out and verifies it. The
// raw files are removed whatever happens. A scrubbed directory that fails
// verify is removed too.
func (l localRun) run() error {
	if len(l.command) == 0 {
		return errors.New("name the agent command after --")
	}
	if _, err := os.Stat(l.out); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s exists; name a new directory", l.out)
	}
	raw, err := os.MkdirTemp("", "record-raw-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(raw)
	stream := filepath.Join(raw, "agent-stdout.jsonl")
	started := l.now()
	if err := l.start(stream); err != nil {
		return err
	}
	files, err := collect(source{agent: l.agent, stream: stream, home: l.home, since: started})
	if err != nil {
		return err
	}
	collectedDir := filepath.Join(raw, "run")
	m := manifest{Recorded: true, Agent: l.agent, Scenario: l.scenario, Run: localRunName, Date: started.UTC().Format(time.DateOnly)}
	if err := files.write(collectedDir, m); err != nil {
		return err
	}
	prompts := append(l.prompts, files.prompts()...)
	if err := l.scrubAndVerify(collectedDir, prompts); err != nil {
		os.RemoveAll(l.out)
		return err
	}
	return nil
}

func (l localRun) scrubAndVerify(collectedDir string, prompts []string) error {
	report, err := scrubDirectory(collectedDir, l.out, prompts)
	if err != nil {
		return err
	}
	report.print(func(line string) { fmt.Fprintln(l.stderr, line) })
	problems, err := verifyDirectory(l.out, prompts)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: the scrubbed run failed verify:\n  %s", errRefused, joinLines(problems))
	}
	return nil
}

// start runs the command with its stdout in the file stream. An agent that
// exits non-zero has still left a run to record.
func (l localRun) start(stream string) error {
	out, err := os.OpenFile(stream, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	command := exec.Command(l.command[0], l.command[1:]...)
	command.Stdin, command.Stdout, command.Stderr = l.stdin, out, l.stderr
	err = command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		fmt.Fprintf(l.stderr, "record: the agent exited %d\n", exit.ExitCode())
		return nil
	}
	if err != nil {
		return fmt.Errorf("run %s: %w", l.command[0], err)
	}
	return nil
}

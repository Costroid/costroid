// Command record turns an agent's run into a test fixture that holds no
// content: no prompt, no reply, no tool output, no path and no credential.
//
// collect copies one run's files into a directory, scrub cuts them to an
// allowlist of keys and gives every id a synthetic form, and verify refuses
// anything that slipped through. local does all three around an agent run on
// this machine. fixture places a recording workflow's artifact beside the
// adapter that reads it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

const usage = `Usage:
  go run ./scripts/record collect --agent claude-code|codex --out DIR [--scenario NAME]
      [--stream FILE] [--execution-file FILE] [--home DIR] [--fresh-home] [--costroid REF]
  go run ./scripts/record scrub --out DIR [--prompt-file FILE]... RAW
  go run ./scripts/record verify [--prompt-file FILE]... DIR
  go run ./scripts/record local --agent claude-code|codex --scenario NAME --out DIR
      [--prompt-file FILE]... -- <agent command>
  go run ./scripts/record fixture --run ID --scenario NAME [--repo OWNER/NAME] [--from DIR]

collect reads the agent's stream for the run's session or thread, then copies
that session's files from --home: CLAUDE_CONFIG_DIR or CODEX_HOME by default.
--fresh-home says the directory was made for this run, as on a CI runner, so
its other sessions are the run's nested ones. The raw directory holds the
run's content: keep it on the machine that made it.

scrub writes RAW, cut to the allowlist, to a new directory. A working
directory reads /work, an agent's text @content and a prompt @prompt. Every
key it drops is printed by name, never by value.

verify exits 1 and names each problem when DIR holds a key outside the
allowlist, an id in no synthetic form, a credential, a home, runner or
temporary path, or the text of a --prompt-file. It prints ok otherwise.

local runs the agent command with its stdout as the stream, then collects
only the session that stream names and its subagents. Other sessions in the
agent's directory are never opened. It scrubs into --out and verifies; the
raw files are removed either way.

fixture downloads the artifact recording-NAME of a recording workflow run
with gh, or reads --from, verifies it again, and places it under
internal/adapter/<adapter>/testdata/<agent version>/NAME/ with a README.

Exit codes: 0 done, 1 verify refused the recording, 125 the command failed.
`

const (
	exitRefused = 1
	exitFailure = 125
)

var errRefused = errors.New("refused")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitFailure
	}
	commands := map[string]func(context.Context, []string, func(string) string, io.Reader, io.Writer, io.Writer) error{
		"collect": runCollect,
		"scrub":   runScrub,
		"verify":  runVerify,
		"local":   runLocal,
		"fixture": runFixture,
	}
	command, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "record: no command %q\n%s", args[0], usage)
		return exitFailure
	}
	err := command(ctx, args[1:], getenv, stdin, stdout, stderr)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errRefused):
		fmt.Fprintf(stderr, "record: %v\n", err)
		return exitRefused
	}
	fmt.Fprintf(stderr, "record: %v\n", err)
	return exitFailure
}

// repeated is a flag that may be given more than once.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	return set
}

func readPrompts(files []string) ([]string, error) {
	var prompts []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read the prompt: %w", err)
		}
		prompts = append(prompts, string(data))
	}
	return prompts, nil
}

func runCollect(_ context.Context, args []string, getenv func(string) string, _ io.Reader, _, stderr io.Writer) error {
	set := newFlags("collect", stderr)
	agent := set.String("agent", "", "")
	out := set.String("out", "", "")
	scenario := set.String("scenario", "", "")
	stream := set.String("stream", "", "")
	execution := set.String("execution-file", "", "")
	home := set.String("home", "", "")
	fresh := set.Bool("fresh-home", false, "")
	costroid := set.String("costroid", "", "")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *out == "" || set.NArg() > 0 {
		return errors.New("collect takes --out and no arguments")
	}
	if *home == "" {
		dir, err := agentHome(*agent, getenv)
		if err != nil {
			return err
		}
		*home = dir
	}
	files, err := collect(source{agent: *agent, stream: *stream, executionFile: *execution, home: os.DirFS(*home), freshHome: *fresh})
	if err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0o700); err != nil {
		return err
	}
	m := manifest{Recorded: true, Agent: *agent, Scenario: *scenario, Run: getenv("GITHUB_RUN_ID"), Date: time.Now().UTC().Format(time.DateOnly), Costroid: *costroid}
	return files.write(*out, m)
}

func runScrub(_ context.Context, args []string, _ func(string) string, _ io.Reader, _, stderr io.Writer) error {
	set := newFlags("scrub", stderr)
	out := set.String("out", "", "")
	var promptFiles repeated
	set.Var(&promptFiles, "prompt-file", "")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *out == "" || set.NArg() != 1 {
		return errors.New("scrub takes --out and one raw directory")
	}
	prompts, err := readPrompts(promptFiles)
	if err != nil {
		return err
	}
	report, err := scrubDirectory(set.Arg(0), *out, prompts)
	if err != nil {
		return err
	}
	report.print(func(line string) { fmt.Fprintln(stderr, line) })
	return nil
}

func runVerify(_ context.Context, args []string, _ func(string) string, _ io.Reader, stdout, stderr io.Writer) error {
	set := newFlags("verify", stderr)
	var promptFiles repeated
	set.Var(&promptFiles, "prompt-file", "")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 1 {
		return errors.New("verify takes one directory")
	}
	prompts, err := readPrompts(promptFiles)
	if err != nil {
		return err
	}
	problems, err := verifyDirectory(set.Arg(0), prompts)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s:\n  %s", errRefused, set.Arg(0), joinLines(problems))
	}
	fmt.Fprintln(stdout, "ok")
	return nil
}

func runLocal(_ context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	set := newFlags("local", stderr)
	agent := set.String("agent", "", "")
	scenario := set.String("scenario", "", "")
	out := set.String("out", "", "")
	var promptFiles repeated
	set.Var(&promptFiles, "prompt-file", "")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *out == "" || !safeWord.MatchString(*scenario) || adapterDirectory[*agent] == "" {
		return errors.New("local takes --agent claude-code or codex, --out and a --scenario that is a plain word")
	}
	prompts, err := readPrompts(promptFiles)
	if err != nil {
		return err
	}
	home, err := agentHome(*agent, getenv)
	if err != nil {
		return err
	}
	local := localRun{
		agent: *agent, scenario: *scenario, out: *out, prompts: prompts, command: set.Args(),
		home: os.DirFS(home), stdin: stdin, stderr: stderr, now: time.Now,
	}
	if err := local.run(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ok: %s\n", *out)
	return nil
}

func runFixture(ctx context.Context, args []string, _ func(string) string, _ io.Reader, stdout, stderr io.Writer) error {
	set := newFlags("fixture", stderr)
	runID := set.String("run", "", "")
	scenario := set.String("scenario", "", "")
	repository := set.String("repo", "", "")
	from := set.String("from", "", "")
	root := set.String("root", ".", "")
	if err := set.Parse(args); err != nil {
		return err
	}
	if !safeWord.MatchString(*runID) || !safeWord.MatchString(*scenario) {
		return errors.New("fixture takes --run and --scenario, each a plain word")
	}
	if *from == "" {
		if *repository == "" {
			return errors.New("fixture takes --repo, or --from with a downloaded artifact")
		}
		dir, err := download(ctx, *repository, *runID, *scenario)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		*from = dir
	}
	target, err := placement{run: *runID, scenario: *scenario, from: *from, root: *root}.place()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "placed %s\n", target)
	return nil
}

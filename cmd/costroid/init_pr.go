package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/comment"
	"go.yaml.in/yaml/v3"
)

const (
	// prBranch is the branch init --pr commits to.
	prBranch      = "try-costroid"
	commitMessage = "Add the costroid check"
	// runPolls and runPoll bound the wait for the pull request's run to show
	// in gh run list: 20 reads, 3 seconds apart.
	runPolls = 20
	runPoll  = 3 * time.Second
)

// prSettings are what --pr reads beyond its arguments: the environment, the
// terminal it asks for a key on, whether a person is there to answer, and how
// long it waits for the run to show.
type prSettings struct {
	getenv   func(string) string
	terminal string
	// attended reports whether standard input is a terminal. A script or a
	// coding agent can hold a terminal with nobody at it, and its standard
	// input is then a pipe, a file or nothing.
	attended func() bool
	polls    int
	poll     time.Duration
}

var prDefaults = prSettings{
	getenv:   os.Getenv,
	terminal: "/dev/tty",
	attended: func() bool { return cli.Terminal(os.Stdin) },
	polls:    runPolls,
	poll:     runPoll,
}

// newPRFlow checks what --pr needs before init writes anything: one secret
// for the agent's key, a remote to push to, and a way to come by the key.
// Where nobody is there to be asked for it and no variable holds it, the one
// thing that decides whether init can go on is on the host: the secret may be
// set already. So init lists the secrets first there, and refuses before it
// writes a file when the list names none.
func newPRFlow(repo repository, flags initFlags, settings prSettings, workflow string, recipe []byte, stdout, stderr io.Writer) (*prFlow, error) {
	secrets := secretNames(recipe)
	if len(secrets) != 1 {
		return nil, fmt.Errorf("the recipe reads %d secrets, and --pr sets one, the agent's key", len(secrets))
	}
	remote, err := remoteName(repo.root)
	if err != nil {
		return nil, fmt.Errorf("--pr pushes the branch to the repository's remote: %w", err)
	}
	flow := &prFlow{
		root:        repo.root,
		remote:      remote,
		secret:      secrets[0],
		keyEnv:      flags.keyEnv,
		keepOrg:     flags.keepOrg,
		platformURL: flags.platformURL,
		workflow:    workflowName(recipe, workflow),
		stdout:      stdout,
		stderr:      stderr,
		said:        cli.NotesFor(stderr),
		getenv:      settings.getenv,
		readKey:     terminal{path: settings.terminal, stderr: stderr}.readKey,
		polls:       settings.polls,
		poll:        settings.poll,
	}
	if flow.keepOrg || flow.keyEnv != "" || settings.attended() {
		return flow, nil
	}
	fmt.Fprintf(flow.said, "$ %s\n", cli.CommandLine(listSecretsCommand))
	if err := flow.listSecrets(listSecretsCommand); err != nil {
		return nil, fmt.Errorf("%s failed: %w", cli.CommandLine(listSecretsCommand), err)
	}
	if !flow.secretExists {
		return nil, errors.New(noTerminal + ": standard input is not a terminal")
	}
	flow.listed = true
	return flow, nil
}

// listSecretsCommand lists the repository's secrets by name.
var listSecretsCommand = []string{"gh", "secret", "list", "--json", "name"}

// workflowName is the name a workflow's runs carry: its name key, or else its
// path, as GitHub names a workflow that has none.
func workflowName(recipe []byte, path string) string {
	var document yaml.Node
	if yaml.Unmarshal(recipe, &document) == nil && len(document.Content) == 1 {
		if name := field(document.Content[0], "name"); name != nil && name.Kind == yaml.ScalarNode && name.Value != "" {
			return name.Value
		}
	}
	return path
}

// prFlow runs the commands init leaves, for --pr, in the repository's root.
// The key it sets as a secret reaches gh on standard input alone.
type prFlow struct {
	root, remote, secret, keyEnv, platformURL string
	// keepOrg leaves the key to an organization secret, so no repository
	// secret is listed or set.
	keepOrg bool
	// workflow is the name the written workflow's runs carry.
	workflow string
	files    []string
	// stdout and stderr are what a command init runs prints on each of its
	// own. said takes init's own lines, which go to stderr and which --quiet
	// drops.
	stdout io.Writer
	stderr io.Writer
	said   io.Writer
	getenv func(string) string
	// readKey asks for the key without echoing it.
	readKey func(prompt string) (string, error)
	polls   int
	poll    time.Duration

	// listed says the secrets were listed before init wrote anything, so the
	// steps do not list them again.
	listed, secretExists               bool
	head, pull, runID, runURL, comment string
}

// prStep is one command of --pr. args names what it runs, with a
// placeholder for a value an earlier command has not found yet.
type prStep struct {
	args func() []string
	skip func() bool
	run  func([]string) error
}

func (f *prFlow) steps() []prStep {
	fixed := func(args ...string) func() []string { return func() []string { return args } }
	known := func(value, placeholder string) string {
		if value == "" {
			return placeholder
		}
		return value
	}
	var steps []prStep
	if !f.keepOrg {
		steps = append(steps,
			prStep{args: fixed(listSecretsCommand...), skip: func() bool { return f.listed }, run: f.listSecrets},
			prStep{args: fixed("gh", "secret", "set", f.secret), skip: func() bool { return f.secretExists }, run: f.setSecret},
		)
	}
	if f.platformURL != "" {
		steps = append(steps, prStep{args: fixed("gh", "variable", "set", platformVariable, "--body", f.platformURL), run: f.passThrough})
	}
	return append(steps,
		prStep{args: fixed("git", "switch", "--create", prBranch), run: f.passThrough},
		prStep{args: fixed(append([]string{"git", "add", "--"}, f.files...)...), run: f.passThrough},
		prStep{args: fixed(append([]string{"git", "commit", "--message", commitMessage, "--"}, f.files...)...), run: f.passThrough},
		prStep{args: fixed("git", "rev-parse", "HEAD"), run: f.readHead},
		prStep{args: fixed("git", "push", "--set-upstream", f.remote, prBranch), run: f.passThrough},
		prStep{args: fixed("gh", "pr", "create", "--fill", "--head", prBranch), run: f.createPull},
		prStep{args: func() []string {
			return []string{"gh", "run", "list", "--commit", known(f.head, "<commit>"), "--event", "pull_request", "--json", "databaseId,url,workflowName"}
		}, run: f.findRun},
		prStep{args: func() []string { return []string{"gh", "run", "watch", known(f.runID, "<run id>")} }, run: f.passThrough},
		prStep{args: func() []string { return []string{"gh", "pr", "view", known(f.pull, prBranch), "--json", "comments"} }, run: f.findComment},
	)
}

// run runs each step in order, printing it before it runs, and stops at the
// first that fails. It then lists the steps left, the failed one first.
func (f *prFlow) run() error {
	if f.keepOrg {
		fmt.Fprintf(f.said, "costroid: init sets no repository secret %s, so the run reads the organization's\n", f.secret)
	}
	steps := f.steps()
	for at, step := range steps {
		if step.skip != nil && step.skip() {
			continue
		}
		args := step.args()
		fmt.Fprintf(f.said, "$ %s\n", cli.CommandLine(args))
		if err := step.run(args); err != nil {
			f.printLeft(steps[at:])
			return fmt.Errorf("%s failed: %w", cli.CommandLine(args), err)
		}
	}
	fmt.Fprintf(f.said, "costroid: the run posted its comment at %s\n", f.comment)
	return nil
}

func (f *prFlow) printLeft(steps []prStep) {
	fmt.Fprint(f.said, "\ninit stopped at the first command that failed. These commands are left, that one first:\n")
	for _, step := range steps {
		if step.skip == nil || !step.skip() {
			fmt.Fprintf(f.said, "  %s\n", cli.CommandLine(step.args()))
		}
	}
}

// command runs args in the root with stdin as its standard input, and its
// standard error passed through. It returns what the command printed when
// capture is set, and passes it through when it is not. gh is told never to
// prompt, so a question it would ask fails the step.
func (f *prFlow) command(args []string, stdin io.Reader, capture bool) (string, error) {
	command := exec.Command(args[0], args[1:]...)
	command.Dir = f.root
	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	command.Stdin = stdin
	command.Stderr = cli.Under(f.stderr)
	var output bytes.Buffer
	command.Stdout = f.stdout
	if capture {
		command.Stdout = &output
	}
	err := command.Run()
	return output.String(), err
}

func (f *prFlow) passThrough(args []string) error {
	_, err := f.command(args, nil, false)
	return err
}

// listedSecret is a secret as gh secret list --json name prints it.
type listedSecret struct {
	Name string `json:"name"`
}

func (f *prFlow) listSecrets(args []string) error {
	output, err := f.command(args, nil, true)
	if err != nil {
		return err
	}
	var secrets []listedSecret
	if err := json.Unmarshal([]byte(output), &secrets); err != nil {
		return fmt.Errorf("read the secrets gh listed: %w", err)
	}
	f.secretExists = slices.Contains(secrets, listedSecret{Name: f.secret})
	if f.secretExists {
		fmt.Fprintf(f.said, "costroid: the repository has the secret %s already, so init leaves it as it is\n", f.secret)
	}
	return nil
}

// setSecret sends the key to gh on standard input, so it is in no argument.
// It says first that the secret it sets outranks an organization's.
func (f *prFlow) setSecret(args []string) error {
	fmt.Fprintf(f.said, "costroid: a repository secret takes precedence over an organization secret of the same name, so the run reads the %s set here. Pass --keep-org-secret to keep the organization's\n", f.secret)
	key, err := f.key()
	if err != nil {
		return err
	}
	_, err = f.command(args, strings.NewReader(key), false)
	return err
}

// key reads the key from the variable --key-env names, or else asks for it.
func (f *prFlow) key() (string, error) {
	if f.keyEnv != "" {
		key := strings.TrimSpace(f.getenv(f.keyEnv))
		if key == "" {
			return "", fmt.Errorf("the environment variable %s is empty, so init has no key to set as %s", f.keyEnv, f.secret)
		}
		return key, nil
	}
	key, err := f.readKey(fmt.Sprintf("The value of %s, which init passes to gh and never prints: ", f.secret))
	if err != nil {
		return "", err
	}
	if key = strings.TrimSpace(key); key == "" {
		return "", fmt.Errorf("no value was given for %s", f.secret)
	}
	return key, nil
}

func (f *prFlow) readHead(args []string) error {
	output, err := f.command(args, nil, true)
	if err != nil {
		return err
	}
	if f.head = strings.TrimSpace(output); !fullSHA.MatchString(f.head) {
		return fmt.Errorf("git named the commit %q, which is not a full SHA", f.head)
	}
	return nil
}

func (f *prFlow) createPull(args []string) error {
	output, err := f.command(args, nil, true)
	fmt.Fprint(f.stdout, output)
	if err != nil {
		return err
	}
	lines := strings.Fields(output)
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "https://") {
		return errors.New("gh printed no address for the pull request")
	}
	f.pull = lines[len(lines)-1]
	return nil
}

// findRun reads the run list until the written workflow's run on the commit
// shows, since GitHub starts it a few seconds after the pull request opens.
func (f *prFlow) findRun(args []string) error {
	for read := range f.polls {
		if read > 0 {
			time.Sleep(f.poll)
		}
		output, err := f.command(args, nil, true)
		if err != nil {
			return err
		}
		var runs []struct {
			ID       int64  `json:"databaseId"`
			URL      string `json:"url"`
			Workflow string `json:"workflowName"`
		}
		if err := json.Unmarshal([]byte(output), &runs); err != nil {
			return fmt.Errorf("read the runs gh listed: %w", err)
		}
		for _, run := range runs {
			if run.Workflow == f.workflow {
				f.runID, f.runURL = strconv.FormatInt(run.ID, 10), run.URL
				return nil
			}
		}
	}
	return fmt.Errorf("no run of the workflow %s started on the commit within %s", f.workflow, time.Duration(f.polls)*f.poll)
}

// findComment finds Costroid's comment on the pull request by its marker.
func (f *prFlow) findComment(args []string) error {
	output, err := f.command(args, nil, true)
	if err != nil {
		return err
	}
	var view struct {
		Comments []struct {
			Body string `json:"body"`
			URL  string `json:"url"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return fmt.Errorf("read the comments gh listed: %w", err)
	}
	for _, posted := range slices.Backward(view.Comments) {
		if strings.Contains(posted.Body, comment.Marker) {
			f.comment = posted.URL
			return nil
		}
	}
	return fmt.Errorf("the run ended and the pull request holds no Costroid comment, so read the run's log at %s", f.runURL)
}

// terminal asks for a key on the terminal at path with its echo off, and
// prints its prompt to stderr.
type terminal struct {
	path   string
	stderr io.Writer
}

// noTerminal is why init asks for no key, and what to do in its place.
const noTerminal = "no terminal to ask for the key on, so name the environment variable that holds it with --key-env"

// readKey is reached only where standard input is a terminal: newPRFlow
// refuses a run nobody attends before init writes anything.
func (t terminal) readKey(prompt string) (key string, err error) {
	tty, err := os.Open(t.path)
	if err != nil {
		return "", fmt.Errorf(noTerminal+": %w", err)
	}
	defer tty.Close()
	if err := stty(tty, "-echo"); err != nil {
		return "", fmt.Errorf("turn off the terminal's echo: %w", err)
	}
	// Ctrl-C at the prompt ends costroid before the read returns, so the echo
	// is turned back on from there too.
	forget := cli.OnInterrupt(cli.Interrupt{Restore: func() { _ = stty(tty, "echo") }})
	defer forget()
	defer func() {
		if restore := stty(tty, "echo"); restore != nil && err == nil {
			err = fmt.Errorf("turn the terminal's echo back on: %w", restore)
		}
	}()
	fmt.Fprint(t.stderr, prompt)
	line, err := bufio.NewReader(tty).ReadString('\n')
	fmt.Fprintln(t.stderr)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the key: %w", err)
	}
	return line, nil
}

// stty changes a setting of the terminal tty with stty, which reads the
// terminal from its standard input.
func stty(tty *os.File, setting string) error {
	command := exec.Command("stty", setting)
	command.Stdin = tty
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("stty %s: %w: %s", setting, err, bytes.TrimSpace(output))
	}
	return nil
}

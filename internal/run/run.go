// Package run launches an agent under a budget and turns its usage into rows.
package run

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/reason"
)

const defaultGrace = 5 * time.Second

// Options describes one run.
type Options struct {
	// Agent names the adapter, one of Agents. Empty picks it by the command's
	// name.
	Agent  string
	Argv   []string
	Budget decimal.Decimal
	// BillingAccount fills BillingAccountId. Empty reads unknown.
	BillingAccount string
	// Credential fills SubAccountId. Empty reads null.
	Credential string
	// CI is what the CI system says about the run. Its tags are claims: the
	// rows stay unverified and unattributed until a server checks them.
	CI      cicontext.Context
	Rows    io.Writer
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Catalog *catalog.Catalog
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Grace is how long a stopped agent has before SIGKILL.
	Grace time.Duration
	// Gateway is the run's own key on a gateway. Nil runs without one.
	Gateway *gateway.Session
}

// installLines are the commands that install each agent at the version this
// release's recipes install.
var installLines = map[string]string{
	"claude": "npm install --global @anthropic-ai/claude-code@2.1.316",
	"codex":  "npm install --global @openai/codex@0.164.0-alpha.11.1",
}

// MissingAgentError is an agent command that is not on PATH.
type MissingAgentError struct {
	Name string
	Err  error
}

func (e *MissingAgentError) Error() string {
	return fmt.Sprintf("%s is not on PATH: install it with %s", e.Name, installLines[e.Name])
}

func (e *MissingAgentError) Unwrap() error {
	return e.Err
}

// RefusedError is a run that never started its agent, and why.
type RefusedError struct {
	Reason reason.Reason
	Err    error
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("run refused, reason %s: %v", e.Reason, e.Err)
}

func (e *RefusedError) Unwrap() error {
	return e.Err
}

// ReaderError is a reader that refused the agent's stream or its execution
// file. No usage was read, so no rows were written.
type ReaderError struct {
	Err error
}

func (e *ReaderError) Error() string {
	return "reason reader_refused: " + e.Err.Error()
}

func (e *ReaderError) Unwrap() error {
	return e.Err
}

type Result struct {
	Agent       string
	AgentExit   int
	Stopped     bool
	Enforcement adapter.Enforcement
	Report      adapter.Report
	Rows        int
	ListTotal   decimal.Decimal
	// Reason is set when the run wrote no rows.
	Reason reason.Reason
	// AgentError is the class of error the agent ended in, empty when it
	// ended in none. It can sit beside rows: an agent can fail after it has
	// made requests.
	AgentError adapter.AgentError
	// RowsSHA256 is the SHA-256 of the rows' x_CostroidRowId values, sorted,
	// each followed by a line feed, in lower-case hex. Empty when the run wrote
	// no rows.
	RowsSHA256 string
	// RowsFileSHA256 is the SHA-256 of every byte the run wrote to Rows, in
	// lower-case hex. A rows file that reads otherwise was written after the
	// run.
	RowsFileSHA256 string
}

// windowedSession is a Session that counts the sessions of other agent
// processes, and needs to know when the agent's own process ran.
type windowedSession interface {
	RunWindow(start, end time.Time)
}

// Agents are the names --agent takes: the two agents, and a program that
// runs each through its SDK.
var Agents = []string{claudecode.Name, claudecode.SDKName, codex.Agent{}.Name(), codex.SDKName}

// AgentNamed picks the adapter by its name, whatever the command is.
func AgentNamed(name string) (adapter.Agent, error) {
	switch name {
	case claudecode.Name:
		return claudecode.Agent{}, nil
	case claudecode.SDKName:
		return claudecode.SDKAgent{}, nil
	case codex.Agent{}.Name():
		return codex.Agent{}, nil
	case codex.SDKName:
		return codex.SDKAgent{}, nil
	}
	return nil, fmt.Errorf("run: no agent named %q: --agent takes %s", name, strings.Join(Agents, ", "))
}

// agent is the run's adapter: the one Agent names, or the one the command's
// name picks.
func (o Options) agent() (adapter.Agent, error) {
	if len(o.Argv) == 0 {
		return nil, errors.New("run: no agent command")
	}
	if o.Agent != "" {
		return AgentNamed(o.Agent)
	}
	return AgentFor(o.Argv)
}

// AgentFor picks the adapter by the command's name.
func AgentFor(argv []string) (adapter.Agent, error) {
	if len(argv) == 0 {
		return nil, errors.New("run: no agent command")
	}
	switch filepath.Base(argv[0]) {
	case "claude":
		return claudecode.Agent{}, nil
	case "codex":
		return codex.Agent{}, nil
	}
	return nil, fmt.Errorf("run: no adapter for %q: costroid runs claude and codex", filepath.Base(argv[0]))
}

// launcher is a command that starts an agent's package or script.
type launcher struct {
	// valued are its options that take the next argument as their value.
	valued []string
	// refused are its options that run code of their own in place of a
	// package or script, which no agent's arguments can follow.
	refused []string
}

// launchers are the commands that start Claude Code or Codex from a package
// or a script. Each takes its own options before the package or script, and
// the agent's after it.
var launchers = map[string]launcher{
	"npx": {
		valued:  []string{"-p", "--package", "--registry", "--cache", "--userconfig", "--prefix", "-w", "--workspace"},
		refused: []string{"-c", "--call"},
	},
	"bunx": {valued: []string{"-p", "--package"}},
	"node": {
		valued:  []string{"-r", "--require", "--import", "--loader", "--experimental-loader", "-C", "--conditions"},
		refused: []string{"-e", "--eval", "-p", "--print"},
	},
}

// splitLauncher splits a command that starts the agent through a launcher
// into the launcher with its options and the package or script it starts,
// and the agent's own arguments. A "--" that ends the launcher's options is
// left out, so the agent's arguments hold the only "--" the run reads. It is
// false for a command that is not a launcher's.
func splitLauncher(argv []string) (prefix, args []string, launched bool, err error) {
	name := filepath.Base(argv[0])
	known, ok := launchers[name]
	if !ok {
		return nil, nil, false, nil
	}
	prefix = []string{argv[0]}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		flag, _, inline := strings.Cut(arg, "=")
		switch {
		case arg == "--":
			if i+1 == len(argv) {
				return nil, nil, true, fmt.Errorf("run: %s names no package or script after --", name)
			}
			return append(prefix, argv[i+1]), argv[i+2:], true, nil
		case slices.Contains(known.refused, flag):
			return nil, nil, true, fmt.Errorf("run: %s %s runs code of its own, so no agent's arguments follow it: name the package or the script", name, flag)
		case slices.Contains(known.valued, flag) && !inline:
			if i+1 == len(argv) {
				return nil, nil, true, fmt.Errorf("run: %s %s takes a value", name, flag)
			}
			prefix = append(prefix, arg, argv[i+1])
			i++
		case strings.HasPrefix(arg, "-") && arg != "-":
			prefix = append(prefix, arg)
		default:
			return append(prefix, arg), argv[i+1:], true, nil
		}
	}
	return nil, nil, true, fmt.Errorf("run: %s names no package or script", name)
}

// planCommand plans the agent's command. A command that starts Claude Code
// or Codex through a launcher is planned as the agent's own arguments, and
// what the plan adds goes after the package or script the launcher starts.
// The session gets the agent's own arguments under no command name: the
// launcher's version is not the agent's, so Codex's version is read from its
// rollout file.
func planCommand(agent adapter.Agent, argv []string, budget decimal.Decimal) (plan, session adapter.Plan, err error) {
	switch agent.(type) {
	case claudecode.Agent, codex.Agent:
	default:
		plan, err = agent.Plan(argv, budget)
		return plan, plan, err
	}
	prefix, args, launched, err := splitLauncher(argv)
	if err != nil || !launched {
		if err == nil {
			plan, err = agent.Plan(argv, budget)
		}
		return plan, plan, err
	}
	own, err := agent.Plan(append([]string{argv[0]}, args...), budget)
	if err != nil {
		return own, own, err
	}
	plan, session = own, own
	plan.Argv = slices.Concat(prefix, own.Argv[1:])
	session.Argv = slices.Concat([]string{""}, own.Argv[1:])
	return plan, session, nil
}

// Run starts the agent, reads its stream, stops it at the cap when the plan
// watches the run, and writes the rows at the grade the run earned.
func Run(ctx context.Context, opts Options) (Result, error) {
	agent, err := opts.agent()
	if err != nil {
		return Result{}, err
	}
	plan, sessionPlan, err := planCommand(agent, opts.Argv, opts.Budget)
	if err != nil {
		return Result{}, err
	}
	release, err := claim(agent)
	if err != nil {
		result := Result{Agent: agent.Name()}
		var refused *RefusedError
		if errors.As(err, &refused) {
			result.Reason = refused.Reason
		}
		return result, err
	}
	defer release()
	process := opts.withDefaults()
	held, err := process.openGateway(ctx, agent)
	if err != nil {
		return Result{Agent: agent.Name()}, err
	}
	if held != nil {
		plan.Enforcement = held.enforcement("")
		plan.Argv = held.argv(plan.Argv)
	}
	sessionPlan.Enforcement = plan.Enforcement
	session := agent.NewSession(sessionPlan)
	if billed, ok := session.(adapter.GatewayBilled); ok && held != nil {
		billed.BilledThroughGateway()
	}
	session = process.sessionWithStatus(agent.Name(), plan, session)
	builder := process.builder(agent.Name(), plan.Enforcement)
	builder.held = held
	result := Result{Agent: agent.Name(), Enforcement: plan.Enforcement}
	started := process.Now()
	stopWatch := held.watch(ctx)
	result.AgentExit, result.Stopped, err = process.execute(ctx, plan, session, builder, held)
	storeFailure := stopWatch()
	if err != nil {
		return result, err
	}
	if windowed, ok := session.(windowedSession); ok {
		windowed.RunWindow(started, process.Now())
	}
	report, err := session.Finish()
	if err != nil {
		result.Reason = reason.ReaderRefused
		return result, &ReaderError{Err: err}
	}
	if report.Enforcement.Grade != "" {
		builder.enforcement = report.Enforcement
		result.Enforcement = report.Enforcement
	}
	if held != nil {
		builder.enforcement = held.enforcement(storeFailure)
		result.Enforcement = builder.enforcement
	}
	if err := opts.record(&result, builder, report); err != nil {
		return result, err
	}
	if held != nil {
		held.compareSpend(ctx, &result)
	}
	endStatus(session, result.Enforcement.Grade)
	return result, nil
}

// Record writes the rows of a run that has already ended. The report's own
// grade stands, because no process was watched.
func Record(opts Options, agent string, report adapter.Report) (Result, error) {
	result := Result{Agent: agent, Enforcement: report.Enforcement}
	return result, opts.record(&result, opts.builder(agent, report.Enforcement), report)
}

// claim holds the agent's state directory when the agent needs it to itself.
// Two runs in one directory would each count the other's session as nested.
func claim(agent adapter.Agent) (func(), error) {
	exclusive, ok := agent.(adapter.Exclusive)
	if !ok {
		return func() {}, nil
	}
	release, err := exclusive.Claim()
	if errors.Is(err, adapter.ErrClaimed) {
		return nil, &RefusedError{Reason: reason.ConcurrentRun, Err: err}
	}
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return release, nil
}

func (o Options) record(result *Result, builder rowBuilder, report adapter.Report) error {
	report, renamed := containModels(report)
	if renamed > 0 {
		report.Warnings = append(report.Warnings, unknownModelLine(renamed))
	}
	rows, err := builder.rows(report)
	if err != nil {
		result.Report = report
		return err
	}
	if checked, warning := builder.checkOvershoot(rows); warning != "" {
		builder.enforcement, result.Enforcement = checked, checked
		report.Warnings = append(report.Warnings, warning)
		if rows, err = builder.rows(report); err != nil {
			result.Report = report
			return err
		}
	}
	result.AgentError = report.AgentError
	switch {
	case len(rows) == 0 && report.AgentError != "":
		result.Reason = reason.AgentError
		report.Warnings = append(report.Warnings, fmt.Sprintf("reason %s: %s", reason.AgentError, reason.AgentErrorLine(report.AgentError)))
	case len(rows) == 0:
		result.Reason = reason.NoUsage
		report.Warnings = append(report.Warnings, fmt.Sprintf("reason %s: %s", reason.NoUsage, reason.NoUsage.Sentence()))
	case report.AgentError != "":
		report.Warnings = append(report.Warnings, reason.AgentErrorLine(report.AgentError)+"; the rows hold the requests it made before")
	}
	result.Report = report
	written := sha256.New()
	writer := focus.NewWriter(io.MultiWriter(o.Rows, written))
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	result.Rows = len(rows)
	result.ListTotal = listTotal(rows)
	result.RowsSHA256 = rowsDigest(rows)
	result.RowsFileSHA256 = hex.EncodeToString(written.Sum(nil))
	return nil
}

func (o Options) builder(agent string, enforcement adapter.Enforcement) rowBuilder {
	return rowBuilder{
		catalog:     o.Catalog,
		agent:       agent,
		enforcement: enforcement,
		account:     o.BillingAccount,
		credential:  o.Credential,
		ci:          o.CI,
	}
}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Grace == 0 {
		o.Grace = defaultGrace
	}
	return o
}

// command is the agent's process: its own process group, writing its stdout
// to the run, with the run's join keys in its environment.
func (o Options) command(plan adapter.Plan, held *heldKey, stdout *os.File) *exec.Cmd {
	command := exec.Command(plan.Argv[0], plan.Argv[1:]...)
	command.Stdin = o.Stdin
	command.Stdout = stdout
	command.Stderr = o.Stderr
	command.Env = os.Environ()
	if attributes := o.CI.ResourceAttributes(os.Getenv(cicontext.ResourceAttributesVariable)); attributes != "" {
		command.Env = append(command.Env, cicontext.ResourceAttributesVariable+"="+attributes)
	}
	if held != nil {
		command.Env = held.env(command.Env)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command
}

// execute runs the agent to its end. Every process the agent leaves running
// is stopped when it exits, before its output is read to the end, so none can
// hold the run open, keep the agent's credentials or write the rows.
func (o Options) execute(ctx context.Context, plan adapter.Plan, session adapter.Session, builder rowBuilder, held *heldKey) (int, bool, error) {
	o = o.lockStderr()
	stdout, writer, err := os.Pipe()
	if err != nil {
		return 0, false, fmt.Errorf("run: open the agent's stdout: %w", err)
	}
	defer stdout.Close()
	command, contained, err := startContained(func() *exec.Cmd { return o.command(plan, held, writer) })
	// The agent holds its own copy, so the run's end of it closes when the
	// agent and everything it left have exited.
	writer.Close()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) && installLines[plan.Argv[0]] != "" {
			return 0, false, &MissingAgentError{Name: plan.Argv[0], Err: err}
		}
		return 0, false, fmt.Errorf("run: start %s: %w", plan.Argv[0], err)
	}
	stop := newStopper(command.Process.Pid, o.Grace)
	defer stop.release()
	watch := context.AfterFunc(ctx, stop.stop)
	defer watch()

	finishTail := o.tail(plan, session, builder, stop)
	readErr := o.read(stdout, session)
	stop.agentDone()
	// An agent whose output can no longer be read meets a closed pipe.
	stdout.Close()
	if left := contained.end(); left > 0 {
		fmt.Fprintf(o.Stderr, "costroid: warning: %s\n", leftRunning(left))
	}
	waitErr := command.Wait()
	contained.release()
	finishTail()
	if readErr != nil {
		return 0, stop.byBudget(), readErr
	}
	var exitError *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitError) {
		return 0, stop.byBudget(), fmt.Errorf("run: wait for the agent: %w", waitErr)
	}
	return exitCode(command), stop.byBudget(), nil
}

// lockStderr makes the run's stderr safe to share with the agent's. os/exec
// gives the agent a file as it is, but copies into any other writer on a
// goroutine of its own while the agent runs, and the run writes its warnings
// to the same writer then. Under --json the agent's stdout goes there too.
// Such a writer takes one write at a time. A file is left as it is, so the
// agent still writes to it directly.
func (o Options) lockStderr() Options {
	if _, isFile := o.Stderr.(*os.File); isFile || o.Stderr == nil {
		return o
	}
	locked := &lockedWriter{w: o.Stderr}
	if sameWriter(o.Stdout, o.Stderr) {
		o.Stdout = locked
	}
	o.Stderr = locked
	return o
}

// sameWriter says whether two writers are one. Two values of a type that
// cannot be compared are never the same writer.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() { _ = recover() }()
	return a == b
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// leftRunning says how many processes the agent left running at its exit.
func leftRunning(count int) string {
	if count == 1 {
		return "the agent left 1 process running, and costroid stopped it"
	}
	return fmt.Sprintf("the agent left %d processes running, and costroid stopped them", count)
}

func exitCode(command *exec.Cmd) int {
	state := command.ProcessState
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

// read copies the agent's stdout through and feeds the session. Lines are read
// whole, because a stream line can be longer than any fixed buffer. A blank
// line is not fed. A line the session cannot read is counted, and one warning
// at the end names the count and the first line's error.
func (o Options) read(stdout io.Reader, session adapter.Session) error {
	reader := bufio.NewReader(stdout)
	var unread unreadLines
	defer unread.summarize(o.Stderr)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := o.Stdout.Write(line); writeErr != nil {
				return fmt.Errorf("run: copy the agent's stdout: %w", writeErr)
			}
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				unread.add(session.Feed(trimmed, o.Now()))
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("run: read the agent's stdout: %w", err)
		}
	}
}

// unreadLines counts the stdout lines a session could not read and keeps the
// first one's error.
type unreadLines struct {
	count int
	first error
}

func (u *unreadLines) add(err error) {
	if err == nil {
		return
	}
	if u.count == 0 {
		u.first = err
	}
	u.count++
}

func (u *unreadLines) summarize(w io.Writer) {
	if u.count == 1 {
		fmt.Fprintf(w, "costroid: warning: 1 line of the agent's stdout was not read: %v\n", u.first)
	} else if u.count > 1 {
		fmt.Fprintf(w, "costroid: warning: %d lines of the agent's stdout were not read; the first: %v\n", u.count, u.first)
	}
}

// tail watches the agent's own file while the plan asks for it. The function
// it returns ends the watch and waits for its last read.
func (o Options) tail(plan adapter.Plan, session adapter.Session, builder rowBuilder, stop *stopper) func() {
	tailer, ok := session.(adapter.Tailer)
	if !ok || !plan.Enforcement.Watchdog {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tailer.Tail(ctx, func(requests []adapter.Request) { o.checkCap(requests, builder, stop) })
	}()
	return func() {
		cancel()
		<-done
	}
}

func (o Options) checkCap(requests []adapter.Request, builder rowBuilder, stop *stopper) {
	rows, err := builder.rows(adapter.Report{Requests: requests})
	if err != nil {
		fmt.Fprintf(o.Stderr, "costroid: price the run: %v\n", err)
		return
	}
	if listTotal(rows).Cmp(o.Budget) >= 0 {
		stop.stopForBudget()
	}
}

// stopper ends the agent's process group, once.
type stopper struct {
	pid    int
	grace  time.Duration
	once   sync.Once
	mu     sync.Mutex
	budget bool
	ended  bool
	timer  *time.Timer
}

func newStopper(pid int, grace time.Duration) *stopper {
	return &stopper{pid: pid, grace: grace}
}

// agentDone marks the agent's output closed. A cap read after this has no run
// left to stop, and must not be reported as a stop.
func (s *stopper) agentDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *stopper) stopForBudget() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.budget = true
	s.mu.Unlock()
	s.stop()
}

func (s *stopper) byBudget() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
}

func (s *stopper) stop() {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// A process group that is already gone needs no signal.
		_ = syscall.Kill(-s.pid, syscall.SIGTERM)
		s.timer = time.AfterFunc(s.grace, func() {
			_ = syscall.Kill(-s.pid, syscall.SIGKILL)
		})
	})
}

func (s *stopper) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
}

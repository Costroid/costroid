// Package run launches an agent under a budget and turns its usage into rows.
package run

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
)

const defaultGrace = 5 * time.Second

// Options describes one run.
type Options struct {
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
}

// Reason says why a run wrote no rows. Its values are a closed list, each
// with one sentence, so a reader of an empty rows file can name the cause.
type Reason string

// The reasons a run writes no rows.
const (
	// ReasonNoUsage is a run whose agent ended and reported no usage.
	ReasonNoUsage Reason = "no_usage"
	// ReasonConcurrentRun is a run refused before its agent started, because
	// another run held the agent's state directory.
	ReasonConcurrentRun Reason = "concurrent_run"
)

var reasonSentences = map[Reason]string{
	ReasonNoUsage:       "the agent reported no usage, so the run wrote no rows",
	ReasonConcurrentRun: "another costroid run holds the agent's state directory, so this run did not start and wrote no rows",
}

// Sentence is the reason in words.
func (r Reason) Sentence() string {
	return reasonSentences[r]
}

// RefusedError is a run that never started its agent, and why.
type RefusedError struct {
	Reason Reason
	Err    error
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("run refused, reason %s: %v", e.Reason, e.Err)
}

func (e *RefusedError) Unwrap() error {
	return e.Err
}

// Result is what the run came to.
type Result struct {
	Agent       string
	AgentExit   int
	Stopped     bool
	Enforcement adapter.Enforcement
	Report      adapter.Report
	Rows        int
	ListTotal   decimal.Decimal
	// Reason is set when the run wrote no rows.
	Reason Reason
}

// windowedSession is a Session that counts the sessions of other agent
// processes, and needs to know when the agent's own process ran.
type windowedSession interface {
	RunWindow(start, end time.Time)
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

// Run starts the agent, reads its stream, stops it at the cap when the plan
// watches the run, and writes the rows at the grade the run earned.
func Run(ctx context.Context, opts Options) (Result, error) {
	agent, err := AgentFor(opts.Argv)
	if err != nil {
		return Result{}, err
	}
	plan, err := agent.Plan(opts.Argv, opts.Budget)
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
	session := agent.NewSession(plan)
	builder := opts.builder(agent.Name(), plan.Enforcement)
	process := opts.withDefaults()
	result := Result{Agent: agent.Name(), Enforcement: plan.Enforcement}
	started := process.Now()
	result.AgentExit, result.Stopped, err = process.execute(ctx, plan, session, builder)
	if err != nil {
		return result, err
	}
	if windowed, ok := session.(windowedSession); ok {
		windowed.RunWindow(started, process.Now())
	}
	report, err := session.Finish()
	if err != nil {
		return result, err
	}
	if report.Enforcement.Grade != "" {
		builder.enforcement = report.Enforcement
		result.Enforcement = report.Enforcement
	}
	return result, opts.record(&result, builder, report)
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
		return nil, &RefusedError{Reason: ReasonConcurrentRun, Err: err}
	}
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return release, nil
}

func (o Options) record(result *Result, builder rowBuilder, report adapter.Report) error {
	rows, err := builder.rows(report)
	if err != nil {
		result.Report = report
		return err
	}
	if len(rows) == 0 {
		result.Reason = ReasonNoUsage
		report.Warnings = append(report.Warnings, fmt.Sprintf("reason %s: %s", ReasonNoUsage, ReasonNoUsage.Sentence()))
	}
	result.Report = report
	writer := focus.NewWriter(o.Rows)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	result.Rows = len(rows)
	result.ListTotal = listTotal(rows)
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

func (o Options) execute(ctx context.Context, plan adapter.Plan, session adapter.Session, builder rowBuilder) (int, bool, error) {
	command := exec.Command(plan.Argv[0], plan.Argv[1:]...)
	command.Stdin = o.Stdin
	command.Stderr = o.Stderr
	if attributes := o.CI.ResourceAttributes(os.Getenv(cicontext.ResourceAttributesVariable)); attributes != "" {
		command.Env = append(os.Environ(), cicontext.ResourceAttributesVariable+"="+attributes)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return 0, false, fmt.Errorf("run: open the agent's stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		return 0, false, fmt.Errorf("run: start %s: %w", plan.Argv[0], err)
	}
	stop := newStopper(command.Process.Pid, o.Grace)
	defer stop.release()
	watch := context.AfterFunc(ctx, stop.stop)
	defer watch()

	finishTail := o.tail(plan, session, builder, stop)
	readErr := o.read(stdout, session)
	stop.agentDone()
	waitErr := command.Wait()
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

func exitCode(command *exec.Cmd) int {
	state := command.ProcessState
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

// read copies the agent's stdout through and feeds the session. Lines are read
// whole, because a stream line can be longer than any fixed buffer.
func (o Options) read(stdout io.Reader, session adapter.Session) error {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := o.Stdout.Write(line); writeErr != nil {
				return fmt.Errorf("run: copy the agent's stdout: %w", writeErr)
			}
			if feedErr := session.Feed(bytes.TrimSpace(line), o.Now()); feedErr != nil {
				fmt.Fprintf(o.Stderr, "costroid: unreadable stream line: %v\n", feedErr)
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

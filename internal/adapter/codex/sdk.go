package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// SDKName is the costroid/agent tag of a program that runs Codex through the
// Codex SDK.
const SDKName = "codex-sdk"

// SDKAgent is a program that runs Codex through the Codex SDK. The program's
// stdout is its own, so the run reads none of it. Every thread whose rollout
// file appears under CODEX_HOME while the program runs is the run's, with the
// subagent threads it starts: in CI a thread in any working directory,
// outside CI one whose working directory is the run's. The run watches those
// files as it watches codex exec's, and stops the program at the cap. It
// cannot point Codex at a gateway: Codex reads its base URL from its
// configuration alone, and no command line reaches the Codex the program
// starts.
type SDKAgent struct {
	// Dir is the run's working directory. Empty means the process's own.
	Dir string
}

// Name returns the costroid/agent tag.
func (SDKAgent) Name() string {
	return SDKName
}

// Plan runs the program as it is, watched through the rollout files.
func (SDKAgent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	if len(argv) == 0 {
		return adapter.Plan{}, errors.New("codex-sdk: no command")
	}
	return adapter.Plan{
		Argv:        slices.Clone(argv),
		Enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: watchedNote, Watchdog: true},
	}, nil
}

// NewSession starts reading a run. It notes every rollout file the Codex home
// already holds, so a thread the program starts is told from an earlier one.
func (a SDKAgent) NewSession(plan adapter.Plan) adapter.Session {
	home := homeDir()
	return &SDKSession{
		planned:   plan.Enforcement,
		home:      home,
		dir:       a.dir(),
		oneJob:    cicontext.Detect(os.Getenv) != cicontext.NoSystem,
		costBasis: costBasisOf(os.Getenv(codexAPIKeyVariable)),
		model:     modelFrom(nil, filepath.Join(home, "config.toml")),
		before:    rolloutTimes(home),
		judged:    map[string]bool{},
		waiting:   map[string]sdkMeta{},
		rootOf:    map[string]string{},
	}
}

// dir is the run's working directory with its links resolved, as Codex states
// its own.
func (a SDKAgent) dir() string {
	dir := a.Dir
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return ""
		}
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// rolloutTimes is when each rollout file under home was last written, by its
// name below home.
func rolloutTimes(home string) map[string]time.Time {
	times := map[string]time.Time{}
	if home == "" {
		return times
	}
	names, err := fs.Glob(os.DirFS(home), rolloutPattern)
	if err != nil {
		return times
	}
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(home, filepath.FromSlash(name))); err == nil {
			times[name] = info.ModTime()
		}
	}
	return times
}

// sdkMeta is what a rollout file's session_meta line says about its thread.
type sdkMeta struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	CWD          string          `json:"cwd"`
	ThreadSource json.RawMessage `json:"thread_source"`
}

func (m sdkMeta) subagent() bool {
	return string(m.ThreadSource) == subagentSource
}

// sdkThread is one thread the run counts.
type sdkThread struct {
	name string
	meta sdkMeta
	file rolloutFile
	log  rolloutLog
}

// SDKSession reads the Codex threads of one program's run. Tail and Finish
// run on separate goroutines, so the lock covers everything below it.
type SDKSession struct {
	planned   adapter.Enforcement
	home      string
	dir       string
	oneJob    bool
	costBasis focus.CostBasis
	model     string
	// before holds the rollout files the home held when the run began.
	before map[string]time.Time

	mu sync.Mutex
	// threads are the run's threads, each subagent after the thread that
	// started it was found.
	threads []*sdkThread
	// judged holds the files already judged, the run's or not.
	judged map[string]bool
	// waiting holds subagent files whose parent is not yet known to be the
	// run's, by path.
	waiting map[string]sdkMeta
	// rootOf names, for each of the run's threads, the thread the program
	// started that it descends from.
	rootOf map[string]string
	// problem is the first reason the run's reading stops.
	problem  string
	warnings []string
	tailed   bool
}

// Feed passes over a line of the program's stdout. The program prints what it
// chooses, so nothing in it is read as usage.
func (*SDKSession) Feed([]byte, time.Time) error {
	return nil
}

// Tail follows every rollout file of the run until ctx ends, then reads them
// once more for the lines Codex wrote last. It calls settled each time a new
// request settles.
func (s *SDKSession) Tail(ctx context.Context, settled func([]adapter.Request)) {
	defer s.close()
	ticker := time.NewTicker(tailInterval)
	defer ticker.Stop()
	for {
		if requests, gained := s.follow(); gained {
			settled(requests)
		}
		select {
		case <-ctx.Done():
			s.follow()
			return
		case <-ticker.C:
		}
	}
}

// follow finds the run's new threads and reads what every one of them gained.
// It reports the run's requests so far, and true when a new one settled.
func (s *SDKSession) follow() ([]adapter.Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tailed = true
	s.discover()
	before := len(s.requests())
	for _, thread := range s.threads {
		lines, err := thread.file.lines()
		if err != nil {
			thread.log.refuse("the rollout file could not be read")
		}
		for _, line := range lines {
			thread.log.apply(line, s.model)
		}
	}
	all := s.requests()
	return all, len(all) > before
}

// discover judges each rollout file it has not judged. A file the home held
// before the run is the run's problem once it is written again: it holds an
// earlier run's spend. A new file is a thread the program started, or a
// subagent of one, once its first line is whole.
func (s *SDKSession) discover() {
	if s.home == "" {
		return
	}
	names, err := fs.Glob(os.DirFS(s.home), rolloutPattern)
	if err != nil {
		return
	}
	for _, name := range names {
		if s.judged[name] {
			continue
		}
		path := filepath.Join(s.home, filepath.FromSlash(name))
		if written, existed := s.before[name]; existed {
			if info, err := os.Stat(path); err == nil && !info.ModTime().Equal(written) {
				s.judged[name] = true
				s.refuse(fmt.Sprintf("rollout file %s was started before the program ran and written while it ran, so it holds an earlier run's spend: start a new thread, or give the program a CODEX_HOME made for the run", name))
			}
			continue
		}
		meta, complete, err := readSDKMeta(path)
		if !complete {
			continue
		}
		s.judged[name] = true
		switch {
		case err != nil:
			s.refuse(fmt.Sprintf("rollout file %s: %v", name, err))
		case meta.subagent():
			s.waiting[path] = meta
		case !s.oneJob && meta.CWD != s.dir:
			s.warnings = append(s.warnings, fmt.Sprintf("thread %s started while the program ran in another working directory, so it was not counted", meta.ID))
		default:
			s.take(name, path, meta, meta.ID)
		}
	}
	s.takeSubagents()
}

// takeSubagents takes every waiting file whose parent is one of the run's
// threads, until no more qualify, in path order.
func (s *SDKSession) takeSubagents() {
	for taken := true; taken; {
		taken = false
		for _, path := range slices.Sorted(maps.Keys(s.waiting)) {
			meta := s.waiting[path]
			root, ours := s.rootOf[meta.SessionID]
			if !ours {
				continue
			}
			delete(s.waiting, path)
			name, err := filepath.Rel(s.home, path)
			if err != nil {
				name = path
			}
			s.take(filepath.ToSlash(name), path, meta, root)
			taken = true
		}
	}
}

// take opens one of the run's threads. A file that cannot be opened stops the
// reading, because it holds some of the run's spend.
func (s *SDKSession) take(name, path string, meta sdkMeta, root string) {
	file, err := os.Open(path)
	if err != nil {
		s.refuse(fmt.Sprintf("rollout file %s could not be opened: %v", name, err))
		return
	}
	s.rootOf[meta.ID] = root
	s.threads = append(s.threads, &sdkThread{
		name: name,
		meta: meta,
		file: rolloutFile{file: file},
		log:  rolloutLog{thread: meta.ID, opened: true},
	})
}

func (s *SDKSession) refuse(problem string) {
	if s.problem == "" {
		s.problem = problem
	}
}

// readSDKMeta reads a file's first line as session_meta. It reports false
// while that line is not yet whole, so the file is judged again later.
func readSDKMeta(path string) (sdkMeta, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return sdkMeta{}, false, nil
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadBytes('\n')
	if err != nil {
		return sdkMeta{}, false, nil
	}
	var entry rolloutEntry
	var meta sdkMeta
	if json.Unmarshal(line, &entry) != nil || entry.Type != "session_meta" || json.Unmarshal(entry.Payload, &meta) != nil || !threadPattern.MatchString(meta.ID) {
		return sdkMeta{}, true, errors.New("it does not open with a session_meta line that names its thread")
	}
	return meta, true, nil
}

// requests are every request of the run's threads, in the order the threads
// were found.
func (s *SDKSession) requests() []adapter.Request {
	var all []adapter.Request
	for _, thread := range s.threads {
		all = append(all, thread.log.requests...)
	}
	return all
}

func (s *SDKSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, thread := range s.threads {
		thread.file.close()
	}
}

// Finish returns the run's usage. Each thread the program started reports
// under its own id, with the subagents it started, and the first names the
// run. A rollout file that failed a check refuses the run, because no other
// record of its spend exists.
func (s *SDKSession) Finish() (adapter.Report, error) {
	s.mu.Lock()
	tailed := s.tailed
	s.mu.Unlock()
	if !tailed {
		s.follow()
		s.close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.problem != "" {
		return adapter.Report{}, errors.New("codex-sdk: " + s.problem)
	}
	for _, thread := range s.threads {
		if thread.log.problem != "" {
			return adapter.Report{}, fmt.Errorf("codex-sdk: rollout file %s: %s", thread.name, thread.log.problem)
		}
	}
	reports := s.threadReports()
	if len(reports) == 0 {
		return adapter.Report{
			CostBasis:   s.costBasis,
			CostSource:  costSourceRollout,
			Enforcement: unwatched("no rollout file appeared under CODEX_HOME while the program ran"),
			Warnings: append(s.warnings, fmt.Sprintf("no rollout file under %s was started while the program ran, so the program started no Codex thread there, or gave Codex another CODEX_HOME",
				filepath.Join(s.home, "sessions"))),
		}, nil
	}
	report := reports[0]
	report.Nested = reports[1:]
	report.Enforcement = s.planned
	report.Warnings = append(slices.Clone(s.warnings), s.closingWarnings(&report)...)
	return report, nil
}

// threadReports is one report per thread the program started, holding its
// own requests and its subagents'.
func (s *SDKSession) threadReports() []adapter.Report {
	var reports []adapter.Report
	index := map[string]int{}
	for _, thread := range s.threads {
		root := s.rootOf[thread.meta.ID]
		at, ok := index[root]
		if !ok {
			at = len(reports)
			index[root] = at
			reports = append(reports, adapter.Report{
				SessionID:    root,
				AgentVersion: thread.log.version,
				CostBasis:    s.costBasis,
				CostSource:   costSourceRollout,
			})
		}
		reports[at].Requests = append(reports[at].Requests, thread.log.requests...)
	}
	return reports
}

// closingWarnings names the Codex versions no fixture records, and the
// requests the files show sent with no usage settled, which it counts in
// report.
func (s *SDKSession) closingWarnings(report *adapter.Report) []string {
	var warnings, versions []string
	var lastSent time.Time
	for _, thread := range s.threads {
		if version := thread.log.version; version != "" && !slices.Contains(recordedVersions, version) && !slices.Contains(versions, version) {
			versions = append(versions, version)
		}
		if !thread.log.sent.IsZero() {
			report.Unrecorded++
			lastSent = later(lastSent, thread.log.sent)
		}
	}
	for _, version := range versions {
		warnings = append(warnings, fmt.Sprintf("Codex %s is not a recorded version; its rollout files passed every check", version))
	}
	if report.Unrecorded > 0 {
		warnings = append(warnings, fmt.Sprintf("the rollout files show %s sent with no usage settled, the last at %s: Codex writes usage only when a request completes, so %s not counted",
			counted(report.Unrecorded, "model request"), lastSent.UTC().Format(time.RFC3339Nano), plural(report.Unrecorded, "that request is", "those requests are")))
	}
	return warnings
}

// Package copilot reads `copilot -p --output-format json` runs of GitHub
// Copilot CLI.
//
// The stream carries no token counts. The session's events.jsonl, which
// Copilot CLI writes under its home, holds one session.usage_record per model
// request and one session.shutdown with each model's totals. The plan names
// the session, so the budget is watched in that file during the run. The
// rows come from the usage records, and the shutdown's totals check them.
package copilot

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
)

// Name is the costroid/agent tag.
const Name = "copilot"

// watchedNote is the grade's note. Copilot CLI has no cap in tokens or
// dollars: its own, --max-ai-credits, counts its AI credits.
const watchedNote = "wrapper-watchdog: priced from Copilot CLI's session file during the run, with a stop at the cap that requests in flight can pass"

// sessionFlag names the session a new run starts.
const sessionFlag = "--session-id"

// The flags that start a session apart from the prompt's own: each resumes a
// session whose earlier spend the session file holds with this run's.
var resumeFlags = []string{"-r", "--resume", "--continue", "--session-id", "--connect"}

// valueFlags take the next argument as their value, from copilot --help of
// 1.0.103. A flag with an optional or repeated value takes none here: it
// never takes an argument that starts with "-".
var valueFlags = []string{
	"-i", "--interactive", "-p", "--prompt", "--model", "--reasoning-effort",
	"--context", "--auto-tier", "--agent", "-n", "--name", "--session-id",
	"--log-dir", "--extension-sdk-path", "--log-level", "--stream",
	"--output-format", "--add-dir", "--attachment", "--disable-mcp-server",
	"--add-github-mcp-toolset", "--add-github-mcp-tool", "--plugin-dir",
	"--additional-mcp-config", "--mcp-github-auth", "--max-autopilot-continues",
	"--mode", "--host-environment", "--host-compute-id", "--dynamic-retrieval",
	"--enable-mcp-server", "--max-ai-credits", "--usage-output-file",
}

// Agent is Copilot CLI.
type Agent struct{}

// Name returns the costroid/agent tag.
func (Agent) Name() string {
	return Name
}

// flagArg is one flag on a command line and its value, if it took one.
type flagArg struct {
	name, value string
	valued      bool
}

// flagsOf lists the command's flags, each with the value it took. A flag
// that takes a value takes the next argument, or the text after "=".
func flagsOf(argv []string) []flagArg {
	var found []flagArg
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		name, value, inline := strings.Cut(arg, "=")
		switch {
		case inline:
			found = append(found, flagArg{name: name, value: value, valued: true})
		case slices.Contains(valueFlags, arg) && i+1 < len(argv):
			found = append(found, flagArg{name: arg, value: argv[i+1], valued: true})
			i++
		default:
			found = append(found, flagArg{name: arg})
		}
	}
	return found
}

// Plan requires -p, adds --output-format json, whose result line names the
// session, and names a new session with --session-id, so the run can watch
// that session's file from its start. Copilot CLI writes its usage only to
// that file.
func (Agent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	if len(argv) == 0 {
		return adapter.Plan{}, errors.New("copilot: no command")
	}
	flags := flagsOf(argv)
	if !slices.ContainsFunc(flags, func(f flagArg) bool { return f.name == "-p" || f.name == "--prompt" }) {
		return adapter.Plan{}, errors.New("copilot: only `copilot -p` runs a prompt and exits, so the command needs -p")
	}
	if i := slices.IndexFunc(flags, func(f flagArg) bool { return slices.Contains(resumeFlags, f.name) }); i >= 0 {
		return adapter.Plan{}, fmt.Errorf("copilot: %s continues a session whose file holds its earlier spend with this run's; start a new session", flags[i].name)
	}
	planned := slices.Clone(argv)
	switch i := slices.IndexFunc(flags, func(f flagArg) bool { return f.name == "--output-format" }); {
	case i < 0:
		planned = slices.Insert(planned, 1, "--output-format", "json")
	case flags[i].value != "json":
		return adapter.Plan{}, fmt.Errorf("copilot: --output-format %s prints no result line naming the session; give json, or leave the flag out", flags[i].value)
	}
	planned = slices.Insert(planned, 1, sessionFlag, newSessionID())
	return adapter.Plan{Argv: planned, Enforcement: adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: watchedNote, Watchdog: true}}, nil
}

// newSessionID is a random version 4 UUID, the form Copilot CLI's session
// ids take.
func newSessionID() string {
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:])
}

// NewSession starts reading a run of the planned command.
func (Agent) NewSession(plan adapter.Plan) adapter.Session {
	session := &Session{home: homeDir(), keyed: providerKeyed(os.Getenv), planned: plan.Enforcement}
	for _, flag := range flagsOf(plan.Argv) {
		if flag.name == sessionFlag {
			session.named = flag.value
		}
	}
	return session
}

// providerKeyed says whether Copilot CLI's provider variables run it on the
// user's own provider key: a base URL, which turns the provider on, and a
// credential for it. Read from copilot help providers, 1.0.103.
func providerKeyed(getenv func(string) string) bool {
	if getenv("COPILOT_PROVIDER_BASE_URL") == "" {
		return false
	}
	for _, credential := range []string{"COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_API_KEY_COMMAND", "COPILOT_PROVIDER_BEARER_TOKEN"} {
		if getenv(credential) != "" {
			return true
		}
	}
	return false
}

// homeDir is where Copilot CLI keeps its state: COPILOT_HOME, or ~/.copilot.
func homeDir() string {
	if dir := os.Getenv("COPILOT_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".copilot")
}

// sessionPath is the session's events.jsonl under Copilot CLI's home.
func sessionPath(home, session string) string {
	return filepath.Join(home, "session-state", session, "events.jsonl")
}

// sessionWord is a session id this reader opens a directory by.
var sessionWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// Session reads one Copilot CLI run. Feed and Tail run on separate
// goroutines, so the lock covers everything below it.
type Session struct {
	home    string
	keyed   bool
	planned adapter.Enforcement
	// named is the session id the plan gave Copilot CLI.
	named string

	mu sync.Mutex
	// session is the id the stream's result line states.
	session string
	// skipped counts the stream lines that are not JSON objects with a type.
	skipped int
	// opened says the watch found the session file during the run.
	opened bool
	// problem is why the watch stopped holding the run to the budget.
	problem string
}

// refuse keeps the first reason the watch could not hold the run.
func (s *Session) refuse(problem string) {
	if s.problem == "" {
		s.problem = problem
	}
}

// earned is the grade the run kept: the plan's, unless the watch failed.
func (s *Session) earned() adapter.Enforcement {
	if s.planned.Watchdog && s.problem != "" {
		return adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: the budget was not enforced: " + s.problem}
	}
	return s.planned
}

// Feed reads one stdout line. Only the result line is read: it names the
// session.
func (s *Session) Feed(line []byte, _ time.Time) error {
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if json.Unmarshal(line, &event) != nil || event.Type == "" {
		s.skipped++
		return nil
	}
	if event.Type == "result" && event.SessionID != "" {
		s.session = event.SessionID
	}
	return nil
}

// Finish reads the session file of the session the result line names, or,
// when a stop left no result line, of the session the plan named.
func (s *Session) Finish() (adapter.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.session
	switch {
	case session == "" && s.named != "":
		session = s.named
	case session == "":
		return adapter.Report{}, errors.New("copilot: the stream held no result line naming the session: Copilot CLI did not finish the prompt, or it ran without --output-format json")
	case s.named != "" && session != s.named:
		s.refuse("Copilot CLI ran another session than the one --session-id named, so the watch read no file")
	}
	if s.planned.Watchdog && !s.opened {
		s.refuse("the session file did not appear during the run")
	}
	if !sessionWord.MatchString(session) {
		return adapter.Report{}, errors.New("copilot: the session id is not a plain word")
	}
	file, err := os.Open(sessionPath(s.home, session))
	if err != nil {
		return adapter.Report{}, fmt.Errorf("copilot: open the session file: %w", err)
	}
	defer file.Close()
	report, err := ReadEvents(file, s.keyed)
	if err != nil {
		return adapter.Report{}, err
	}
	if report.SessionID != session {
		return adapter.Report{}, errors.New("copilot: the session file's session.start names another session than the run's")
	}
	report.Enforcement = s.earned()
	if s.skipped > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s skipped: not a JSON object with a type", counted(s.skipped, "stream line")))
	}
	return report, nil
}

// counted is a count and its noun, plural past one.
func counted(count int, noun string) string {
	if count == 1 {
		return "1 " + noun + " was"
	}
	return fmt.Sprintf("%d %ss were", count, noun)
}

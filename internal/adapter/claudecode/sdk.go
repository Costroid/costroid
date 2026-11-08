package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
)

// SDKName is the costroid/agent tag of a program that runs Claude Code
// through the Claude Agent SDK.
const SDKName = "claude-code-sdk"

// sdkNote is the grade's note on an SDK run that holds no gateway key.
const sdkNote = "none: the budget sits in the program's maxBudgetUsd, which Costroid cannot see"

// costStateLine is the session file line where Claude Code states the
// session's per-model totals, the same figures a result's modelUsage holds.
const costStateLine = "cost-state"

// SDKAgent is a program that runs Claude Code through the Claude Agent SDK.
// The program's stdout is its own, so the run reads none of it. Every Claude
// Code session that started and ended while the program ran is the run's:
// in CI a session in any project directory, outside CI one whose working
// directory is the run's.
type SDKAgent struct {
	// ConfigDir holds Claude Code's sessions. Empty means CLAUDE_CONFIG_DIR,
	// then ~/.claude.
	ConfigDir string
	// Dir is the run's working directory. Empty means the process's own.
	Dir string
}

// Name returns the costroid/agent tag.
func (SDKAgent) Name() string {
	return SDKName
}

// Plan runs the program as it is. Its budget is the program's own
// maxBudgetUsd, which no command line states.
func (SDKAgent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	if len(argv) == 0 {
		return adapter.Plan{}, errors.New("claude-code-sdk: no command")
	}
	return adapter.Plan{
		Argv:        slices.Clone(argv),
		Enforcement: adapter.Enforcement{Grade: adapter.GradeNone, Note: sdkNote},
	}, nil
}

// NewSession starts reading a run. It reads the environment the program
// inherits once: the CI system, ENABLE_PROMPT_CACHING_1H and the API key.
func (a SDKAgent) NewSession(adapter.Plan) adapter.Session {
	return &SDKSession{
		configDir:    a.cli().configDir(),
		dir:          a.dir(),
		oneJob:       cicontext.Detect(os.Getenv) != cicontext.NoSystem,
		oneHourCache: os.Getenv(oneHourCacheVariable) != "",
		keyBilled:    os.Getenv(apiKeyVariable) != "",
	}
}

// Claim holds the config directory for one run, as a run of Claude Code
// does, so a second run there cannot count this run's sessions as its own.
func (a SDKAgent) Claim() (func(), error) {
	return a.cli().Claim()
}

// GatewayEnv points Claude Code at the gateway, with the run's key as its
// bearer token. No command line reaches the Claude Code the program starts,
// so the environment also empties every other variable that picks where its
// requests go.
func (a SDKAgent) GatewayEnv(base, key string) []string {
	env := a.cli().GatewayEnv(base, key)
	for _, variable := range routingVariables {
		if variable != baseURLVariable && variable != authTokenVariable {
			env = append(env, variable+"=")
		}
	}
	return env
}

// GatewayArgs is empty: the program's command line is not Claude Code's.
func (SDKAgent) GatewayArgs(string) []string {
	return nil
}

// GatewayBypass names the first settings file the Claude Code the program
// starts may read that sends its requests around the gateway: one whose env
// sets a variable that picks where requests go, or that names a key helper.
// The run cannot give that Claude Code a --settings of its own to override
// them, so it refuses the run instead. The command line itself is the
// program's and is not read.
func (a SDKAgent) GatewayBypass([]string) (string, error) {
	for _, path := range a.settingsFiles() {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("claude-code-sdk: under a gateway the run reads %s to see where Claude Code sends requests, and it could not be read: %w", path, err)
		}
		var settings struct {
			Env       map[string]json.RawMessage `json:"env"`
			KeyHelper json.RawMessage            `json:"apiKeyHelper"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("claude-code-sdk: under a gateway the run reads %s to see where Claude Code sends requests, and it is not a settings object: %w", path, err)
		}
		for _, variable := range routingVariables {
			if _, ok := settings.Env[variable]; ok {
				return path + " with " + variable + " in its env", nil
			}
		}
		if helper, _ := jsonString(settings.KeyHelper); helper != "" {
			return path + " with an apiKeyHelper", nil
		}
	}
	return "", nil
}

// settingsFiles are the files whose env Claude Code applies over the
// environment it starts with: the project's two in the run's directory and
// the user's in the config directory.
func (a SDKAgent) settingsFiles() []string {
	project := filepath.Join(a.dir(), ".claude")
	return []string{
		filepath.Join(project, "settings.json"),
		filepath.Join(project, "settings.local.json"),
		filepath.Join(a.cli().configDir(), "settings.json"),
	}
}

func (a SDKAgent) cli() Agent {
	return Agent{ConfigDir: a.ConfigDir}
}

// dir is the run's working directory with its links resolved, as Claude Code
// states its own.
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

// SDKSession reads the Claude Code sessions of one program's run.
type SDKSession struct {
	configDir string
	dir       string
	// oneJob is true inside a CI system, where the runner hosts this job alone.
	oneJob bool
	// oneHourCache is true when ENABLE_PROMPT_CACHING_1H is set.
	oneHourCache bool
	// keyBilled is true when the program inherits ANTHROPIC_API_KEY.
	keyBilled bool
	// billedThroughGateway is true when the run starts the program on a
	// gateway's key.
	billedThroughGateway bool
	// from and to are when the program ran.
	from, to time.Time
}

// Feed passes over a line of the program's stdout. The program prints what it
// chooses, so nothing in it is read as usage.
func (*SDKSession) Feed([]byte, time.Time) error {
	return nil
}

// RunWindow records when the program ran. Only a session inside it counts.
//
// The run takes both ends from the system's wall clock, the clock Claude Code
// dates each session line by. Claude Code states a line's time in whole
// milliseconds, cut down, so a line written in the run's first millisecond
// reads up to 1 ms before the run's start. The start is therefore taken down
// to its whole millisecond: that is the window's only slack, under 1 ms. The
// end needs none: a line's time never reads later than when it was written,
// and every line is written before the program ends.
func (s *SDKSession) RunWindow(start, end time.Time) {
	s.from, s.to = start.Truncate(time.Millisecond), end
}

// BilledThroughGateway reads the run as key-billed: the gateway bills every
// request per token on its own provider key.
func (s *SDKSession) BilledThroughGateway() {
	s.billedThroughGateway = true
}

// sdkRead is one session the run counts, with when it began.
type sdkRead struct {
	report adapter.Report
	first  time.Time
}

// Finish reads every session under the config directory's projects that
// started and ended inside the run. The earliest names the run, and each
// other one is reported under its own session id. A session that wrote
// inside the run and fails a rule is named in a warning, with the reason.
func (s *SDKSession) Finish() (adapter.Report, error) {
	files, err := filepath.Glob(filepath.Join(s.configDir, "projects", "*", "*.jsonl"))
	if err != nil {
		return adapter.Report{}, fmt.Errorf("claude-code-sdk: find session files: %w", err)
	}
	slices.Sort(files)
	counted := map[string]bool{}
	var reads []sdkRead
	var warnings []string
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		if !sessionIDPattern.MatchString(id) || writtenBefore(file, s.from) {
			continue
		}
		read, warning, err := s.readSession(file, id, counted)
		if err != nil {
			return adapter.Report{}, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if read != nil {
			reads = append(reads, *read)
		}
	}
	if len(reads) == 0 {
		return adapter.Report{
			CostBasis:  s.session("").costBasis(),
			CostSource: SourceSessionFile,
			Warnings:   append(warnings, fmt.Sprintf("no Claude Code session under %s started and ended while the program ran, so the program ran no query there, or gave Claude Code another CLAUDE_CONFIG_DIR", filepath.Join(s.configDir, "projects"))),
		}, nil
	}
	slices.SortStableFunc(reads, func(a, b sdkRead) int { return a.first.Compare(b.first) })
	report := reads[0].report
	for _, read := range reads[1:] {
		report.Nested = append(report.Nested, read.report)
		report.Unrecorded += read.report.Unrecorded
	}
	report.Warnings = append(warnings, report.Warnings...)
	for _, nested := range report.Nested {
		report.Warnings = append(report.Warnings, nested.Warnings...)
	}
	return report, nil
}

// readSession reads one candidate session file and its subagents. It returns
// nil when the session is not the run's, and a warning when it wrote inside
// the run and still does not count. A count past the bound refuses the run.
func (s *SDKSession) readSession(file, id string, counted map[string]bool) (*sdkRead, string, error) {
	inner := s.session(id)
	builder := newRequestBuilder(inner.unsplitWrite())
	facts, err := builder.read(file)
	switch {
	case errors.Is(err, errPastBound):
		return nil, "", fmt.Errorf("claude-code-sdk: session %s: %w", id, err)
	case err != nil:
		return nil, fmt.Sprintf("session %s wrote while the program ran and was not counted: %v", id, err), nil
	case facts.sidechain || facts.first.IsZero() || facts.first.After(s.to) || facts.last.Before(s.from):
		return nil, "", nil
	case facts.first.Before(s.from):
		return nil, fmt.Sprintf("session %s started before the program ran and wrote while it ran, so it holds earlier spend and was not counted", id), nil
	}
	last, err := readSubagents(builder, file, facts.last)
	switch {
	case errors.Is(err, errPastBound):
		return nil, "", fmt.Errorf("claude-code-sdk: session %s: %w", id, err)
	case err != nil:
		return nil, fmt.Sprintf("session %s was not counted: %v", id, err), nil
	case last.After(s.to):
		return nil, fmt.Sprintf("session %s started while the program ran and wrote after it ended, so it was not counted", id), nil
	case s.oneJob:
		// The runner hosts this job alone, so no directory rule applies.
	case s.dir == "":
		return nil, fmt.Sprintf("session %s started while the program ran, and the run's working directory is unknown, so it was not counted", id), nil
	case facts.cwd != s.dir:
		return nil, fmt.Sprintf("session %s started while the program ran in another working directory, so it was not counted", id), nil
	}
	inner.version, inner.first, inner.last = facts.version, facts.first, last
	complete := builder.unanswered() == 0 && facts.inFlight == 0
	report, err := inner.sdkReport(file, builder.requests(facts.first, last), facts, complete)
	if err != nil {
		return nil, "", err
	}
	kept := report.Requests[:0]
	for _, request := range report.Requests {
		if !counted[request.ID] {
			counted[request.ID] = true
			kept = append(kept, request)
		}
	}
	report.Requests = kept
	if len(report.Requests) == 0 && report.Unrecorded == 0 {
		return nil, "", nil
	}
	return &sdkRead{report: report, first: facts.first}, "", nil
}

// session is the reader of one session, holding what the run knows of how
// the program bills.
func (s *SDKSession) session(id string) *Session {
	inner := &Session{
		configDir:            s.configDir,
		sessionID:            id,
		oneJob:               s.oneJob,
		oneHourCache:         s.oneHourCache,
		billedThroughGateway: s.billedThroughGateway,
	}
	if s.keyBilled {
		// The source Claude Code states for a key in ANTHROPIC_API_KEY.
		inner.apiKeySource = apiKeyVariable
	}
	return inner
}

// sdkReport takes the session's requests when they agree with the totals its
// cost-state line states, and one set of rows per model from those totals
// when they do not. Totals that count requests outside the conversation
// agree only when the files are complete: they hold a response to every
// request they announce, and no file ends inside a line. A session with no
// cost-state line ended before Claude Code wrote one, and its requests are
// the only record of it.
func (s *Session) sdkReport(file string, requests []adapter.Request, facts fileFacts, complete bool) (adapter.Report, error) {
	report := adapter.Report{SessionID: s.sessionID, AgentVersion: s.version, CostBasis: s.costBasis(), CostSource: SourceSessionFile}
	totals, err := readCostState(file, s.sessionID)
	if err != nil {
		return report, fmt.Errorf("claude-code-sdk: session %s: %w", s.sessionID, err)
	}
	s.totals, s.sawResult = totals, totals != nil
	if err := s.checkResult(); err != nil {
		return report, err
	}
	s.routes = routesOf(requests)
	side := s.sideModels(requests)
	outside, agree := outsideConversation(requests, totals, side)
	switch {
	case totals == nil:
		reportInFlight(&report, facts.inFlight)
		report.Warnings = append(report.Warnings, fmt.Sprintf("session %s states no cost-state totals, so its file's requests are the only record of it", s.sessionID))
	case agree && (len(outside) == 0 || complete):
		more, err := s.sideRequests(side, outside, SourceSessionFile)
		if err != nil {
			return report, err
		}
		requests = append(requests, more...)
	default:
		perModel, err := s.perModel()
		if err != nil {
			return report, err
		}
		report.Warnings = append(report.Warnings, fmt.Sprintf("session %s: the requests in its file differ from the per-model totals Claude Code states for it, which also count requests made outside the conversation, so the totals were used, one set of rows per model", s.sessionID))
		report.Requests = perModel
		return report, nil
	}
	report.Warnings = append(report.Warnings, facts.warnings()...)
	report.Requests = requests
	return report, nil
}

// readCostState reads the per-model totals of the last cost-state line the
// session wrote, nil when it wrote none.
func readCostState(file, sessionID string) (map[string]modelUsage, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	var totals map[string]modelUsage
	for _, text := range strings.Split(string(data), "\n") {
		line, ok := parseSessionLine([]byte(text))
		if !ok || line.kind != costStateLine || line.sessionID != sessionID {
			continue
		}
		var state struct {
			ModelUsage json.RawMessage `json:"modelUsage"`
		}
		if err := json.Unmarshal([]byte(text), &state); err != nil {
			return nil, fmt.Errorf("the cost-state line could not be read: %w", err)
		}
		if totals, err = readModelUsage(state.ModelUsage); err != nil {
			return nil, fmt.Errorf("the cost-state line could not be read: %w", err)
		}
		if totals == nil {
			totals = map[string]modelUsage{}
		}
	}
	return totals, nil
}

// routesOf is the tier and region each model's requests in the files state,
// which price its whole-session rows as a stream's messages would.
func routesOf(requests []adapter.Request) map[string]*route {
	routes := map[string]*route{}
	for _, request := range requests {
		if routes[request.Model] == nil {
			routes[request.Model] = &route{tiers: map[string]bool{}, regions: map[string]bool{}}
		}
		routes[request.Model].tiers[request.ServiceTier] = true
		routes[request.Model].regions[request.Region] = true
	}
	return routes
}

// Package codex reads `codex exec --json` runs.
//
// The stream reports usage once, at turn.completed, as a running total for the
// thread, and one prompt is one turn. So the budget is watched in the rollout
// file Codex writes during the turn, which also gives per-request rows. The
// stream stays as the check on the file and as the record when it cannot be
// read.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	name     = "codex"
	provider = "openai"

	// UnknownModel stands in when neither the command nor the config names
	// a model. It prices at the catalog's fallback tier.
	UnknownModel = "unknown"

	costSourceStream  = "agent_stream"
	costSourceRollout = "agent_session_file"

	watchedNote = "wrapper-watchdog: priced from the rollout file during the turn, with a stop at the cap that requests in flight can pass"

	ephemeralFlag = "--ephemeral"

	versionTimeout = 10 * time.Second
)

var versionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*`)

// valueFlags are the exec flags that take the next argument as their value.
// A flag not listed is read as taking none.
var valueFlags = []string{
	"-m", "--model", "-c", "--config", "-p", "--profile", "-s", "--sandbox",
	"-C", "--cd", "-i", "--image", "-o", "--output-last-message",
	"--output-schema", "--add-dir", "--color", "--local-provider",
	"--enable", "--disable",
}

// Agent is the Codex CLI.
type Agent struct{}

// Name returns the costroid/agent tag.
func (Agent) Name() string {
	return name
}

// Plan adds --json. Codex has no budget flag, so the wrapper watches the
// rollout file, and a run that writes none has nothing holding it to the budget.
func (Agent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	subcommand := slices.Index(argv, "exec")
	if subcommand < 1 {
		return adapter.Plan{}, fmt.Errorf("codex: only `codex exec` reports usage on a stream, so the command needs exec")
	}
	if first, ok := firstPositional(argv[subcommand+1:]); ok && first == "resume" {
		return adapter.Plan{}, fmt.Errorf("codex: exec resume reports the whole thread's earlier spend, and the stream gives no way to subtract it; start a new thread")
	}
	planned := slices.Clone(argv)
	if !slices.Contains(argv, "--json") {
		planned = slices.Insert(planned, subcommand+1, "--json")
	}
	enforcement := adapter.Enforcement{Grade: adapter.GradeWrapperWatchdog, Note: watchedNote, Watchdog: true}
	if slices.Contains(argv, ephemeralFlag) {
		enforcement = unwatched(ephemeralFlag + " writes no rollout file")
	}
	return adapter.Plan{Argv: planned, Enforcement: enforcement}, nil
}

// firstPositional is the first argument that is neither a flag nor a flag's
// value. After "--", the next argument is positional whatever it reads.
func firstPositional(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		case slices.Contains(valueFlags, arg):
			i++
		case strings.HasPrefix(arg, "-") && arg != "-":
		default:
			return arg, true
		}
	}
	return "", false
}

// unwatched is the grade of a run nothing could watch, and why.
func unwatched(why string) adapter.Enforcement {
	return adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: the budget was not enforced: " + why}
}

// NewSession starts reading a run of the planned command.
func (Agent) NewSession(plan adapter.Plan) adapter.Session {
	return &Session{
		planned:   plan.Enforcement,
		home:      homeDir(),
		model:     modelOf(plan.Argv),
		costBasis: costBasisOf(os.Getenv("CODEX_API_KEY")),
		version:   versionOf(plan.Argv),
	}
}

func costBasisOf(apiKey string) focus.CostBasis {
	if apiKey != "" {
		return focus.Measured
	}
	return focus.ListEquivalent
}

func versionOf(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], "--version").Output()
	if err != nil {
		return ""
	}
	return versionPattern.FindString(string(out))
}

func modelOf(argv []string) string {
	return modelFrom(argv, filepath.Join(homeDir(), "config.toml"))
}

// modelFrom picks the model as Codex does: the model flag, then the selected
// profile's model, then a model set by a config override, then the config
// file's own. The rollout file's turn_context, when read, stands above all.
func modelFrom(argv []string, configPath string) string {
	args := modelFromArgs(argv)
	if args.model != "" {
		return args.model
	}
	config := readConfig(configPath)
	profile := args.profile
	if profile == "" {
		profile = config.profile
	}
	if model := config.profiles[profile]; profile != "" && model != "" {
		return model
	}
	if args.override != "" {
		return args.override
	}
	if config.model != "" {
		return config.model
	}
	return UnknownModel
}

// argsModel is what a command line says about the model. A later flag
// replaces an earlier one.
type argsModel struct {
	// model is the -m or --model value.
	model string
	// override is a model set by -c or --config.
	override string
	// profile is the -p or --profile value, or a profile set by -c.
	profile string
}

// modelFromArgs reads -m, --model, -p, --profile, -c and --config, each with
// its value in the next argument or after "=". A -c value is a TOML key and
// value, with or without spaces around "=" and with either quote.
func modelFromArgs(argv []string) argsModel {
	var found argsModel
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		flag, value, inline := strings.Cut(arg, "=")
		if !inline {
			if !slices.Contains([]string{"-m", "--model", "-p", "--profile", "-c", "--config"}, arg) || i+1 == len(argv) {
				continue
			}
			i++
			value = argv[i]
		}
		switch flag {
		case "-m", "--model":
			found.model = value
		case "-p", "--profile":
			found.profile = value
		case "-c", "--config":
			key, setting, ok := strings.Cut(value, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "model":
				found.override = tomlString(setting)
			case "profile":
				found.profile = tomlString(setting)
			}
		}
	}
	return found
}

// tomlString reads a TOML value as a string: a basic or literal string, or,
// as Codex reads an override that is not TOML, the bare text.
func tomlString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return value
	}
	switch value[0] {
	case '"':
		if end := closingQuote(value); end > 0 {
			if unquoted, err := strconv.Unquote(value[:end+1]); err == nil {
				return unquoted
			}
			return value[1:end]
		}
	case '\'':
		if end := strings.IndexByte(value[1:], '\''); end >= 0 {
			return value[1 : end+1]
		}
	}
	if comment := strings.IndexByte(value, '#'); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}
	return value
}

// closingQuote is the index of the quote that ends a basic string, or -1.
func closingQuote(value string) int {
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// homeDir is where Codex keeps its config and rollout files.
func homeDir() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexConfig is what config.toml says about the model.
type codexConfig struct {
	model   string
	profile string
	// profiles holds each [profiles.<name>] table's model.
	profiles map[string]string
}

// readConfig reads the top-level model and profile keys and each profile's
// model, as plain or quoted keys, under a table header or dotted. A file that
// cannot be read says nothing.
func readConfig(path string) codexConfig {
	config := codexConfig{profiles: map[string]string{}}
	file, err := os.Open(path)
	if err != nil {
		return config
	}
	defer file.Close()
	var table []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			table = tomlTable(line)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		path := append(slices.Clone(table), tomlKey(key)...)
		switch {
		case slices.Equal(path, []string{"model"}):
			config.model = tomlString(value)
		case slices.Equal(path, []string{"profile"}):
			config.profile = tomlString(value)
		case len(path) == 3 && path[0] == "profiles" && path[2] == "model":
			config.profiles[path[1]] = tomlString(value)
		}
	}
	return config
}

// tomlTable reads a [table] header. An array of tables reads as no table the
// reader knows.
func tomlTable(line string) []string {
	if strings.HasPrefix(line, "[[") {
		return []string{"[["}
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return []string{"["}
	}
	return tomlKey(line[1:end])
}

// tomlKey splits a dotted key, with each part bare or quoted.
func tomlKey(key string) []string {
	var parts []string
	for _, part := range strings.Split(key, ".") {
		parts = append(parts, strings.Trim(strings.TrimSpace(part), `"'`))
	}
	return parts
}

type usage struct {
	InputTokens     int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheWriteInput int64 `json:"cache_write_input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningOutput int64 `json:"reasoning_output_tokens"`
}

// field is one count and its name on the stream.
type field struct {
	name  string
	value int64
}

// fields lists the counts by their names on the stream.
func (u usage) fields() []field {
	return []field{
		{"input_tokens", u.InputTokens},
		{"cached_input_tokens", u.CachedInput},
		{"cache_write_input_tokens", u.CacheWriteInput},
		{"output_tokens", u.OutputTokens},
		{"reasoning_output_tokens", u.ReasoningOutput},
	}
}

// negative names the first count below zero, if any.
func (u usage) negative() error {
	for _, count := range u.fields() {
		if count.value < 0 {
			return fmt.Errorf("%s is %d, below zero", count.name, count.value)
		}
	}
	return nil
}

// plus adds two usages of counts at or above zero. It reports false when a
// count is below zero or a sum passes the largest count an int64 holds.
func (u usage) plus(other usage) (usage, bool) {
	addends := other.fields()
	for i, count := range u.fields() {
		addend := addends[i].value
		if count.value < 0 || addend < 0 || count.value > math.MaxInt64-addend {
			return usage{}, false
		}
	}
	return usage{
		InputTokens:     u.InputTokens + other.InputTokens,
		CachedInput:     u.CachedInput + other.CachedInput,
		CacheWriteInput: u.CacheWriteInput + other.CacheWriteInput,
		OutputTokens:    u.OutputTokens + other.OutputTokens,
		ReasoningOutput: u.ReasoningOutput + other.ReasoningOutput,
	}, true
}

// minus is the usage since an earlier running total. Both hold counts at or
// above zero, so no field overflows; a field that fell is negative, which
// tokens refuses.
func (u usage) minus(earlier usage) usage {
	return usage{
		InputTokens:     u.InputTokens - earlier.InputTokens,
		CachedInput:     u.CachedInput - earlier.CachedInput,
		CacheWriteInput: u.CacheWriteInput - earlier.CacheWriteInput,
		OutputTokens:    u.OutputTokens - earlier.OutputTokens,
		ReasoningOutput: u.ReasoningOutput - earlier.ReasoningOutput,
	}
}

// assumedWrite is the class a request's writes took. Codex reports one total
// with no lifetime, so every write is assumed to be OpenAI's default.
func assumedWrite(tokens adapter.Tokens) focus.TokenClass {
	if tokens.CacheWrite30m == 0 {
		return ""
	}
	return focus.CacheWrite30m
}

// tokens maps one turn's usage. Codex input holds cached tokens and cache
// writes, so uncached input is what remains. Writes have no reported lifetime;
// OpenAI's default is 30 minutes. A count below zero is refused: no source
// counts fewer than no tokens, and it would price as a negative cost.
func (u usage) tokens() (adapter.Tokens, error) {
	if err := u.negative(); err != nil {
		return adapter.Tokens{}, fmt.Errorf("codex: %w", err)
	}
	if u.CachedInput > u.InputTokens || u.CacheWriteInput > u.InputTokens-u.CachedInput {
		return adapter.Tokens{}, fmt.Errorf("codex: input %d is below cached %d plus cache writes %d", u.InputTokens, u.CachedInput, u.CacheWriteInput)
	}
	tokens := adapter.Tokens{
		InputUncached:       u.InputTokens - u.CachedInput - u.CacheWriteInput,
		CacheRead:           u.CachedInput,
		CacheWrite30m:       u.CacheWriteInput,
		Output:              u.OutputTokens,
		ReasoningOutput:     u.ReasoningOutput,
		CachedInputReported: u.CachedInput,
	}
	if err := tokens.Check(); err != nil {
		return adapter.Tokens{}, fmt.Errorf("codex: %w", err)
	}
	return tokens, nil
}

// Session reads one Codex run. Feed and Tail run on separate goroutines, so
// the lock covers everything below it.
type Session struct {
	planned   adapter.Enforcement
	home      string
	costBasis focus.CostBasis

	mu            sync.Mutex
	model         string
	version       string
	threadID      string
	threadStarted time.Time
	turnStart     time.Time
	turnOpen      bool
	// turnsStarted counts the stream's turn.started lines.
	turnsStarted int
	total        usage
	turns        []adapter.Request
	// unreadTurn is why a turn.completed line could not be taken. The line
	// holds the run's figures, so the run's reading stops at Finish.
	unreadTurn error
	// skipped counts the lines whose shape this reader does not know.
	skipped  int
	warnings []string
	log      rolloutLog
	// rolloutModified is when the thread's rollout file was last written, as
	// it stood when the run first found it.
	rolloutModified time.Time
	// runStart is when the agent's process started. Zero means not known.
	runStart  time.Time
	subagents subagents
}

// RunWindow records when the agent's process ran.
func (s *Session) RunWindow(start, _ time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runStart = start
}

// Feed reads one stdout line. Its type is read first, then the fields that
// type carries, so a field this reader never uses, such as message, can take
// any shape. A line whose shape this reader does not know is skipped and
// counted. A turn.completed line that cannot be read stops the run's reading
// at Finish, because it holds the run's figures.
func (s *Session) Feed(line []byte, at time.Time) error {
	if !json.Valid(line) {
		return fmt.Errorf("codex: stream line is not JSON")
	}
	var envelope struct {
		Type json.RawMessage `json:"type"`
	}
	kind, ok := "", false
	if json.Unmarshal(line, &envelope) == nil {
		kind, ok = jsonString(envelope.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		s.skipped++
		return nil
	}
	switch kind {
	case "thread.started":
		var started struct {
			ThreadID json.RawMessage `json:"thread_id"`
		}
		id, ok := "", json.Unmarshal(line, &started) == nil
		if ok {
			id, ok = jsonString(started.ThreadID)
		}
		if !ok || id == "" {
			s.skipped++
			return nil
		}
		s.threadID = id
		s.threadStarted = at
		s.turnStart = at
	case "turn.started":
		s.turnStart = at
		s.turnOpen = true
		s.turnsStarted++
	case "turn.completed":
		s.readTurn(line, at)
	case "turn.failed", "error":
		s.warnings = append(s.warnings, "codex reported "+kind+": a failed turn's usage is not on the stream")
	}
	return nil
}

// jsonString reads a JSON string. Absent, null and any other value read as
// not a string.
func jsonString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// readTurn takes a turn.completed line. The first that cannot be read is kept,
// and later turns are not taken, because each is a difference from the last.
func (s *Session) readTurn(line []byte, at time.Time) {
	if s.unreadTurn != nil {
		return
	}
	var completed struct {
		Usage usage `json:"usage"`
	}
	if err := json.Unmarshal(line, &completed); err != nil {
		s.unreadTurn = fmt.Errorf("codex: turn.completed could not be read: %w", err)
		return
	}
	if err := s.completeTurn(completed.Usage, at); err != nil {
		s.unreadTurn = fmt.Errorf("codex: turn.completed could not be read: %w", err)
	}
}

// completeTurn takes one turn's usage. The turn's whole input bounds each of
// its requests' from above, so it selects the context tier: within the lowest
// tier, every request is in it. The usage is a running total, so a count
// that fell is refused.
func (s *Session) completeTurn(total usage, at time.Time) error {
	if err := total.negative(); err != nil {
		return err
	}
	turn := total.minus(s.total)
	tokens, err := turn.tokens()
	if err != nil {
		return fmt.Errorf("turn %d against the running total before it: %w", len(s.turns)+1, err)
	}
	s.total = total
	s.turnOpen = false
	s.turns = append(s.turns, adapter.Request{
		ID:                 s.threadID + ":turn:" + strconv.Itoa(len(s.turns)+1),
		Provider:           provider,
		Model:              s.model,
		Start:              s.turnStart,
		End:                at,
		ServiceTier:        catalog.TierStandard,
		Region:             catalog.RegionGlobal,
		ContextInputTokens: turn.InputTokens,
		Grain:              adapter.PerTurn,
		Tokens:             tokens,
		AssumedWrite:       assumedWrite(tokens),
	})
	return nil
}

// noThread is the error of a stream that never started a thread.
var noThread = "codex: the stream held no thread.started event. Codex did not start a thread, often because it is not signed in or the version is older than " + recordedVersion

// Finish returns the run's usage and the grade it earned. Per-request rows
// from the rollout files stand unless the stream's total contradicts them. A
// run with neither source reported nothing, and whatever it spent is unknown.
func (s *Session) Finish() (adapter.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID == "" {
		return adapter.Report{}, errors.New(noThread)
	}
	if s.unreadTurn != nil {
		return adapter.Report{}, s.unreadTurn
	}
	predates := s.rolloutPredates()
	if predates {
		s.log.refuse(predatesProblem)
	}
	s.judgeRollout()
	if s.log.model != "" {
		for i := range s.turns {
			s.turns[i].Model = s.log.model
		}
	}
	report := adapter.Report{
		SessionID:    s.threadID,
		AgentVersion: s.version,
		CostBasis:    s.costBasis,
		CostSource:   costSourceStream,
		Requests:     s.turns,
		Warnings:     s.warnings,
		Enforcement:  s.earned(),
	}
	insideATurn := s.turnOpen || len(s.turns) == 0
	if requests := s.requests(); len(requests) > 0 && (s.log.problem == "" || len(s.turns) == 0) {
		report.CostSource = costSourceRollout
		report.Requests = requests
	}
	if insideATurn && s.log.opened {
		s.reportInFlight(&report)
	}
	if note := s.versionNote(report.CostSource == costSourceRollout); note != "" {
		report.Warnings = append(report.Warnings, note)
	}
	if predates {
		report.Warnings = append(report.Warnings, predatesWarning)
		for i := range report.Requests {
			report.Requests[i].Note = predatesNote
		}
	}
	if len(report.Requests) == 0 {
		report.Warnings = append(report.Warnings, "no turn completed, so neither the stream nor a rollout file reported usage")
	}
	if s.skipped > 0 {
		report.Warnings = append(report.Warnings, skippedLines(s.skipped))
	}
	return report, nil
}

func skippedLines(count int) string {
	if count == 1 {
		return "1 stream line was skipped: its shape is not one this reader knows, and it is not a turn.completed line"
	}
	return fmt.Sprintf("%d stream lines were skipped: their shape is not one this reader knows, and none is a turn.completed line", count)
}

// The words for a thread whose rollout file holds spend from before the run.
const (
	predatesProblem = "the thread's rollout file was created before the run started"
	predatesWarning = "the thread's rollout file was created before the run started, so the thread holds an earlier run's spend: this run's own total is unknown, and the rows may hold the earlier spend"
	predatesNote    = "the thread's rollout file was created before the run started, so this entry may hold an earlier run's spend, and the run's own total is unknown"
)

// predatesSlack is how far before the run's start a new rollout file's write
// time may read, for a clock or file system that rounds.
const predatesSlack = time.Second

// rolloutPredates says whether the thread's rollout file was created before
// the run: it holds more tasks than the stream started turns, or it was last
// written before the run started when the run first found it.
func (s *Session) rolloutPredates() bool {
	if !s.log.opened {
		return false
	}
	if s.log.tasks > max(1, s.turnsStarted) {
		return true
	}
	return !s.runStart.IsZero() && !s.rolloutModified.IsZero() && s.rolloutModified.Before(s.runStart.Add(-predatesSlack))
}

// versionNote names a Codex version no recorded fixture covers, and what came
// of reading its rollout file.
func (s *Session) versionNote(read bool) string {
	version := s.log.version
	if version == "" || slices.Contains(recordedVersions, version) {
		return ""
	}
	if read {
		return fmt.Sprintf("Codex %s is not a recorded version; the rollout file passed every check", version)
	}
	if s.log.problem != "" {
		return fmt.Sprintf("Codex %s is not a recorded version, and its rollout file failed a check: %s", version, s.log.problem)
	}
	return ""
}

// reportInFlight says what a run that ended inside a turn lost. Codex writes a
// request's usage only when the request completes, so a request the files show
// sent and unsettled reached no row. Files that stopped being read cannot say.
func (s *Session) reportInFlight(report *adapter.Report) {
	if s.log.problem != "" {
		if report.CostSource == costSourceRollout {
			report.Warnings = append(report.Warnings, "the run ended inside a turn: the rows stop at the last request the rollout file settled, and a request in flight is not counted")
		}
		return
	}
	count, last := s.subagents.inFlight()
	if !s.log.sent.IsZero() {
		count++
		last = later(last, s.log.sent)
	}
	report.Unrecorded = count
	switch count {
	case 0:
		report.Warnings = append(report.Warnings, "the run ended inside a turn, and the rollout files show no model request in flight, so the rows hold every request they show sent")
	case 1:
		report.Warnings = append(report.Warnings, fmt.Sprintf("the run ended inside a turn with 1 model request in flight, sent at %s: Codex writes usage only when a request completes, so that request in flight is not counted and its tokens reach no row", last.UTC().Format(time.RFC3339Nano)))
	default:
		report.Warnings = append(report.Warnings, fmt.Sprintf("the run ended inside a turn with %d model requests in flight, the last sent at %s: Codex writes usage only when a request completes, so those requests in flight are not counted and their tokens reach no row", count, last.UTC().Format(time.RFC3339Nano)))
	}
}

// judgeRollout decides whether the files held the run. The totals are compared
// only when the stream closed its last turn, because a stopped turn has no
// stream total to compare. Whether the stream's total holds the subagents'
// spend is not recorded, so either sum agrees.
func (s *Session) judgeRollout() {
	own := s.log.settled
	switch {
	case !s.planned.Watchdog:
	case !s.log.opened:
		s.log.refuse("no rollout file appeared for this thread")
	case !s.turnOpen && len(s.turns) > 0 && own != s.total:
		withSubagents, ok := s.subagents.settled()
		if ok {
			withSubagents, ok = own.plus(withSubagents)
		}
		if !ok {
			s.log.refuse("the rollout files' totals pass the largest count this reader holds")
		} else if withSubagents != s.total {
			s.log.refuse("the rollout file's total differs from the stream's")
		}
	}
}

func (s *Session) earned() adapter.Enforcement {
	if s.planned.Watchdog && s.log.problem != "" {
		return unwatched(s.log.problem)
	}
	return s.planned
}

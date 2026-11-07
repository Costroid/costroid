package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
)

// tailInterval is how often the rollout file is read during a run.
const tailInterval = 50 * time.Millisecond

// recordedVersion is the Codex version the recipes pin.
const recordedVersion = "0.164.0-alpha.11.1"

// recordedVersions lists the Codex versions with a recorded rollout fixture.
// The format is undocumented, so any version's file is read only while it
// keeps the shape the reader checks line by line: a session_meta that names a
// version before any usage, a running total that never falls and moves by
// each request, requests whose counts add up, and, at the end, a total that
// agrees with the stream's. Another version reads with a note.
var recordedVersions = []string{recordedVersion, "0.167.0"}

var threadPattern = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

type rolloutEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID         string `json:"id"`
	CLIVersion string `json:"cli_version"`
	// ThreadSource and SessionID mark a thread another started, and name that
	// thread. ThreadSource stays raw, so a shape other than a string is no
	// subagent rather than an error.
	ThreadSource json.RawMessage `json:"thread_source"`
	SessionID    string          `json:"session_id"`
}

type turnContext struct {
	Model string `json:"model"`
}

type usageRecord struct {
	ResponseID string `json:"response_id"`
	Usage      usage  `json:"usage"`
	Thread     usage  `json:"thread_token_usage"`
}

type eventMessage struct {
	Type string     `json:"type"`
	Info *tokenInfo `json:"info"`
}

type tokenInfo struct {
	Total usage `json:"total_token_usage"`
	Last  usage `json:"last_token_usage"`
}

// rolloutLog is what one thread's file has said so far. A problem stops the
// reading, and the run keeps what had settled before it.
type rolloutLog struct {
	// thread names the stand-in id of a request the file gives no id.
	thread  string
	opened  bool
	version string
	model   string
	start   time.Time
	// tasks counts the task_started events, one per turn.
	tasks    int
	settled  usage
	requests []adapter.Request
	problem  string
	// sent is when the file last showed input leave for the model, at the
	// task's start or as a tool's output, with no request settled since. Zero
	// means no request is in flight.
	sent time.Time
}

// toolOutputs are the response items that carry a tool's result back to the
// model, which Codex sends as the next request.
var toolOutputs = []string{"function_call_output", "custom_tool_call_output"}

func (l *rolloutLog) refuse(problem string) {
	if l.problem == "" {
		l.problem = problem
	}
}

// Tail follows the thread's rollout file until ctx ends, then reads it once
// more for the lines the agent wrote last.
func (s *Session) Tail(ctx context.Context, settled func([]adapter.Request)) {
	var file rolloutFile
	defer file.close()
	defer s.subagents.close()
	ticker := time.NewTicker(tailInterval)
	defer ticker.Stop()
	for {
		if requests, working := s.follow(&file); working {
			settled(requests)
		}
		select {
		case <-ctx.Done():
			s.follow(&file)
			return
		case <-ticker.C:
		}
	}
}

// follow reads what the thread's file and its subagents' files gained. It
// reports the run's requests so far, and true when a new one settled and the
// stream has not closed the turn.
func (s *Session) follow(file *rolloutFile) ([]adapter.Request, bool) {
	thread, since := s.threadStart()
	if file.file == nil && !file.open(s.home, thread) {
		return nil, false
	}
	lines, err := file.lines()
	gained := s.subagents.read(s.home, thread, since)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.log.opened {
		s.rolloutModified = file.modified
	}
	s.log.opened = true
	if err != nil {
		s.log.refuse("the rollout file could not be read")
	}
	before := len(s.requests())
	for _, line := range lines {
		s.applyRollout(line)
	}
	for _, read := range gained {
		s.applySubagent(read)
	}
	all := s.requests()
	working := s.turnOpen || len(s.turns) == 0
	return all, working && len(all) > before
}

func (s *Session) threadStart() (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID, s.threadStarted
}

// requests are the thread's own requests, then each subagent's.
func (s *Session) requests() []adapter.Request {
	all := slices.Clone(s.log.requests)
	for _, sub := range s.subagents.files {
		all = append(all, sub.log.requests...)
	}
	return all
}

// applyRollout reads one line of the thread's own file.
func (s *Session) applyRollout(line []byte) {
	s.log.thread = s.threadID
	s.log.apply(line, s.model)
	if s.log.version != "" {
		s.version = s.log.version
	}
}

// applySubagent reads what one subagent's file gained. A subagent's file that
// cannot be read leaves its spend unknown, so the run's reading stops with it.
func (s *Session) applySubagent(read subagentRead) {
	log := &read.sub.log
	if read.err != nil {
		log.refuse("the rollout file could not be read")
	}
	for _, line := range read.lines {
		log.apply(line, s.model)
	}
	if log.problem != "" {
		s.log.refuse("subagent thread " + log.thread + ": " + log.problem)
	}
}

// apply reads one line. Only the lines named here are parsed past their
// envelope, and of a response item only its type, so nothing a prompt or a
// tool wrote is ever decoded.
func (l *rolloutLog) apply(line []byte, model string) {
	if l.problem != "" {
		return
	}
	var entry rolloutEntry
	err := json.Unmarshal(line, &entry)
	if err == nil {
		err = l.applyEntry(entry, model)
	}
	if err != nil {
		l.refuse("a rollout line could not be parsed")
	}
}

func (l *rolloutLog) applyEntry(entry rolloutEntry, model string) error {
	switch entry.Type {
	case "session_meta":
		var meta sessionMeta
		if err := json.Unmarshal(entry.Payload, &meta); err != nil {
			return err
		}
		l.applyMeta(meta)
	case "turn_context":
		var turn turnContext
		if err := json.Unmarshal(entry.Payload, &turn); err != nil {
			return err
		}
		l.model = turn.Model
	case "token_usage_record":
		var record usageRecord
		if err := json.Unmarshal(entry.Payload, &record); err != nil {
			return err
		}
		l.settle(record.Thread, record.Usage, record.ResponseID, entry.Timestamp, model)
	case "event_msg":
		var message eventMessage
		if err := json.Unmarshal(entry.Payload, &message); err != nil {
			return err
		}
		l.applyEvent(message, entry.Timestamp, model)
	case "response_item":
		var item struct {
			Type string `json:"type"`
		}
		// An item is never usage, so one that does not decode stops nothing.
		if json.Unmarshal(entry.Payload, &item) == nil && slices.Contains(toolOutputs, item.Type) {
			l.sent = entry.Timestamp
		}
	}
	return nil
}

func (l *rolloutLog) applyMeta(meta sessionMeta) {
	l.version = meta.CLIVersion
}

func (l *rolloutLog) applyEvent(message eventMessage, at time.Time, model string) {
	switch message.Type {
	case "task_started":
		l.tasks++
		l.start = at
		l.sent = at
	case "token_count":
		if message.Info != nil {
			l.settle(message.Info.Total, message.Info.Last, "", at, model)
		}
	}
}

// settle takes one request. A token_usage_record and its token_count state the
// same running total, and a token_count also repeats for rate-limit updates, so
// only a total that moved is new. Whichever line comes first settles it.
func (l *rolloutLog) settle(total, last usage, id string, at time.Time, model string) {
	if l.version == "" {
		l.refuse("the rollout file names no Codex version")
		return
	}
	if total.negative() != nil {
		l.refuse("a rollout running total holds a count below zero")
		return
	}
	if last.negative() != nil {
		l.refuse("a rollout usage record holds a count below zero")
		return
	}
	if total == l.settled {
		return
	}
	if total.minus(l.settled) != last {
		l.refuse("the rollout file's running total did not move by its last request")
		return
	}
	tokens, err := last.tokens()
	if err != nil {
		l.refuse("a rollout usage record does not add up")
		return
	}
	l.settled = total
	l.requests = append(l.requests, l.request(id, last, tokens, at, model))
	l.start = at
	l.sent = time.Time{}
}

func (l *rolloutLog) request(id string, last usage, tokens adapter.Tokens, at time.Time, model string) adapter.Request {
	if id == "" {
		id = l.thread + ":request:" + strconv.Itoa(len(l.requests)+1)
	}
	if l.model != "" {
		model = l.model
	}
	start := l.start
	if start.IsZero() {
		start = at
	}
	return adapter.Request{
		ID:                 id,
		Provider:           provider,
		Model:              model,
		Start:              start,
		End:                at,
		ServiceTier:        catalog.TierStandard,
		Region:             catalog.RegionGlobal,
		ContextInputTokens: last.InputTokens,
		Grain:              adapter.PerRequest,
		Tokens:             tokens,
		AssumedWrite:       assumedWrite(tokens),
	}
}

// rolloutFile reads a growing file by whole lines.
type rolloutFile struct {
	file    *os.File
	partial []byte
	// modified is when the file was last written, as it stood when opened.
	modified time.Time
}

// open finds sessions/<year>/<month>/<day>/rollout-<time>-<thread>.jsonl. The
// directories follow the local date, so the search covers every day.
func (f *rolloutFile) open(home, thread string) bool {
	if home == "" || !threadPattern.MatchString(thread) {
		return false
	}
	found, err := fs.Glob(os.DirFS(home), path.Join("sessions", "*", "*", "*", "rollout-*-"+thread+".jsonl"))
	if err != nil || len(found) == 0 {
		return false
	}
	file, err := os.Open(filepath.Join(home, filepath.FromSlash(found[0])))
	if err != nil {
		return false
	}
	if info, err := file.Stat(); err == nil {
		f.modified = info.ModTime()
	}
	f.file = file
	return true
}

func (f *rolloutFile) lines() ([][]byte, error) {
	data, err := io.ReadAll(f.file)
	f.partial = append(f.partial, data...)
	end := bytes.LastIndexByte(f.partial, '\n')
	if end < 0 {
		return nil, err
	}
	whole := bytes.Clone(f.partial[:end])
	f.partial = slices.Delete(f.partial, 0, end+1)
	return bytes.Split(whole, []byte("\n")), err
}

func (f *rolloutFile) close() {
	if f.file != nil {
		// A file opened for reading has nothing left to flush.
		_ = f.file.Close()
	}
}

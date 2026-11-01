package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// supportedVersions lists the Codex versions with a recorded rollout fixture.
// The format is undocumented, so another version's file is not read.
var supportedVersions = []string{"0.164.0-alpha.11.1"}

var threadPattern = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

type rolloutEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	CLIVersion string `json:"cli_version"`
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

// rolloutLog is what the file has said so far. A problem stops the reading,
// and the run keeps what had settled before it.
type rolloutLog struct {
	opened   bool
	version  string
	model    string
	start    time.Time
	settled  usage
	requests []adapter.Request
	problem  string
}

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

// follow reads what the file gained. It reports the requests so far, and true
// when a new one settled and the stream has not closed the turn.
func (s *Session) follow(file *rolloutFile) ([]adapter.Request, bool) {
	if file.file == nil && !file.open(s.home, s.thread()) {
		return nil, false
	}
	lines, err := file.lines()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log.opened = true
	if err != nil {
		s.log.refuse("the rollout file could not be read")
	}
	before := len(s.log.requests)
	for _, line := range lines {
		s.applyRollout(line)
	}
	working := s.turnOpen || len(s.turns) == 0
	return slices.Clone(s.log.requests), working && len(s.log.requests) > before
}

func (s *Session) thread() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID
}

// applyRollout reads one line. Only the lines named here are parsed past
// their envelope, so nothing a prompt or a tool wrote is ever decoded.
func (s *Session) applyRollout(line []byte) {
	if s.log.problem != "" {
		return
	}
	var entry rolloutEntry
	err := json.Unmarshal(line, &entry)
	if err == nil {
		err = s.applyEntry(entry)
	}
	if err != nil {
		s.log.refuse("a rollout line could not be parsed")
	}
}

func (s *Session) applyEntry(entry rolloutEntry) error {
	switch entry.Type {
	case "session_meta":
		var meta sessionMeta
		if err := json.Unmarshal(entry.Payload, &meta); err != nil {
			return err
		}
		s.applyMeta(meta)
	case "turn_context":
		var turn turnContext
		if err := json.Unmarshal(entry.Payload, &turn); err != nil {
			return err
		}
		s.log.model = turn.Model
	case "token_usage_record":
		var record usageRecord
		if err := json.Unmarshal(entry.Payload, &record); err != nil {
			return err
		}
		s.settle(record.Thread, record.Usage, record.ResponseID, entry.Timestamp)
	case "event_msg":
		var message eventMessage
		if err := json.Unmarshal(entry.Payload, &message); err != nil {
			return err
		}
		s.applyEvent(message, entry.Timestamp)
	}
	return nil
}

func (s *Session) applyMeta(meta sessionMeta) {
	s.log.version = meta.CLIVersion
	s.version = meta.CLIVersion
	if !slices.Contains(supportedVersions, meta.CLIVersion) {
		s.log.refuse(fmt.Sprintf("no recorded fixture covers Codex %q, so its rollout file is not read", meta.CLIVersion))
	}
}

func (s *Session) applyEvent(message eventMessage, at time.Time) {
	switch message.Type {
	case "task_started":
		s.log.start = at
	case "token_count":
		if message.Info != nil {
			s.settle(message.Info.Total, message.Info.Last, "", at)
		}
	}
}

// settle takes one request. A token_usage_record and its token_count state the
// same running total, and a token_count also repeats for rate-limit updates, so
// only a total that moved is new. Whichever line comes first settles it.
func (s *Session) settle(total, last usage, id string, at time.Time) {
	if s.log.version == "" {
		s.log.refuse("the rollout file names no Codex version")
		return
	}
	if total == s.log.settled {
		return
	}
	if total.minus(s.log.settled) != last {
		s.log.refuse("the rollout file's running total did not move by its last request")
		return
	}
	tokens, err := last.tokens()
	if err != nil {
		s.log.refuse("a rollout usage record does not add up")
		return
	}
	s.log.settled = total
	s.log.requests = append(s.log.requests, s.request(id, last, tokens, at))
	s.log.start = at
}

func (s *Session) request(id string, last usage, tokens adapter.Tokens, at time.Time) adapter.Request {
	if id == "" {
		id = s.threadID + ":request:" + strconv.Itoa(len(s.log.requests)+1)
	}
	model := s.log.model
	if model == "" {
		model = s.model
	}
	start := s.log.start
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
	}
}

// rolloutFile reads a growing file by whole lines.
type rolloutFile struct {
	file    *os.File
	partial []byte
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

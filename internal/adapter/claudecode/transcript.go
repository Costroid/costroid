package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
)

// sessionEnvelope is a session file line's own fields, each kept raw so that
// a line of any shape decodes.
type sessionEnvelope struct {
	Type        json.RawMessage `json:"type"`
	SessionID   json.RawMessage `json:"sessionId"`
	IsSidechain json.RawMessage `json:"isSidechain"`
	CWD         json.RawMessage `json:"cwd"`
	Version     json.RawMessage `json:"version"`
	ID          json.RawMessage `json:"id"`
	RequestID   json.RawMessage `json:"requestId"`
	RequestRef  json.RawMessage `json:"requestRef"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

// sessionLine is what a session file line states about itself. A field that
// is absent or of another type reads as empty.
type sessionLine struct {
	kind       string
	sessionID  string
	sidechain  bool
	cwd        string
	version    string
	id         string
	requestRef string
	// requestID is kept raw, so a value that is not text is told from none.
	requestID json.RawMessage
	// at is zero when the line states no time that reads as RFC 3339.
	at      time.Time
	message json.RawMessage
}

// parseSessionLine reads a line's envelope. It is false for a line that is
// not a JSON object, or that states no type as text.
func parseSessionLine(raw []byte) (sessionLine, bool) {
	var envelope sessionEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return sessionLine{}, false
	}
	kind, ok := jsonString(envelope.Type)
	if !ok {
		return sessionLine{}, false
	}
	text := func(raw json.RawMessage) string {
		value, _ := jsonString(raw)
		return value
	}
	line := sessionLine{
		kind:       kind,
		sessionID:  text(envelope.SessionID),
		sidechain:  string(envelope.IsSidechain) == "true",
		cwd:        text(envelope.CWD),
		version:    text(envelope.Version),
		id:         text(envelope.ID),
		requestRef: text(envelope.RequestRef),
		requestID:  envelope.RequestID,
		message:    envelope.Message,
	}
	if at, err := time.Parse(time.RFC3339Nano, text(envelope.Timestamp)); err == nil {
		line.at = at
	}
	return line, true
}

// transcript is what the run's own session files hold.
type transcript struct {
	// path is the main session file. Empty means none was found.
	path string
	// cwd is the working directory the main session file states first.
	cwd      string
	requests []adapter.Request
	// unanswered counts the requests the files announce and hold no response
	// to.
	unanswered int
	fileCounts
}

// complete says whether the files hold every request they announce: each
// api-request line has its response, and no file ends inside a line.
func (t transcript) complete() bool {
	return t.unanswered == 0 && t.inFlight == 0
}

// fileCounts are the lines the reader passed over in a run's files.
type fileCounts struct {
	// skipped counts the lines whose shape the reader does not know.
	skipped int
	// untimed counts the responses that state no time.
	untimed int
	// inFlight counts the files that end inside a line.
	inFlight int
}

func (c *fileCounts) add(other fileCounts) {
	c.skipped += other.skipped
	c.untimed += other.untimed
	c.inFlight += other.inFlight
}

// warnings says what the reader passed over in files whose requests it used.
func (c fileCounts) warnings() []string {
	var warnings []string
	if c.skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d session file %s skipped: %s shape is not one this reader knows",
			c.skipped, plural(c.skipped, "line was", "lines were"), plural(c.skipped, "its", "their")))
	}
	if c.untimed > 0 {
		warnings = append(warnings, fmt.Sprintf("%d %s in the session files %s no time, so %s the time of the line before it",
			c.untimed, plural(c.untimed, "response", "responses"), plural(c.untimed, "states", "state"), plural(c.untimed, "it takes", "each takes")))
	}
	return warnings
}

// readSessionFiles walks the main transcript and every subagent's. Each
// content block of a response is its own line, so a requestId repeats; it
// counts once.
func (s *Session) readSessionFiles() (transcript, error) {
	if s.configDir == "" || !sessionIDPattern.MatchString(s.sessionID) {
		return transcript{}, nil
	}
	files, err := s.sessionFiles()
	if err != nil || len(files) == 0 {
		return transcript{}, err
	}
	builder := newRequestBuilder(s.unsplitWrite())
	run := transcript{path: files[0]}
	for i, file := range files {
		facts, err := builder.read(file)
		if err != nil {
			return transcript{}, err
		}
		if i == 0 {
			run.cwd = facts.cwd
		}
		run.add(facts.fileCounts)
	}
	run.requests = builder.requests(s.window())
	run.unanswered = builder.unanswered()
	return run, nil
}

func (s *Session) sessionFiles() ([]string, error) {
	main, err := filepath.Glob(filepath.Join(s.configDir, "projects", "*", s.sessionID+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("find session file: %w", err)
	}
	if len(main) == 0 {
		return nil, nil
	}
	directory := strings.TrimSuffix(main[0], ".jsonl")
	subagents, err := filepath.Glob(filepath.Join(directory, "subagents", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("find subagent files: %w", err)
	}
	return append(main[:1:1], subagents...), nil
}

type requestBuilder struct {
	// unsplit is the class that takes writes with no stated lifetime.
	unsplit   focus.TokenClass
	byID      map[string]*adapter.Request
	order     []string
	startedAt map[string]time.Time
	refs      map[string]string
}

func newRequestBuilder(unsplit focus.TokenClass) *requestBuilder {
	return &requestBuilder{
		unsplit:   unsplit,
		byID:      map[string]*adapter.Request{},
		startedAt: map[string]time.Time{},
		refs:      map[string]string{},
	}
}

// read adds one file's requests and returns what the file says about itself.
func (b *requestBuilder) read(path string) (fileFacts, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileFacts{sessions: map[string]bool{}}, fmt.Errorf("read session file: %w", err)
	}
	return b.readData(data)
}

// readData adds the requests in one file's text. Only api-request and
// assistant lines are read; a line of any other type is passed over whatever
// its shape, and a line with no type it can read is counted. Text after the
// last line break that does not parse is a line Claude Code was writing when
// the run stopped: the request in flight.
func (b *requestBuilder) readData(data []byte) (fileFacts, error) {
	facts := fileFacts{sessions: map[string]bool{}}
	lines := strings.Split(string(data), "\n")
	var previous time.Time
	for number, text := range lines {
		if strings.TrimSpace(text) == "" {
			continue
		}
		line, ok := parseSessionLine([]byte(text))
		switch {
		case !ok && number == len(lines)-1 && !json.Valid([]byte(text)):
			facts.inFlight++
			continue
		case !ok:
			facts.skipped++
			continue
		}
		if err := b.add(line, previous, &facts); err != nil {
			return facts, fmt.Errorf("session file line %d: %w", number+1, err)
		}
		if !line.at.IsZero() {
			previous = line.at
		}
		facts.observe(line)
	}
	return facts, nil
}

// add reads one line of a type the reader reads. A response that states no
// time takes the time of the line before it.
func (b *requestBuilder) add(line sessionLine, previous time.Time, facts *fileFacts) error {
	switch line.kind {
	case "api-request":
		if line.id == "" || line.at.IsZero() {
			facts.skipped++
			return nil
		}
		b.startedAt[line.id] = line.at
	case "assistant":
		id, err := responseID(line.requestID)
		if err != nil || id == "" {
			return err
		}
		if line.at.IsZero() {
			line.at = previous
			facts.untimed++
		}
		if err := b.addResponse(id, line); err != nil {
			return fmt.Errorf("request %s: %w", id, err)
		}
	}
	return nil
}

// responseID is the request an assistant line answers. Empty means the line
// answers none, as a message Claude Code writes itself does not.
func responseID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	id, ok := jsonString(raw)
	if !ok {
		return "", fmt.Errorf("requestId %s is not text", shown(raw))
	}
	return id, nil
}

// addResponse reads the usage of one response. A count it cannot read fails
// the line, and a count past the bound fails it with errPastBound.
func (b *requestBuilder) addResponse(id string, line sessionLine) error {
	u, model, err := readResponse(line.message)
	if err != nil {
		return err
	}
	context, err := contextInput(u.InputTokens, u.CacheRead, u.CacheCreated)
	if err != nil {
		return err
	}
	tokens, assumed, err := b.tokens(u)
	if err != nil {
		return err
	}
	request, seen := b.byID[id]
	if !seen {
		request = &adapter.Request{ID: id, Provider: provider, Grain: adapter.PerRequest}
		b.byID[id] = request
		b.order = append(b.order, id)
	}
	b.refs[id] = line.requestRef
	if request.Start.IsZero() {
		request.Start = line.at
	}
	request.Model = model
	request.End = line.at
	request.ServiceTier = serviceTier(u)
	request.Region = region(u.InferenceGeo)
	request.ContextInputTokens = context
	request.Tokens, request.AssumedWrite = tokens, assumed
	return nil
}

// cacheCreation is the written tokens' stated lifetimes.
type cacheCreation struct {
	FiveMinute int64
	OneHour    int64
}

// usage is one response's counts and the fields that select its price.
type usage struct {
	InputTokens    int64
	OutputTokens   int64
	CacheRead      int64
	CacheCreated   int64
	CacheCreation  *cacheCreation
	ThinkingTokens int64
	ServiceTier    string
	InferenceGeo   string
	Speed          string
}

// readResponse reads the model and the usage of an assistant line's message.
// Every count is read whole. A message that is not an object, or states no
// usage, holds no figure, and fails.
func readResponse(raw json.RawMessage) (usage, string, error) {
	var message struct {
		Model json.RawMessage `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &message) != nil {
		return usage{}, "", errors.New("the message is not an object")
	}
	model, err := optionalText("model", message.Model)
	if err != nil {
		return usage{}, "", err
	}
	if !isObject(message.Usage) {
		return usage{}, "", errors.New("the message states no usage object")
	}
	var fields struct {
		InputTokens   json.RawMessage `json:"input_tokens"`
		OutputTokens  json.RawMessage `json:"output_tokens"`
		CacheRead     json.RawMessage `json:"cache_read_input_tokens"`
		CacheCreated  json.RawMessage `json:"cache_creation_input_tokens"`
		CacheCreation json.RawMessage `json:"cache_creation"`
		Details       json.RawMessage `json:"output_tokens_details"`
		ServiceTier   json.RawMessage `json:"service_tier"`
		InferenceGeo  json.RawMessage `json:"inference_geo"`
		Speed         json.RawMessage `json:"speed"`
	}
	if err := json.Unmarshal(message.Usage, &fields); err != nil {
		return usage{}, "", fmt.Errorf("usage: %w", err)
	}
	var u usage
	counts := []struct {
		name  string
		raw   json.RawMessage
		value *int64
	}{
		{"input_tokens", fields.InputTokens, &u.InputTokens},
		{"output_tokens", fields.OutputTokens, &u.OutputTokens},
		{"cache_read_input_tokens", fields.CacheRead, &u.CacheRead},
		{"cache_creation_input_tokens", fields.CacheCreated, &u.CacheCreated},
	}
	if u.CacheCreation, err = readCacheCreation(fields.CacheCreation); err != nil {
		return usage{}, "", err
	}
	if u.ThinkingTokens, err = readThinking(fields.Details); err != nil {
		return usage{}, "", err
	}
	for _, count := range counts {
		if *count.value, err = wholeCount(count.raw); err != nil {
			return usage{}, "", fmt.Errorf("usage %s: %w", count.name, err)
		}
	}
	for _, text := range []struct {
		name  string
		raw   json.RawMessage
		value *string
	}{
		{"service_tier", fields.ServiceTier, &u.ServiceTier},
		{"inference_geo", fields.InferenceGeo, &u.InferenceGeo},
		{"speed", fields.Speed, &u.Speed},
	} {
		if *text.value, err = optionalText("usage "+text.name, text.raw); err != nil {
			return usage{}, "", err
		}
	}
	return u, model, nil
}

// readCacheCreation reads the written tokens' stated lifetimes. Absent or
// null states none.
func readCacheCreation(raw json.RawMessage) (*cacheCreation, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var fields struct {
		FiveMinute json.RawMessage `json:"ephemeral_5m_input_tokens"`
		OneHour    json.RawMessage `json:"ephemeral_1h_input_tokens"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &fields) != nil {
		return nil, fmt.Errorf("usage cache_creation %s is not an object", shown(raw))
	}
	var lifetimes cacheCreation
	var err error
	if lifetimes.FiveMinute, err = wholeCount(fields.FiveMinute); err != nil {
		return nil, fmt.Errorf("usage cache_creation ephemeral_5m_input_tokens: %w", err)
	}
	if lifetimes.OneHour, err = wholeCount(fields.OneHour); err != nil {
		return nil, fmt.Errorf("usage cache_creation ephemeral_1h_input_tokens: %w", err)
	}
	return &lifetimes, nil
}

// readThinking reads the thinking tokens the output states. Absent or null
// states none.
func readThinking(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var fields struct {
		Thinking json.RawMessage `json:"thinking_tokens"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &fields) != nil {
		return 0, fmt.Errorf("usage output_tokens_details %s is not an object", shown(raw))
	}
	thinking, err := wholeCount(fields.Thinking)
	if err != nil {
		return 0, fmt.Errorf("usage output_tokens_details thinking_tokens: %w", err)
	}
	return thinking, nil
}

// optionalText reads a text field. Absent and null are empty; any other value
// that is not text fails, because the field selects a price.
func optionalText(name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	value, ok := jsonString(raw)
	if !ok {
		return "", fmt.Errorf("%s %s is not text", name, shown(raw))
	}
	return value, nil
}

func (b *requestBuilder) tokens(u usage) (adapter.Tokens, focus.TokenClass, error) {
	tokens := adapter.Tokens{
		InputUncached:       u.InputTokens,
		CacheRead:           u.CacheRead,
		Output:              u.OutputTokens,
		ReasoningOutput:     u.ThinkingTokens,
		CachedInputReported: u.CacheRead,
	}
	var reported cacheCreation
	if u.CacheCreation != nil {
		reported = *u.CacheCreation
	}
	var assumed focus.TokenClass
	var err error
	tokens.CacheWrite5m, tokens.CacheWrite1h, assumed, err = splitWrites(u.CacheCreated, reported.FiveMinute, reported.OneHour, b.unsplit)
	return tokens, assumed, err
}

// unanswered counts the api-request lines whose id no response names as its
// requestRef.
func (b *requestBuilder) unanswered() int {
	answered := map[string]bool{}
	for _, ref := range b.refs {
		answered[ref] = true
	}
	count := 0
	for id := range b.startedAt {
		if !answered[id] {
			count++
		}
	}
	return count
}

// requests lists the requests in the order they started. A request no line
// timed takes the given window.
func (b *requestBuilder) requests(from, to time.Time) []adapter.Request {
	requests := make([]adapter.Request, 0, len(b.order))
	for _, id := range b.order {
		request := *b.byID[id]
		if start, ok := b.startedAt[b.refs[id]]; ok {
			request.Start = start
		}
		if request.Start.IsZero() {
			request.Start = from
		}
		if request.End.IsZero() {
			request.End = to
		}
		requests = append(requests, request)
	}
	slices.SortStableFunc(requests, func(a, b adapter.Request) int {
		return a.Start.Compare(b.Start)
	})
	return requests
}

func serviceTier(u usage) string {
	if u.Speed == "fast" {
		return "fast"
	}
	if u.ServiceTier == "" {
		return catalog.TierStandard
	}
	return u.ServiceTier
}

func region(geo string) string {
	if geo == "" {
		return catalog.RegionGlobal
	}
	return geo
}

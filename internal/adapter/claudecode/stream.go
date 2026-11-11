package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
)

// limitSubtypes are the result subtypes of a run that stopped at a limit its
// command set. Such a run did not fail, so it carries no error class.
var limitSubtypes = []string{"error_max_budget_usd", "error_max_turns", "error_max_structured_output_retries"}

// Feed reads one stdout line. A line that is not JSON is not the agent's
// stream and carries no usage. The line's type is read first, then the fields
// that type carries. A line whose shape this reader does not know is skipped
// and counted, unless it is a result: the result holds the run's figures, so
// a result that cannot be read stops the run's reading at Finish.
func (s *Session) Feed(line []byte, at time.Time) error {
	if s.first.IsZero() {
		s.first = at
	}
	s.last = at
	if !json.Valid(line) {
		return fmt.Errorf("claude-code: stream line is not JSON")
	}
	var envelope struct {
		Type    json.RawMessage `json:"type"`
		Subtype json.RawMessage `json:"subtype"`
	}
	kind, ok := "", false
	if json.Unmarshal(line, &envelope) == nil {
		kind, ok = jsonString(envelope.Type)
	}
	if !ok {
		s.skipped++
		return nil
	}
	subtype, _ := jsonString(envelope.Subtype)
	switch kind {
	case "system":
		if subtype == "init" {
			s.readInit(line)
		}
	case "assistant":
		s.readAssistant(line)
	case "result":
		s.readResult(line, subtype)
	case rateLimitEvent:
		s.sawRateLimit = true
	}
	return nil
}

func (s *Session) readInit(line []byte) {
	var init struct {
		SessionID    string `json:"session_id"`
		APIKeySource string `json:"apiKeySource"`
		Version      string `json:"claude_code_version"`
		CWD          string `json:"cwd"`
	}
	if json.Unmarshal(line, &init) != nil {
		s.skipped++
		return
	}
	s.sessionID, s.version, s.apiKeySource = init.SessionID, init.Version, init.APIKeySource
	if s.cwd == "" {
		s.cwd = init.CWD
	}
}

func (s *Session) readAssistant(line []byte) {
	var assistant struct {
		// Message is an object on an assistant line and can be a plain string
		// on other lines, so it is decoded only where a model is read from it.
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &assistant) != nil {
		s.skipped++
		return
	}
	var message struct {
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	// A message that is not an object names no model; the line still counts.
	if json.Unmarshal(assistant.Message, &message) == nil && message.Model != "" {
		if s.answered == nil {
			s.answered, s.routes = map[string]bool{}, map[string]*route{}
		}
		s.answered[message.Model] = true
		if s.routes[message.Model] == nil {
			s.routes[message.Model] = &route{tiers: map[string]bool{}, regions: map[string]bool{}}
		}
		s.routes[message.Model].observe(message.Usage)
	}
	if len(assistant.Error) > 0 && string(assistant.Error) != "null" {
		class, _ := jsonString(assistant.Error)
		s.stated = adapter.ParseAgentError(class)
	}
}

// readResult keeps the run's figures. A run can hold several results.
// modelUsage is cumulative, so the last one wins and adding them would double
// count; a later result that reads replaces an earlier one that did not.
func (s *Session) readResult(line []byte, subtype string) {
	s.sawResult = true
	var result struct {
		SessionID  json.RawMessage `json:"session_id"`
		IsError    json.RawMessage `json:"is_error"`
		Usage      json.RawMessage `json:"usage"`
		ModelUsage json.RawMessage `json:"modelUsage"`
	}
	if err := json.Unmarshal(line, &result); err != nil {
		s.unreadResult = err
		return
	}
	totals, err := readModelUsage(result.ModelUsage)
	if err != nil {
		s.unreadResult = err
		return
	}
	s.unreadResult, s.totals = nil, totals
	s.usageAlone = len(totals) == 0 && statesUsage(result.Usage)
	s.endedInError = string(result.IsError) == "true" && !slices.Contains(limitSubtypes, subtype)
	if id, ok := jsonString(result.SessionID); ok && id != "" {
		s.sessionID = id
	}
}

// modelUsageFields are the counts the result states per model, by name.
var modelUsageFields = []struct {
	name  string
	field func(*modelUsage) *int64
}{
	{"inputTokens", func(m *modelUsage) *int64 { return &m.InputTokens }},
	{"outputTokens", func(m *modelUsage) *int64 { return &m.OutputTokens }},
	{"cacheReadInputTokens", func(m *modelUsage) *int64 { return &m.CacheRead }},
	{"cacheCreationInputTokens", func(m *modelUsage) *int64 { return &m.CacheCreated }},
	{"cacheCreationEphemeral5mInputTokens", func(m *modelUsage) *int64 { return &m.FiveMinute }},
	{"cacheCreationEphemeral1hInputTokens", func(m *modelUsage) *int64 { return &m.OneHour }},
	{"thinkingTokens", func(m *modelUsage) *int64 { return &m.Thinking }},
}

// readModelUsage reads the result's per-model totals. Absent or null is no
// models. Every count is read whole; a field the reader does not price is
// left unread, whatever its type. costUSD is read exactly when it is a
// non-negative JSON number, and otherwise states nothing: it only says which
// context tier priced the tokens.
func readModelUsage(raw json.RawMessage) (map[string]modelUsage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var models map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, errors.New("modelUsage is not an object of models")
	}
	totals := make(map[string]modelUsage, len(models))
	for model, fields := range models {
		var totalsOfModel modelUsage
		for _, field := range modelUsageFields {
			value, err := wholeCount(fields[field.name])
			if err != nil {
				return nil, fmt.Errorf("modelUsage %s %s: %w", model, field.name, err)
			}
			*field.field(&totalsOfModel) = value
		}
		context, err := contextInput(totalsOfModel.InputTokens, totalsOfModel.CacheRead, totalsOfModel.CacheCreated)
		if err != nil {
			return nil, fmt.Errorf("modelUsage %s: %w", model, err)
		}
		totalsOfModel.context = context
		totalsOfModel.stated = statedUSD(fields["costUSD"])
		totals[model] = totalsOfModel
	}
	return totals, nil
}

// statesUsage says whether a result's usage object holds any tokens. A usage
// that cannot be read counts as stating some, so the run is never read as
// one that used nothing.
func statesUsage(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return true
	}
	for _, name := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		value, err := wholeCount(fields[name])
		if err != nil || value != 0 {
			return true
		}
	}
	return false
}

// route is what one model's assistant messages on the stream stated about
// where they ran: the service tier, fast mode read as the fast tier, and the
// inference region.
type route struct {
	tiers, regions map[string]bool
}

// observe reads one message's usage. The stream's counts are placeholders,
// so only the routing fields are read, and a field that is not text states
// nothing.
func (r *route) observe(raw json.RawMessage) {
	var fields struct {
		ServiceTier  json.RawMessage `json:"service_tier"`
		InferenceGeo json.RawMessage `json:"inference_geo"`
		Speed        json.RawMessage `json:"speed"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &fields) != nil {
		return
	}
	tier, tierStated := jsonString(fields.ServiceTier)
	speed, speedStated := jsonString(fields.Speed)
	if tierStated || speedStated {
		r.tiers[serviceTier(usage{ServiceTier: tier, Speed: speed})] = true
	}
	if geo, ok := jsonString(fields.InferenceGeo); ok {
		r.regions[region(geo)] = true
	}
}

// settle is the tier and region one model's whole-run rows take, with a note
// when the stream does not state them alone. Fast mode or a residency stated
// on any message prices the whole run: exact when every message stated it,
// an upper bound when some did not. Neither ever reads under the run.
func (r *route) settle() (tier, where, note string) {
	if r == nil {
		r = &route{}
	}
	tier, tierNote, tierStated := r.tier()
	where, regionNote, regionStated := r.region()
	switch {
	case !tierStated && !regionStated:
		return tier, where, "no stream message states this model's service tier or region, so the standard tier and global price it"
	case !tierStated:
		tierNote = "no stream message states this model's service tier, so the standard tier prices it"
	case !regionStated:
		regionNote = "no stream message states this model's region, so global prices it"
	}
	return tier, where, joinNotes(tierNote, regionNote)
}

func (r *route) tier() (tier, note string, stated bool) {
	tiers := slices.Sorted(maps.Keys(r.tiers))
	switch {
	case len(tiers) == 0:
		return catalog.TierStandard, "", false
	case r.tiers[catalog.TierFast] && len(tiers) > 1:
		return catalog.TierFast, "fast mode shows on some of this model's stream messages, so the fast tier prices all of them, an upper bound", true
	case len(tiers) == 1:
		return tiers[0], "", true
	}
	return catalog.TierStandard, fmt.Sprintf("the stream states the %s tiers for this model, and one total cannot split them, so the standard tier prices it", strings.Join(tiers, " and ")), true
}

func (r *route) region() (where, note string, stated bool) {
	if len(r.regions) == 0 {
		return catalog.RegionGlobal, "", false
	}
	var resident []string
	for _, where := range slices.Sorted(maps.Keys(r.regions)) {
		if where != catalog.RegionGlobal {
			resident = append(resident, where)
		}
	}
	switch {
	case len(resident) == 0:
		return catalog.RegionGlobal, "", true
	case len(resident) == 1 && r.regions[catalog.RegionGlobal]:
		return resident[0], fmt.Sprintf("data residency in %s shows on some of this model's stream messages, so it prices all of them, an upper bound", resident[0]), true
	case len(resident) == 1:
		return resident[0], "", true
	}
	return catalog.RegionGlobal, fmt.Sprintf("the stream states the regions %s for this model, and one total cannot split them, so global prices it", strings.Join(resident, " and ")), true
}

// joinNotes joins the notes that say something, as a row's description does.
func joinNotes(notes ...string) string {
	return strings.Join(slices.DeleteFunc(notes, func(note string) bool { return note == "" }), "; ")
}

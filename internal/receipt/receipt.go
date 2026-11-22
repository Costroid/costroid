// Package receipt holds the token counts of a developer's own agent sessions
// on one branch, as costroid receipt prints them for the developer to post on
// the pull request, and as a report reads, refuses and prices them.
//
// A receipt states counts and never an amount. Every string in it is a closed
// word, a day or a pattern, and no field holds text a person or a model wrote.
package receipt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// Marker opens a receipt's comment. Costroid's own comment opens with
	// another marker, and neither is a prefix of the other.
	Marker = "<!-- costroid:receipt -->"
	// Schema is the version of the receipt contract this release writes and
	// reads.
	Schema = "costroid.receipt/0.1"
	// ClaudeCode is the one agent whose session files this version of the
	// contract reads.
	ClaudeCode = "claude-code"
	// MaxSessions is the most entries a receipt holds.
	MaxSessions = 150
	// MaxBody is the most bytes a receipt's comment holds: a comment on
	// GitHub holds 65,536 characters.
	MaxBody = 65536

	fenceOpen  = "```json\n"
	fenceClose = "\n```"
	dayLayout  = time.DateOnly
	maxBranch  = 255
	// maxRequests is the largest integer a JSON reader keeps exactly.
	maxRequests = 1<<53 - 1
)

var (
	branchPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*(/[A-Za-z0-9_+-][A-Za-z0-9._+-]*)*$`)
	branchForbidden = regexp.MustCompile(`(\.\.|\.$|\.lock(/|$))`)
	versionPattern  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	sessionPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
	modelPattern    = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,128}$`)
	tierPattern     = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	regionPattern   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	dayPattern      = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$`)
	countPattern    = regexp.MustCompile(`^(0|[1-9][0-9]{0,14})$`)
	integerPattern  = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})$`)
	// plainKey is a key an error may repeat.
	plainKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,64}$`)
)

// Receipt is one receipt, as the contract lays it out.
type Receipt struct {
	Schema  string `json:"schema"`
	Branch  string `json:"branch"`
	Written string `json:"written"`
	// Since is the UTC day of the oldest entry of the branch's reflog on the
	// machine, nil when the reflog holds none.
	Since           *string   `json:"since"`
	Agents          []string  `json:"agents"`
	Sessions        []Session `json:"sessions"`
	SessionsOmitted int       `json:"sessions_omitted"`
}

// Session is one entry: the requests of one session on one model, service
// tier and region, summed.
type Session struct {
	Agent        string  `json:"agent"`
	AgentVersion string  `json:"agent_version"`
	SessionID    string  `json:"session_id"`
	Model        string  `json:"model"`
	ServiceTier  *string `json:"service_tier"`
	Region       *string `json:"region"`
	FirstDay     string  `json:"first_day"`
	LastDay      string  `json:"last_day"`
	Requests     int64   `json:"requests"`
	// LargestInput is the largest whole input of any one request the entry
	// sums. It selects a context tier.
	LargestInput string `json:"largest_input"`
	Tokens       Tokens `json:"tokens"`
	// AssumedTTL names the cache lifetimes the reader's rule assigned because
	// the session file did not state them. Empty when it stated each.
	AssumedTTL []string `json:"assumed_ttl,omitempty"`
}

// Tokens are a Claude Code entry's counts by billed class, each a whole
// number as a decimal string.
type Tokens struct {
	InputUncached string `json:"input_uncached"`
	CacheRead     string `json:"cache_read"`
	CacheWrite5m  string `json:"cache_write_5m"`
	CacheWrite1h  string `json:"cache_write_1h"`
	Output        string `json:"output"`
}

// key is what no two entries share.
type key struct {
	session, model, tier, region string
}

func (s Session) key() key {
	return key{s.SessionID, s.Model, orEmpty(s.ServiceTier), orEmpty(s.Region)}
}

func orEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// Body is the comment that carries the receipt: the marker, the object in a
// fenced block, and one line feed. A receipt that breaks the contract has
// none.
func (r Receipt) Body() (string, error) {
	if err := r.Check(); err != nil {
		return "", err
	}
	object, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("receipt: encode: %w", err)
	}
	return Marker + "\n" + fenceOpen + string(object) + fenceClose + "\n", nil
}

// Parse reads a comment's body as a receipt. A browser can store a comment
// with CRLF line endings, and a copy from a terminal can gain or lose a final
// line feed, so each CRLF reads as LF and blank lines after the closing fence
// are ignored, and nothing else is normalised. The body is then the marker
// line, the fenced object and at most one line feed, with no carriage return
// left. The object holds every key the contract requires and no other, each
// key once. Any fault refuses the whole receipt, and none of its sessions is
// read.
func Parse(body string) (Receipt, error) {
	if len(body) > MaxBody {
		return Receipt{}, fmt.Errorf("receipt: the comment is past %d bytes", MaxBody)
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if strings.Contains(body, "\r") {
		return Receipt{}, errors.New("receipt: the comment holds a carriage return that no line feed follows")
	}
	object, opened := strings.CutPrefix(body, Marker+"\n"+fenceOpen)
	object = strings.TrimRight(object, "\n")
	object, closed := strings.CutSuffix(object, fenceClose)
	if !opened || !closed {
		return Receipt{}, errors.New("receipt: the comment is not the marker line and one fenced json block")
	}
	if err := oneObject([]byte(object)); err != nil {
		return Receipt{}, fmt.Errorf("receipt: %w", err)
	}
	receipt, err := decode([]byte(object))
	if err != nil {
		return Receipt{}, fmt.Errorf("receipt: %w", err)
	}
	if err := receipt.Check(); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// oneObject holds the text to one JSON object with nothing after it, and no
// object in it to a key it names twice.
func oneObject(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if first != json.Delim('{') {
		return errors.New("the block does not hold one object")
	}
	if err := walkObject(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("the block holds more than one object")
	}
	return nil
}

// walkObject reads an object's members after its opening brace.
func walkObject(decoder *json.Decoder) error {
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, _ := token.(string)
		if seen[name] {
			return fmt.Errorf("the key %q is stated twice", shown(name))
		}
		seen[name] = true
		if err := walkValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func walkValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		return walkObject(decoder)
	case json.Delim('['):
		for decoder.More() {
			if err := walkValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	}
	return nil
}

// fields reads an object's members and holds them to the keys it must and
// may hold.
func fields(raw json.RawMessage, required, optional []string) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &members) != nil {
		return nil, errors.New("is not an object")
	}
	for name := range members {
		if !slices.Contains(required, name) && !slices.Contains(optional, name) {
			return nil, fmt.Errorf("holds the key %q, which a receipt does not define", shown(name))
		}
	}
	for _, name := range required {
		if _, found := members[name]; !found {
			return nil, fmt.Errorf("lacks the key %q", name)
		}
	}
	return members, nil
}

// shown is a key as an error may name it: a key is the comment author's own
// text, so one outside a plain word is not repeated.
func shown(name string) string {
	if !plainKey.MatchString(name) {
		return "?"
	}
	return name
}

func decode(object []byte) (Receipt, error) {
	top, err := fields(object, []string{"schema", "branch", "written", "since", "agents", "sessions", "sessions_omitted"}, nil)
	if err != nil {
		return Receipt{}, fmt.Errorf("the receipt %w", err)
	}
	var receipt Receipt
	if receipt.Schema, err = text(top["schema"]); err != nil {
		return Receipt{}, fmt.Errorf("schema %w", err)
	}
	if receipt.Branch, err = text(top["branch"]); err != nil {
		return Receipt{}, fmt.Errorf("branch %w", err)
	}
	if receipt.Written, err = text(top["written"]); err != nil {
		return Receipt{}, fmt.Errorf("written %w", err)
	}
	if receipt.Since, err = nullable(top["since"]); err != nil {
		return Receipt{}, fmt.Errorf("since %w", err)
	}
	if err := json.Unmarshal(top["agents"], &receipt.Agents); err != nil || receipt.Agents == nil {
		return Receipt{}, errors.New("agents is not a list of names")
	}
	omitted, err := integer(top["sessions_omitted"])
	if err != nil {
		return Receipt{}, fmt.Errorf("sessions_omitted %w", err)
	}
	receipt.SessionsOmitted = int(omitted)
	var entries []json.RawMessage
	if err := json.Unmarshal(top["sessions"], &entries); err != nil || entries == nil {
		return Receipt{}, errors.New("sessions is not a list")
	}
	for index, entry := range entries {
		session, err := decodeSession(entry)
		if err != nil {
			return Receipt{}, fmt.Errorf("session %d: %w", index+1, err)
		}
		receipt.Sessions = append(receipt.Sessions, session)
	}
	return receipt, nil
}

func decodeSession(raw json.RawMessage) (Session, error) {
	members, err := fields(raw, []string{
		"agent", "agent_version", "session_id", "model", "service_tier", "region",
		"first_day", "last_day", "requests", "largest_input", "tokens",
	}, []string{"assumed_ttl"})
	if err != nil {
		return Session{}, fmt.Errorf("the entry %w", err)
	}
	var session Session
	for name, into := range map[string]*string{
		"agent": &session.Agent, "agent_version": &session.AgentVersion, "session_id": &session.SessionID,
		"model": &session.Model, "first_day": &session.FirstDay, "last_day": &session.LastDay,
		"largest_input": &session.LargestInput,
	} {
		if *into, err = text(members[name]); err != nil {
			return Session{}, fmt.Errorf("%s %w", name, err)
		}
	}
	if session.ServiceTier, err = nullable(members["service_tier"]); err != nil {
		return Session{}, fmt.Errorf("service_tier %w", err)
	}
	if session.Region, err = nullable(members["region"]); err != nil {
		return Session{}, fmt.Errorf("region %w", err)
	}
	if session.Requests, err = integer(members["requests"]); err != nil {
		return Session{}, fmt.Errorf("requests %w", err)
	}
	if session.Tokens, err = decodeTokens(members["tokens"]); err != nil {
		return Session{}, err
	}
	if ttl, stated := members["assumed_ttl"]; stated {
		if err := json.Unmarshal(ttl, &session.AssumedTTL); err != nil || len(session.AssumedTTL) == 0 {
			return Session{}, errors.New("assumed_ttl is not a list of lifetimes")
		}
	}
	return session, nil
}

func decodeTokens(raw json.RawMessage) (Tokens, error) {
	members, err := fields(raw, []string{"input_uncached", "cache_read", "cache_write_5m", "cache_write_1h", "output"}, nil)
	if err != nil {
		return Tokens{}, fmt.Errorf("tokens %w", err)
	}
	var tokens Tokens
	for name, into := range map[string]*string{
		"input_uncached": &tokens.InputUncached, "cache_read": &tokens.CacheRead,
		"cache_write_5m": &tokens.CacheWrite5m, "cache_write_1h": &tokens.CacheWrite1h, "output": &tokens.Output,
	} {
		if *into, err = text(members[name]); err != nil {
			return Tokens{}, fmt.Errorf("tokens %s %w", name, err)
		}
	}
	return tokens, nil
}

// text reads a JSON string.
func text(raw json.RawMessage) (string, error) {
	var value string
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) || json.Unmarshal(raw, &value) != nil {
		return "", errors.New("is not a string")
	}
	return value, nil
}

// nullable reads a JSON string or null.
func nullable(raw json.RawMessage) (*string, error) {
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	value, err := text(raw)
	if err != nil {
		return nil, errors.New("is neither a string nor null")
	}
	return &value, nil
}

// integer reads a whole JSON number written in digits alone.
func integer(raw json.RawMessage) (int64, error) {
	digits := string(bytes.TrimSpace(raw))
	if !integerPattern.MatchString(digits) {
		return 0, errors.New("is not a whole number")
	}
	var value int64
	if _, err := fmt.Sscan(digits, &value); err != nil || value > maxRequests {
		return 0, errors.New("is past the largest whole number a reader keeps exactly")
	}
	return value, nil
}

// ValidBranch reports a branch name a receipt can be for. A detached head has
// no branch, and HEAD is never one.
func ValidBranch(name string) bool {
	return len(name) <= maxBranch && name != "HEAD" && branchPattern.MatchString(name) && !branchForbidden.MatchString(name)
}

// day reads a UTC day that is a date of the calendar.
func day(value string) (time.Time, error) {
	if !dayPattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("%q is not a day written YYYY-MM-DD", value)
	}
	parsed, err := time.Parse(dayLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is no day of the calendar", value)
	}
	return parsed, nil
}

// Check holds a receipt to the contract: every string to its pattern, every
// count to its form, the days in order, and no entry twice.
func (r Receipt) Check() error {
	if err := r.checkTop(); err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	written, _ := day(r.Written)
	var since time.Time
	if r.Since != nil {
		since, _ = day(*r.Since)
	}
	seen := map[key]bool{}
	for index, session := range r.Sessions {
		if err := session.check(written, since); err != nil {
			return fmt.Errorf("receipt: session %d: %w", index+1, err)
		}
		if seen[session.key()] {
			return fmt.Errorf("receipt: session %d states a session, model, service tier and region an earlier entry states", index+1)
		}
		seen[session.key()] = true
	}
	return nil
}

func (r Receipt) checkTop() error {
	switch {
	case r.Schema != Schema:
		return fmt.Errorf("the schema is not %s", Schema)
	case !ValidBranch(r.Branch):
		return errors.New("the branch is not a branch name a receipt can be for")
	case len(r.Sessions) == 0:
		return errors.New("it holds no session")
	case len(r.Sessions) > MaxSessions:
		return fmt.Errorf("it holds more than %d sessions", MaxSessions)
	case r.SessionsOmitted < 0:
		return errors.New("sessions_omitted is below zero")
	case !slices.Equal(r.Agents, []string{ClaudeCode}):
		return fmt.Errorf("the agents read are not %s alone, the one agent this release reads", ClaudeCode)
	}
	if _, err := day(r.Written); err != nil {
		return fmt.Errorf("written: %w", err)
	}
	if r.Since != nil {
		if _, err := day(*r.Since); err != nil {
			return fmt.Errorf("since: %w", err)
		}
	}
	return nil
}

// check holds one entry to the contract, its days to the receipt's.
func (s Session) check(written, since time.Time) error {
	switch {
	case s.Agent != ClaudeCode:
		return fmt.Errorf("the agent is not %s", ClaudeCode)
	case !versionPattern.MatchString(s.AgentVersion):
		return errors.New("agent_version is outside its pattern")
	case !sessionPattern.MatchString(s.SessionID):
		return errors.New("session_id is outside its pattern")
	case !modelPattern.MatchString(s.Model):
		return errors.New("model is outside its pattern")
	case s.ServiceTier != nil && !tierPattern.MatchString(*s.ServiceTier):
		return errors.New("service_tier is outside its pattern")
	case s.Region != nil && (!regionPattern.MatchString(*s.Region) || *s.Region == "global"):
		return errors.New("region is outside its pattern, or names default routing")
	case s.Requests < 1 || s.Requests > maxRequests:
		return errors.New("requests is not a whole number from 1")
	}
	for name, count := range map[string]string{
		"largest_input": s.LargestInput, "input_uncached": s.Tokens.InputUncached, "cache_read": s.Tokens.CacheRead,
		"cache_write_5m": s.Tokens.CacheWrite5m, "cache_write_1h": s.Tokens.CacheWrite1h, "output": s.Tokens.Output,
	} {
		if !countPattern.MatchString(count) {
			return fmt.Errorf("%s is not a whole number below 10^15 written in digits", name)
		}
	}
	if err := checkAssumed(s.AssumedTTL); err != nil {
		return err
	}
	first, err := day(s.FirstDay)
	if err != nil {
		return fmt.Errorf("first_day: %w", err)
	}
	last, err := day(s.LastDay)
	if err != nil {
		return fmt.Errorf("last_day: %w", err)
	}
	switch {
	case first.After(last):
		return errors.New("first_day is after last_day")
	case last.After(written):
		return errors.New("last_day is after the day the receipt was written")
	case !since.IsZero() && last.Before(since):
		return errors.New("last_day is before the lower bound")
	}
	return nil
}

// checkAssumed holds a Claude Code entry's assumed lifetimes to the two it
// writes, each once.
func checkAssumed(lifetimes []string) error {
	if lifetimes == nil {
		return nil
	}
	if len(lifetimes) == 0 {
		return errors.New("assumed_ttl is empty")
	}
	seen := map[string]bool{}
	for _, lifetime := range lifetimes {
		if lifetime != "5m" && lifetime != "1h" || seen[lifetime] {
			return errors.New("assumed_ttl names a lifetime Claude Code does not write, or one twice")
		}
		seen[lifetime] = true
	}
	return nil
}

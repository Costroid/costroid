package receipt

import (
	"cmp"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/catalog"
)

// unknownModel stands in for a model name outside the receipt's pattern, as a
// run's rows name it.
const unknownModel = "unknown-model"

// maxCount is one past the largest count a receipt states.
const maxCount = 1_000_000_000_000_000

// ErrNoSessions says no request on the branch was found.
var ErrNoSessions = errors.New("receipt: no session on the branch was found")

// Scope is what a receipt counts: the requests made on Branch, in a working
// directory under one of Roots, from Since on. Now is when it is written.
type Scope struct {
	Branch string
	// Roots are the repository's worktrees, as absolute paths.
	Roots []string
	// Since is the oldest entry of the branch's reflog, zero when it holds
	// none.
	Since time.Time
	Now   time.Time
}

// Tally is what Collect counted and what it left out, in requests.
type Tally struct {
	Counted int
	// Elsewhere counts the requests made on another branch, and NoBranch
	// those whose line recorded none.
	Elsewhere, NoBranch int
	// Outside counts the requests made in a working directory outside the
	// repository.
	Outside int
	// Earlier counts the requests made before Since, and Untimed those whose
	// files state no time or a time after Now.
	Earlier, Untimed int
	// Unnamed counts the requests of sessions whose id or version a receipt
	// cannot state.
	Unnamed int
	// SessionsOmitted counts the oldest sessions left out so that the
	// receipt fits in one comment.
	SessionsOmitted int
}

// entry is one entry being summed, with the times it spans.
type entry struct {
	session     Session
	first, last time.Time
	largest     int64
	tokens      [5]int64
	assumed     map[string]bool
}

// Collect sums the requests that count into a receipt for scope.Branch. A
// request counts when its line recorded the branch and a working directory
// inside the repository, and it was made from scope.Since on. A session that
// moved to another branch counts up to the line where it moved.
func Collect(requests []claudecode.LocalRequest, scope Scope) (Receipt, Tally, error) {
	var tally Tally
	entries := map[key]*entry{}
	for _, request := range requests {
		if counted := scope.admit(request, &tally); !counted {
			continue
		}
		if err := add(entries, request); err != nil {
			return Receipt{}, tally, err
		}
		tally.Counted++
	}
	if len(entries) == 0 {
		return Receipt{}, tally, ErrNoSessions
	}
	receipt := Receipt{
		Schema:  Schema,
		Branch:  scope.Branch,
		Written: scope.Now.UTC().Format(dayLayout),
		Agents:  []string{ClaudeCode},
	}
	if !scope.Since.IsZero() {
		since := scope.Since.UTC().Format(dayLayout)
		receipt.Since = &since
	}
	fitted, omitted, err := fit(receipt, slices.Collect(maps.Values(entries)))
	tally.SessionsOmitted = omitted
	return fitted, tally, err
}

// admit applies the rules a request is counted by, and counts the one that
// leaves it out.
func (s Scope) admit(request claudecode.LocalRequest, tally *Tally) bool {
	switch {
	case request.Branch == "":
		tally.NoBranch++
	case request.Branch != s.Branch:
		tally.Elsewhere++
	case !s.inside(request.Dir):
		tally.Outside++
	case request.Start.IsZero() || request.Start.UTC().Format(dayLayout) > s.Now.UTC().Format(dayLayout):
		tally.Untimed++
	case !s.Since.IsZero() && request.Start.Before(s.Since):
		tally.Earlier++
	case !sessionPattern.MatchString(request.Session) || !versionPattern.MatchString(request.Version):
		tally.Unnamed++
	default:
		return true
	}
	return false
}

// inside reports a working directory that is a root or under one.
func (s Scope) inside(dir string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	dir = filepath.Clean(dir)
	return slices.ContainsFunc(s.Roots, func(root string) bool {
		root = filepath.Clean(root)
		return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
	})
}

func add(entries map[key]*entry, request claudecode.LocalRequest) error {
	model := request.Model
	if !modelPattern.MatchString(model) {
		model = unknownModel
	}
	tier := optional(request.ServiceTier, tierPattern)
	region := optional(request.Region, regionPattern)
	if region != nil && *region == catalog.RegionGlobal {
		region = nil
	}
	session := Session{
		Agent: ClaudeCode, AgentVersion: request.Version, SessionID: request.Session,
		Model: model, ServiceTier: tier, Region: region,
	}
	found, seen := entries[session.key()]
	if !seen {
		found = &entry{session: session, first: request.Start, last: request.Start, assumed: map[string]bool{}}
		entries[session.key()] = found
	}
	found.session.Requests++
	found.first = minTime(found.first, request.Start)
	found.last = maxTime(found.last, request.Start)
	found.largest = max(found.largest, request.ContextInputTokens)
	tokens := request.Tokens
	for index, count := range []int64{tokens.InputUncached, tokens.CacheRead, tokens.CacheWrite5m, tokens.CacheWrite1h, tokens.Output} {
		if count < 0 || found.tokens[index] > maxCount-1-count {
			return errors.New("receipt: a session's tokens of one class pass 10^15")
		}
		found.tokens[index] += count
	}
	if lifetime := request.AssumedWrite.Lifetime(); lifetime != "" {
		found.assumed[lifetime] = true
	}
	return nil
}

// optional is a value the receipt states when it passes its pattern, and
// null otherwise.
func optional(value string, pattern interface{ MatchString(string) bool }) *string {
	if !pattern.MatchString(value) {
		return nil
	}
	return &value
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// finished is the entry as the receipt states it.
func (e *entry) finished() Session {
	session := e.session
	session.FirstDay = e.first.UTC().Format(dayLayout)
	session.LastDay = e.last.UTC().Format(dayLayout)
	session.LargestInput = strconv.FormatInt(e.largest, 10)
	session.Tokens = Tokens{
		InputUncached: strconv.FormatInt(e.tokens[0], 10),
		CacheRead:     strconv.FormatInt(e.tokens[1], 10),
		CacheWrite5m:  strconv.FormatInt(e.tokens[2], 10),
		CacheWrite1h:  strconv.FormatInt(e.tokens[3], 10),
		Output:        strconv.FormatInt(e.tokens[4], 10),
	}
	for _, lifetime := range []string{"5m", "1h"} {
		if e.assumed[lifetime] {
			session.AssumedTTL = append(session.AssumedTTL, lifetime)
		}
	}
	return session
}

// fit keeps the newest sessions, whole, that fit in one comment: at most
// MaxSessions entries and MaxBody bytes. It answers how many sessions it left
// out.
func fit(receipt Receipt, entries []*entry) (Receipt, int, error) {
	latest := map[string]time.Time{}
	for _, found := range entries {
		latest[found.session.SessionID] = maxTime(latest[found.session.SessionID], found.last)
	}
	sessions := slices.Collect(maps.Keys(latest))
	slices.SortFunc(sessions, func(a, b string) int {
		return cmp.Or(latest[b].Compare(latest[a]), strings.Compare(a, b))
	})
	for kept := len(sessions); kept > 0; kept-- {
		fitted := receipt
		fitted.SessionsOmitted = len(sessions) - kept
		fitted.Sessions = stated(entries, sessions[:kept])
		if len(fitted.Sessions) > MaxSessions {
			continue
		}
		body, err := fitted.Body()
		if err != nil {
			return Receipt{}, 0, err
		}
		if len(body) <= MaxBody {
			return fitted, fitted.SessionsOmitted, nil
		}
	}
	return Receipt{}, 0, errors.New("receipt: no one session fits in a comment")
}

// stated are the entries of the kept sessions, in the order they began.
func stated(entries []*entry, kept []string) []Session {
	var chosen []*entry
	for _, found := range entries {
		if slices.Contains(kept, found.session.SessionID) {
			chosen = append(chosen, found)
		}
	}
	slices.SortFunc(chosen, func(a, b *entry) int {
		x, y := a.session.key(), b.session.key()
		return cmp.Or(a.first.Compare(b.first), strings.Compare(x.session, y.session), strings.Compare(x.model, y.model),
			strings.Compare(x.tier, y.tier), strings.Compare(x.region, y.region))
	})
	sessions := make([]Session, len(chosen))
	for index, found := range chosen {
		sessions[index] = found.finished()
	}
	return sessions
}

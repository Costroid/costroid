// Package accuracy holds the record of Costroid's own accuracy test that a
// release carries: the lines of the accuracy page, docs/accuracy.md, for the
// page's last Window days, as the page stood when the release was built. The
// comment quotes it. Nothing in it is read from a network or a setting.
//
// The record is written by `go run ./scripts/accuracy -write`, from the same
// lines it writes the page from, and a test refuses a record that differs
// from the page.
package accuracy

import (
	"math/big"
	"slices"
)

// Window is how many of the page's last days the record holds: the page's own
// standard of consecutive days.
const Window = 14

// Record is the page's lines for its last Window days, in the page's order.
type Record struct {
	Lines []Line
}

// Line is one line of the page's table of days: one reconciliation of one
// credential on one UTC day, or a day nothing reconciled.
type Line struct {
	// Day is the UTC day, as YYYY-MM-DD.
	Day string
	// Sources are the providers the line names, as the page writes them.
	Sources []string
	// Agents are the agents the line's rows name, each with its version.
	Agents []Agent
	// Verdict is the page's verdict: "pass", "pass after named causes", or
	// the failures, apart by a comma.
	Verdict string
	// TokenError is the largest token class's error, in percent, as an exact
	// fraction such as "1/1000". It reads "n/a" when a class has no count on
	// the provider's side, and is empty when the line compares no tokens.
	TokenError string
}

// Agent is an agent and its version, as a row's tags name them. Version is
// empty when the rows state none.
type Agent struct {
	Name, Version string
}

// The verdicts of a line that passed.
const (
	VerdictPass          = "pass"
	VerdictPassExplained = "pass after named causes"
)

// NoTokenError is a line's token error when a class has no count on the
// provider's side, so no percentage exists.
const NoTokenError = "n/a"

// Passed reports whether the page judged the line to pass.
func (l Line) Passed() bool {
	return l.Verdict == VerdictPass || l.Verdict == VerdictPassExplained
}

// names reports whether the line names the agent and the source.
func (l Line) names(agent, source string) bool {
	return slices.Contains(l.Sources, source) && slices.ContainsFunc(l.Agents, func(a Agent) bool { return a.Name == agent })
}

// providerNames is the closed list of sources the line can name, with the
// name it gives each.
var providerNames = map[string]string{
	"anthropic": "Anthropic",
	"openai":    "OpenAI",
}

// ProviderName is the name the line gives a source, false for a source
// outside the closed list.
func ProviderName(source string) (string, bool) {
	name, known := providerNames[source]
	return name, known
}

// Count is what the record says of one agent on one source.
type Count struct {
	Agent, Source string
	// Versions are the agent's versions the counted days name, in order.
	Versions []string
	// First and Last are the first and the last day of the record's window,
	// whatever days the agent ran.
	First, Last string
	// Ran counts the days in the window with a line that names the agent and
	// the source, failed days included. Passed counts those whose every such
	// line passed.
	Ran, Passed int
	// TokenError is the largest token error, in percent, on a line of a day
	// that passed. It is nil when no such line compared tokens.
	TokenError *big.Rat
}

// Window is the first and the last day of the record, empty for a record
// that holds no line.
func (r Record) Window() (first, last string) {
	for _, line := range r.Lines {
		if first == "" || line.Day < first {
			first = line.Day
		}
		if line.Day > last {
			last = line.Day
		}
	}
	return first, last
}

// Count counts the days of the window on which the agent ran on the source.
// Every day counts, the days that failed included.
func (r Record) Count(agent, source string) Count {
	count := Count{Agent: agent, Source: source}
	count.First, count.Last = r.Window()
	passed := map[string]bool{}
	var days []string
	for _, line := range r.Lines {
		if !line.names(agent, source) {
			continue
		}
		if _, seen := passed[line.Day]; !seen {
			days = append(days, line.Day)
			passed[line.Day] = true
		}
		passed[line.Day] = passed[line.Day] && line.Passed()
		for _, named := range line.Agents {
			if named.Name == agent && named.Version != "" && !slices.Contains(count.Versions, named.Version) {
				count.Versions = append(count.Versions, named.Version)
			}
		}
	}
	slices.Sort(count.Versions)
	count.Ran = len(days)
	for _, day := range days {
		if passed[day] {
			count.Passed++
		}
	}
	for _, line := range r.Lines {
		if !line.names(agent, source) || !passed[line.Day] {
			continue
		}
		size, ok := new(big.Rat).SetString(line.TokenError)
		if ok && (count.TokenError == nil || size.Cmp(count.TokenError) > 0) {
			count.TokenError = size
		}
	}
	return count
}

// Covers reports whether a day of the record speaks for a run of one agent on
// one source: the line names both, and the source is one the line can name.
func (r Record) Covers(agent, source string) bool {
	if _, known := ProviderName(source); !known || agent == "" {
		return false
	}
	return r.Count(agent, source).Ran > 0
}

// Tested are the agents and the sources the record's lines name, each once
// and in order, counting only lines whose sources include one the line can
// name. Both are empty for a record that holds no day of the test.
func (r Record) Tested() (agents, sources []string) {
	for _, line := range r.Lines {
		var named []string
		for _, source := range line.Sources {
			if _, known := ProviderName(source); known {
				named = append(named, source)
			}
		}
		if len(named) == 0 || len(line.Agents) == 0 {
			continue
		}
		for _, source := range named {
			if !slices.Contains(sources, source) {
				sources = append(sources, source)
			}
		}
		for _, agent := range line.Agents {
			if !slices.Contains(agents, agent.Name) {
				agents = append(agents, agent.Name)
			}
		}
	}
	slices.Sort(agents)
	slices.Sort(sources)
	return agents, sources
}

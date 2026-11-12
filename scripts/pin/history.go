package main

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// refusal is one sentence a pin would make false.
type refusal struct {
	path     string
	line     int
	reason   string
	sentence string
}

func (r refusal) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", r.path, r.line, r.reason, r.sentence)
}

// refusals are every sentence that stops one pin.
type refusals []refusal

func (r refusals) Error() string {
	return fmt.Sprintf("%d sentences state history", len(r))
}

// window is how many words on either side of a version are read for a
// phrase that dates it.
const window = 4

// A version right after one of these words, or right before one of the
// after phrases, names a release that stays the same when the pin moves.
var (
	beforePhrases   = [][]string{{"after"}, {"before"}, {"since"}, {"until"}, {"up", "to"}}
	afterPhrases    = [][]string{{"and", "earlier"}, {"or", "earlier"}, {"and", "later"}, {"or", "later"}}
	sentencePhrases = [][]string{{"in", "no", "release"}}
	recordedRun     = regexp.MustCompile(`(?i)\brecord(ed|ing|ings)\b|\bruns? [0-9]{6,}\b`)
	sentenceEnd     = regexp.MustCompile(`[.!?]["'*)\x60]*(\s|$)`)
	listItem        = regexp.MustCompile(`^([-*]|[0-9]+\.) `)
)

// statesHistory says why the reference at text[start:end] dates something,
// or "" when it is a pin that may move.
func statesHistory(name, text string, start, end int) string {
	from, prose := unitAround(name, text, start)
	head, tail := sentenceBounds(prose, start-from, end-from)
	before := words(strings.TrimSuffix(prose[head:start-from], "v"))
	after := words(prose[end-from : tail])
	whole := words(prose[head:tail])
	for _, phrase := range beforePhrases {
		if holds(lastWords(before), phrase) {
			return fmt.Sprintf("%q comes before the version", strings.Join(phrase, " "))
		}
	}
	for _, phrase := range afterPhrases {
		if holds(firstWords(after), phrase) {
			return fmt.Sprintf("%q comes after the version", strings.Join(phrase, " "))
		}
	}
	for _, phrase := range sentencePhrases {
		if holds(whole, phrase) {
			return fmt.Sprintf("the sentence says %q", strings.Join(phrase, " "))
		}
	}
	if recordedRun.MatchString(prose[head:tail]) {
		return "the sentence names a recorded run"
	}
	return ""
}

// sentenceAround is the sentence that holds text[start:end], on one line,
// for a refusal to quote.
func sentenceAround(name, text string, start, end int) string {
	from, prose := unitAround(name, text, start)
	head, tail := sentenceBounds(prose, start-from, end-from)
	return strings.Join(strings.Fields(prose[head:tail]), " ")
}

// unitAround returns where the run of lines that reads as one piece of prose
// around offset starts, and that prose with comment markers blanked. A line
// of code, a table row or a heading is a unit of its own.
func unitAround(name, text string, offset int) (int, string) {
	lines := strings.SplitAfter(text, "\n")
	kinds := lineKinds(name, lines)
	at, position := 0, 0
	for i, line := range lines {
		if offset < position+len(line) || i == len(lines)-1 {
			at = i
			break
		}
		position += len(line)
	}
	first, last := at, at
	if kinds[at] == prose {
		for first > 0 && kinds[first-1] == prose && !opensParagraph(name, lines[first]) {
			first--
		}
		for last+1 < len(lines) && kinds[last+1] == prose && !opensParagraph(name, lines[last+1]) {
			last++
		}
	}
	start := 0
	for _, line := range lines[:first] {
		start += len(line)
	}
	var unit strings.Builder
	for _, line := range lines[first : last+1] {
		unit.WriteString(blankComment(name, line))
	}
	return start, unit.String()
}

type lineKind int

const (
	blank lineKind = iota
	code
	prose
)

// lineKinds reads a Markdown file's fenced blocks, tables and headings, and a
// YAML file's comments. Every other line of any other file is code.
func lineKinds(name string, lines []string) []lineKind {
	kinds := make([]lineKind, len(lines))
	fenced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch extension := path.Ext(name); {
		case trimmed == "":
			kinds[i] = blank
		case extension == ".md" && strings.HasPrefix(trimmed, "```"):
			fenced = !fenced
			kinds[i] = code
		case extension == ".md" && (fenced || strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "#")):
			kinds[i] = code
		case extension == ".md":
			kinds[i] = prose
		case (extension == ".yml" || extension == ".yaml") && strings.HasPrefix(trimmed, "#") && strings.Trim(trimmed, "#") != "":
			kinds[i] = prose
		default:
			kinds[i] = code
		}
	}
	return kinds
}

// opensParagraph reports a Markdown list item, which starts prose of its own.
func opensParagraph(name, line string) bool {
	if path.Ext(name) != ".md" {
		return false
	}
	return listItem.MatchString(strings.TrimLeft(line, " "))
}

// blankComment turns a YAML comment's markers into spaces, keeping every
// offset.
func blankComment(name, line string) string {
	if extension := path.Ext(name); extension != ".yml" && extension != ".yaml" {
		return line
	}
	indent := len(line) - len(strings.TrimLeft(line, " "))
	markers := len(line[indent:]) - len(strings.TrimLeft(line[indent:], "#"))
	return line[:indent] + strings.Repeat(" ", markers) + line[indent+markers:]
}

// sentenceBounds returns the sentence of prose that holds [start, end).
func sentenceBounds(prose string, start, end int) (int, int) {
	head := 0
	for _, match := range sentenceEnd.FindAllStringIndex(prose[:start], -1) {
		head = match[1]
	}
	tail := len(prose)
	if match := sentenceEnd.FindStringIndex(prose[end:]); match != nil {
		tail = end + match[1]
	}
	return head, tail
}

// words lowercases text and splits it on everything but letters.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
}

func lastWords(all []string) []string {
	return all[max(0, len(all)-window):]
}

func firstWords(all []string) []string {
	return all[:min(len(all), window)]
}

// holds reports phrase as consecutive words of all.
func holds(all, phrase []string) bool {
	for i := 0; i+len(phrase) <= len(all); i++ {
		if strings.Join(all[i:i+len(phrase)], " ") == strings.Join(phrase, " ") {
			return true
		}
	}
	return false
}

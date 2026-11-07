// Package review renders the agent's own answer as a second pull request
// comment, apart from Costroid's report.
//
// The answer is the agent's text, and the agent read the pull request, so the
// comment treats it as data: it sits in a fenced code block no line of it can
// close, so no link, mention, image or HTML in it renders. The comment says
// the words are the agent's.
package review

import (
	"bytes"
	"regexp"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"
)

const (
	// Marker opens the answer's comment, which is how the next run finds it.
	// It differs from the report's marker, so neither run edits the other's.
	Marker = "<!-- costroid:review -->"
	// MaxAnswerBytes is the most of the answer the comment shows. A fence is
	// at most half the answer's length plus one, so the body stays under the
	// 65535 characters GitHub stores in a comment.
	MaxAnswerBytes = 30000
	// MaxFileBytes is the most of the answer the agent's job keeps and the
	// report's job reads.
	MaxFileBytes = 1 << 20
)

// head is the form of a commit the comment names.
var head = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Answer is what one run left for the comment.
type Answer struct {
	// Text is the agent's answer. Empty says the run kept none.
	Text string
	// Head is the commit the agent ran on. A value that is not a commit
	// leaves the commit out of the comment.
	Head string
}

var body = template.Must(template.New("review").Parse(`{{.Marker}}
### The agent's answer

{{if .Text -}}
The text below is the agent's own answer from its run{{.On}}. Costroid shows it as plain text and did not write or check it.

{{.Fence}}text
{{.Text}}
{{.Fence}}
{{- if .Cut}}

The answer runs past {{.Cap}} bytes, so this comment shows its first {{.Cap}} bytes.
{{- end}}
{{- else -}}
The agent's job kept no answer from its run{{.On}}. The job's log says why.
{{- end}}
`))

type view struct {
	Marker, On, Fence, Text string
	Cut                     bool
	Cap                     int
}

// Render returns the comment's body.
func Render(answer Answer) string {
	text, cut := Clean(answer.Text)
	data := view{Marker: Marker, Text: text, Cut: cut, Cap: MaxAnswerBytes}
	if head.MatchString(answer.Head) {
		data.On = " on `" + answer.Head[:7] + "`"
	}
	if text != "" {
		data.Fence = Fence(text)
	}
	var out bytes.Buffer
	if err := body.Execute(&out, data); err != nil {
		panic("review: the template does not fit its data: " + err.Error())
	}
	return out.String()
}

// Clean makes the answer safe to fence: valid UTF-8 with line feeds for line
// ends, with every control character but tab and line feed, and every
// direction override, shown as U+FFFD, and no blank lines around it. It cuts
// the result to MaxAnswerBytes at a character boundary and says whether it cut.
func Clean(text string) (string, bool) {
	text = strings.ToValidUTF8(text, string(utf8.RuneError))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), isDirectionControl(r):
			return utf8.RuneError
		}
		return r
	}, text)
	text = strings.TrimRight(strings.TrimLeft(text, "\n"), " \t\n")
	if len(text) <= MaxAnswerBytes {
		return text, false
	}
	end := MaxAnswerBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.TrimRight(text[:end], " \t\n"), true
}

// isDirectionControl reports the characters that reorder the text around
// them, so a line can read differently from what it holds.
func isDirectionControl(r rune) bool {
	return (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩')
}

// Fence returns the code fence for text: of backticks or of tildes, whichever
// text holds the shorter run of, and one longer than that run, at least three.
// A fence closes only on a line of at least as many of its own character, so
// no line of text can close it.
func Fence(text string) string {
	char, run := "`", longestRun(text, '`')
	if tildes := longestRun(text, '~'); tildes < run {
		char, run = "~", tildes
	}
	return strings.Repeat(char, max(3, run+1))
}

func longestRun(text string, char byte) int {
	longest, current := 0, 0
	for i := range len(text) {
		if text[i] != char {
			current = 0
			continue
		}
		current++
		longest = max(longest, current)
	}
	return longest
}

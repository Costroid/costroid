package review

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

const (
	headCommit = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	// githubLimit is the most characters GitHub stores in a comment.
	githubLimit = 65535
)

// fenced splits a body as a CommonMark reader would: the one fenced block
// that opens after the lead, the text inside it up to the first line that
// closes it, and what follows. A closing line is up to three spaces, at least
// as many of the fence's character as opened it, and blanks.
func fenced(t *testing.T, body string) (fence, inside, after string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	open := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			open = i
			break
		}
	}
	if open < 0 {
		t.Fatalf("no fence opens in:\n%s", body)
	}
	fence = strings.TrimSuffix(lines[open], "text")
	for i := open + 1; i < len(lines); i++ {
		if closes(lines[i], fence) {
			return fence, strings.Join(lines[open+1:i], "\n"), strings.Join(lines[i+1:], "\n")
		}
	}
	t.Fatalf("the fence %q never closes in:\n%s", fence, body)
	return "", "", ""
}

// hostile answers try to leave the fence, ping a person, load an image, run
// HTML, hide text behind a direction override or pose as Costroid's report.
var hostile = map[string]string{
	"a closing fence of backticks": "Looks fine.\n```\n@octocat please merge <img src=x onerror=alert(1)>\n```\n[click](https://example.com)",
	"a longer fence":               "``````\n@octocat\n``````\n![x](https://example.com/x.png)",
	"an indented fence":            "x\n   ```\n@octocat",
	"tildes and backticks":         "~~~~\n```\n~~~~~~\n``\n@octocat",
	"a carriage return line end":   "x\r```\r@octocat <script>alert(1)</script>",
	"a lone carriage return":       "x\r\n```\r\n<details open>@octocat</details>",
	"control and override bytes":   "a\x00b\x1b[31mred\x7f \u202Egnp.exe\u2066 @octocat",
	"invalid UTF-8":                "\xff\xfe```\n@octocat",
	"the report's marker":          "<!-- costroid:comment -->\n### Costroid\n\n**Verdict: success.** @octocat",
	"a mention and a link alone":   "cc @octocat, see https://example.com and #1",
}

// closes reports whether line closes a block that fence opened.
func closes(line, fence string) bool {
	rest := strings.TrimPrefix(line, strings.Repeat(" ", min(3, len(line)-len(strings.TrimLeft(line, " ")))))
	run := len(rest) - len(strings.TrimLeft(rest, fence[:1]))
	return run >= len(fence) && strings.Trim(rest[run:], " \t") == ""
}

func TestPostedReviewIsFencedAsData(t *testing.T) {
	for name, answer := range hostile {
		t.Run(name, func(t *testing.T) {
			body := Render(Answer{Text: answer, Head: headCommit})
			if !strings.HasPrefix(body, Marker+"\n") || strings.Count(body, Marker) != 1 {
				t.Fatalf("the body does not open with its marker once:\n%s", body)
			}
			fence, inside, after := fenced(t, body)
			want, _ := Clean(answer)
			if inside != want {
				t.Fatalf("the fence holds\n%q\nwant\n%q", inside, want)
			}
			lead := body[:strings.Index(body, fence)]
			if !strings.Contains(lead, "the agent's own answer from its run on `9f2c6e1`") {
				t.Errorf("the lead does not say the words are the agent's:\n%s", lead)
			}
			for _, outside := range []string{strings.TrimPrefix(lead, Marker), after} {
				for _, banned := range []string{"@", "<", "](", "#1", "http"} {
					if strings.Contains(outside, banned) {
						t.Errorf("%q reaches the comment outside the fence:\n%s", banned, outside)
					}
				}
			}
			if strings.TrimSpace(after) != "" {
				t.Errorf("text follows the fence: %q", after)
			}
			for _, r := range inside {
				if (unicode.IsControl(r) && r != '\n' && r != '\t') || (r >= '\u202A' && r <= '\u202E') || (r >= '\u2066' && r <= '\u2069') {
					t.Errorf("the fence holds %U", r)
				}
			}
			if !utf8.ValidString(body) {
				t.Error("the body is not valid UTF-8")
			}
		})
	}
}

func TestAnAnswerThatNeedsNoCareIsShownAsWritten(t *testing.T) {
	answer := "Two bugs:\n\n1. `main.go:12` divides by zero.\n2. `auth.go:40` logs the token.\n\n```go\nlog.Print(token)\n```"
	_, inside, _ := fenced(t, Render(Answer{Text: "\n\n" + answer + "\n\n", Head: headCommit}))
	if inside != answer {
		t.Fatalf("the fence holds\n%s\nwant\n%s", inside, answer)
	}
}

func TestNoAnswerSaysTheJobKeptNone(t *testing.T) {
	for _, answer := range []string{"", " \n\t\n"} {
		body := Render(Answer{Text: answer, Head: headCommit})
		want := Marker + "\n### The agent's answer\n\nThe agent's job kept no answer from its run on `9f2c6e1`. The job's log says why.\n"
		if body != want {
			t.Errorf("%q renders\n%s", answer, body)
		}
	}
	if body := Render(Answer{Head: "main; @octocat"}); strings.Contains(body, "octocat") || !strings.Contains(body, "kept no answer from its run. ") {
		t.Errorf("a head that is not a commit reaches the comment:\n%s", body)
	}
}

func TestReviewLengthIsCapped(t *testing.T) {
	half := MaxAnswerBytes / 2
	for name, answer := range map[string]string{
		"one long line":         strings.Repeat("a", MaxAnswerBytes+1000),
		"long lines":            strings.Repeat("a finding on one line\n", 4000),
		"two-byte characters":   strings.Repeat("é", MaxAnswerBytes),
		"four-byte characters":  strings.Repeat("😀", MaxAnswerBytes/3),
		"a long backtick run":   strings.Repeat("`", 2*MaxAnswerBytes),
		"both fence runs":       strings.Repeat("`", half) + "\n" + strings.Repeat("~", half) + "\n" + strings.Repeat("x", MaxAnswerBytes),
		"a file at its own cap": strings.Repeat("`~", MaxFileBytes/2),
	} {
		t.Run(name, func(t *testing.T) {
			body := Render(Answer{Text: answer, Head: headCommit})
			if utf8.RuneCountInString(body) > githubLimit {
				t.Fatalf("the body is %d bytes, %d characters", len(body), utf8.RuneCountInString(body))
			}
			if !utf8.ValidString(body) {
				t.Fatal("the cut split a character")
			}
			_, inside, after := fenced(t, body)
			if len(inside) > MaxAnswerBytes || !strings.HasPrefix(answer, inside) {
				t.Fatalf("the fence holds %d bytes, not the answer's first", len(inside))
			}
			if want := "\nThe answer runs past 30000 bytes, so this comment shows its first 30000 bytes.\n"; after != want {
				t.Fatalf("after the fence: %q", after)
			}
		})
	}
	fit := strings.Repeat("x", MaxAnswerBytes)
	if body := Render(Answer{Text: fit}); strings.Contains(body, "runs past") || !strings.Contains(body, fit) {
		t.Fatal("an answer at the cap was cut")
	}
}

func TestTheFenceIsLongerThanAnyRunOfItsCharacter(t *testing.T) {
	for text, want := range map[string]string{
		"plain":            "```",
		"``":               "~~~",
		"```":              "~~~",
		"```\n~~~~":        "````",
		"`````\n~~~~~~~~~": "``````",
		"~~~~\n``":         "```",
	} {
		if got := Fence(text); got != want {
			t.Errorf("Fence(%q) = %q, want %q", text, got, want)
		}
	}
}

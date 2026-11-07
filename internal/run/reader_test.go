package run

import (
	"strings"
	"testing"
)

// TestReaderLineKeepsTheFirstLineAllowlistedAndCut holds the line a result
// object carries to its first line, its allowlist and 160 bytes.
func TestReaderLineKeepsTheFirstLineAllowlistedAndCut(t *testing.T) {
	cases := map[string]string{
		"claude-code: read the execution file: invalid character '`' looking for beginning of value": "claude-code: read the execution file: invalid character '?' looking for beginning of value",
		"  first line  \nsecond line": "first line",
		"@team <img src=x> [a](b) é":  "?team ?img src=x? ?a?(b) ?",
		strings.Repeat("a", 200):      strings.Repeat("a", 160),
		strings.Repeat("é", 100):      strings.Repeat("?", 100),
		"":                            "",
	}
	for text, want := range cases {
		if got := ReaderLine(text); got != want {
			t.Errorf("ReaderLine(%q) = %q, want %q", text, got, want)
		}
		if got := ReaderLine(want); got != want {
			t.Errorf("ReaderLine is not stable on %q: %q", want, got)
		}
	}
}

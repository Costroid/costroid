package main

import (
	"regexp"
	"strings"
	"unicode"
)

// A licence is one licence file's text the generator knows, by the words it
// is made of, and the SPDX identifiers of the terms it states.
type licence struct {
	ids     []string
	pattern []string
	// apache marks the Apache License, Version 2.0 unchanged. NOTICE gives such
	// a file by its copyright lines and holds the licence's text once.
	apache bool
}

// holder stands in a pattern for the words that name a copyright holder.
const (
	holder      = "\x00holder"
	holderWords = 6
)

// licences holds the Apache License three ways: with its appendix, ending at
// END OF TERMS AND CONDITIONS, and ending at its last section without that
// line. Modules ship each.
var licences = func() []licence {
	apacheTerms, _, found := strings.Cut(apacheText, "END OF TERMS AND CONDITIONS")
	if !found {
		panic("apacheText lacks the end of its terms")
	}
	apacheEnded := apacheTerms + "END OF TERMS AND CONDITIONS"
	return []licence{
		{ids: []string{"Apache-2.0"}, pattern: patternWords(apacheText), apache: true},
		{ids: []string{"Apache-2.0"}, pattern: patternWords(apacheEnded), apache: true},
		{ids: []string{"Apache-2.0"}, pattern: patternWords(apacheTerms), apache: true},
		{ids: []string{"MIT"}, pattern: patternWords(mitText)},
		{ids: []string{"MIT-0"}, pattern: patternWords(mitNoAttributionText)},
		{ids: []string{"BSD-3-Clause"}, pattern: patternWords(bsd3Text)},
		{
			ids:     []string{"Apache-2.0", "MIT"},
			pattern: patternWords(yamlMITText + "\n" + mitText + "\n" + yamlApacheText + "\n" + apacheHeaderText),
		},
		{
			ids:     []string{"Apache-2.0", "MIT"},
			pattern: patternWords(mcpTransitionText + "\n" + apacheEnded + "\n" + mitText + "\n" + mcpDocumentationText),
		},
	}
}()

// identify returns the licence a file's text states, or false when the
// generator does not know it.
func identify(text string) (licence, bool) {
	subject := words(text)
	for _, l := range licences {
		if matches(l.pattern, subject) {
			return l, true
		}
	}
	return licence{}, false
}

// matches reports whether text is the pattern word for word, each holder
// standing for one to holderWords words.
func matches(pattern, text []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == holder {
			for n := 1; n <= holderWords && n <= len(text); n++ {
				if matches(pattern[1:], text[n:]) {
					return true
				}
			}
			return false
		}
		if len(text) == 0 || text[0] != pattern[0] {
			return false
		}
		pattern, text = pattern[1:], text[1:]
	}
	return len(text) == 0
}

var (
	listMarker  = regexp.MustCompile(`^\s*(\*|-|\d+\.|\([a-z]\))\s+`)
	placeholder = regexp.MustCompile(`<<\w+>>`)
	titles      = map[string]bool{"the mit license mit": true, "mit license": true, "mit no attribution": true}
)

// words lowers a licence file's text to the words that carry its terms. It
// sets aside copyright lines, title lines, list markers, case and
// punctuation, so two copies that differ only in those compare equal.
func words(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if isCopyright(line) {
			continue
		}
		line := strings.Join(lineWords(listMarker.ReplaceAllString(line, "")), " ")
		if titles[line] || line == "all rights reserved" {
			continue
		}
		out = append(out, strings.Fields(line)...)
	}
	return out
}

// patternWords is words for a known text, with each <<name>> as holder.
func patternWords(text string) []string {
	var out []string
	for i, part := range placeholder.Split(text, -1) {
		if i > 0 {
			out = append(out, holder)
		}
		out = append(out, words(part)...)
	}
	return out
}

func lineWords(line string) []string {
	return strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// isCopyright reports whether a line is a copyright notice: it starts with
// Copyright or (c) and holds a year, or the year's placeholder.
func isCopyright(line string) bool {
	line = strings.ToLower(strings.TrimSpace(line))
	if !strings.HasPrefix(line, "copyright") && !strings.HasPrefix(line, "(c)") && !strings.HasPrefix(line, "©") {
		return false
	}
	return strings.ContainsFunc(line, unicode.IsDigit) || strings.Contains(line, "yyyy")
}

// isPlaceholder reports whether a copyright line is the Apache appendix's
// template, which names nobody.
func isPlaceholder(line string) bool {
	return strings.Contains(strings.ToLower(line), "yyyy")
}

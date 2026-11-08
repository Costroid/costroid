package main

import (
	"fmt"
	"slices"
	"strings"
)

// rule opens each module's section of NOTICE, and the licence text at its end.
var rule = strings.Repeat("=", 80)

const preamble = `Costroid
Copyright 2026 The Costroid Authors

Licensed under the Apache License, Version 2.0. See LICENSE.

The costroid binary and container image include the Go standard library and
these third-party Go modules, each under its own licence:

`

const afterListing = `
Each module's licence and notice files follow as the module holds them. A
file that is the Apache License, Version 2.0 unchanged is given by its
copyright lines, and the licence's text closes this file.
`

// A section is one module's part of NOTICE: the module and its files.
type section struct {
	module module
	files  []licenceFile
}

// ids gives the SPDX identifiers of every licence a module's files state,
// joined as one expression.
func (s section) ids() string {
	var ids []string
	for _, f := range s.files {
		for _, id := range f.licence.ids {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return strings.Join(ids, " AND ")
}

func render(sections []section) string {
	var b strings.Builder
	b.WriteString(preamble)
	width := 0
	for _, s := range sections {
		width = max(width, len(s.module.String()))
	}
	for _, s := range sections {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, s.module, s.ids())
	}
	b.WriteString(afterListing)
	for _, s := range sections {
		fmt.Fprintf(&b, "\n%s\n%s\n", rule, s.module)
		for _, f := range s.files {
			b.WriteString("\n")
			renderFile(&b, f)
		}
	}
	fmt.Fprintf(&b, "\n%s\nApache License, Version 2.0\n\n%s\n", rule, strings.Trim(apacheText, "\n"))
	return b.String()
}

func renderFile(b *strings.Builder, f licenceFile) {
	for _, path := range f.paths {
		fmt.Fprintf(b, "File: %s\n", path)
	}
	switch {
	case f.notice:
		fmt.Fprintf(b, "\n%s\n", f.text)
	case f.licence.apache:
		fmt.Fprintf(b, "Licence: %s, whose text closes this file\n", strings.Join(f.licence.ids, " AND "))
		if lines := copyrightLines(f.text); len(lines) > 0 {
			fmt.Fprintf(b, "\n%s\n", strings.Join(lines, "\n"))
		}
	default:
		fmt.Fprintf(b, "Licence: %s\n\n%s\n", strings.Join(f.licence.ids, " AND "), f.text)
	}
}

// copyrightLines gives a file's copyright lines, without the Apache
// appendix's template line.
func copyrightLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if isCopyright(line) && !isPlaceholder(line) {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

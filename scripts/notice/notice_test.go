package main

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const noticeFile = "../../NOTICE"

func TestNoticeNamesEveryLinkedModule(t *testing.T) {
	ctx := context.Background()
	binary := filepath.Join(t.TempDir(), "costroid")
	if err := buildRelease(ctx, binary); err != nil {
		t.Fatal(err)
	}
	want, err := generate(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile(noticeFile)
	if err != nil {
		t.Fatal(err)
	}
	if d := difference(string(have), want); d != "" {
		t.Fatalf("%s; run make notice", d)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	listed := listedModules(string(have))
	for _, dep := range info.Deps {
		if name := dep.Path + " " + dep.Version; !slices.Contains(listed, name) {
			t.Errorf("NOTICE does not list %s, which the binary links", name)
		}
	}
	if !slices.Contains(listed, standardLibrary) {
		t.Errorf("NOTICE does not list the %s", standardLibrary)
	}
}

func TestNoticeStopsOnAnUnknownLicence(t *testing.T) {
	known := mitText
	unknown := mitText + "\n\nThe Software shall not be used on a Tuesday."
	for _, test := range []struct {
		name, licence string
		stops         bool
	}{
		{"MIT as published", known, false},
		{"MIT with a clause added", unknown, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			text := "Copyright (c) 2026 Example Authors\n\n" + test.licence + "\n"
			if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			m := module{path: "example.com/tuesday", version: "v1.0.0", dir: dir, packageDirs: []string{dir}}
			_, err := licenceFiles(m)
			if !test.stops {
				if err != nil {
					t.Fatalf("the generator stopped on a licence it knows: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("the generator identified a licence with a clause it has never read")
			}
			if !strings.Contains(err.Error(), "example.com/tuesday v1.0.0") || !strings.Contains(err.Error(), "LICENSE") {
				t.Fatalf("the error does not name the module and its file: %v", err)
			}
		})
	}
}

func TestNoticeStopsOnAModuleWithoutALicence(t *testing.T) {
	dir := t.TempDir()
	notice := "Example\nCopyright 2026 Example Authors\n"
	if err := os.WriteFile(filepath.Join(dir, "NOTICE"), []byte(notice), 0o644); err != nil {
		t.Fatal(err)
	}
	m := module{path: "example.com/unlicensed", version: "v0.1.0", dir: dir, packageDirs: []string{dir}}
	if _, err := licenceFiles(m); err == nil || !strings.Contains(err.Error(), "example.com/unlicensed v0.1.0 holds no licence file") {
		t.Fatalf("err = %v, want the module named as holding no licence file", err)
	}
}

func TestNoticeReadsLicencesOnlyAboveALinkedPackage(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("LICENSE", "Copyright 2026 Example Authors\n\n"+mitText)
	write("internal/linked/LICENSE", "Copyright 2026 Example Authors\n\n"+strings.ReplaceAll(bsd3Text, "<<holder>>", "the copyright holder"))
	write("internal/linked/inner/code.go", "package inner\n")
	write("internal/unlinked/LICENSE", "Read nowhere.\n")
	m := module{path: "example.com/nested", version: "v1.0.0", dir: dir, packageDirs: []string{filepath.Join(dir, "internal", "linked", "inner")}}
	files, err := licenceFiles(m)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, fmt.Sprintf("%s %v", f.paths, f.licence.ids))
	}
	if want := []string{"[LICENSE] [MIT]", "[internal/linked/LICENSE] [BSD-3-Clause]"}; !slices.Equal(got, want) {
		t.Fatalf("files = %q, want %q", got, want)
	}
}

func TestNoticeDifferenceNamesTheModule(t *testing.T) {
	m := func(path, id string) section {
		return section{
			module: module{path: path, version: "v1.0.0"},
			files:  []licenceFile{{paths: []string{"LICENSE"}, text: "Copyright 2026 " + path, licence: licence{ids: []string{id}}}},
		}
	}
	want := render([]section{m("example.com/a", "MIT"), m("example.com/b", "MIT")})
	for _, test := range []struct {
		name, have, names string
	}{
		{"a module left out", render([]section{m("example.com/a", "MIT")}), "example.com/b v1.0.0"},
		{"a licence changed", render([]section{m("example.com/a", "MIT"), m("example.com/b", "BSD-3-Clause")}), "example.com/b v1.0.0"},
		{"a module added", render([]section{m("example.com/a", "MIT"), m("example.com/b", "MIT"), m("example.com/c", "MIT")}), "example.com/c v1.0.0"},
		{"a line of text changed", strings.Replace(want, "Copyright 2026 example.com/a", "Copyright 2025 example.com/a", 1), "example.com/a v1.0.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := difference(test.have, want)
			if !strings.Contains(d, test.names) {
				t.Fatalf("difference = %q, want it to name %s", d, test.names)
			}
		})
	}
	if d := difference(want, want); d != "" {
		t.Fatalf("difference of a text with itself = %q", d)
	}
}

// difference names the first module whose part of NOTICE differs from the
// generator's, or gives "" when the two are the same.
func difference(have, want string) string {
	if have == want {
		return ""
	}
	haveListing, wantListing := listing(have), listing(want)
	for i := range max(len(haveListing), len(wantListing)) {
		switch {
		case i >= len(haveListing):
			return fmt.Sprintf("NOTICE does not list %s", moduleOf(wantListing[i]))
		case i >= len(wantListing):
			return fmt.Sprintf("NOTICE lists %s, which the binary does not link", moduleOf(haveListing[i]))
		case haveListing[i] != wantListing[i]:
			return fmt.Sprintf("NOTICE lists %q where the generator gives %q, for %s", haveListing[i], wantListing[i], moduleOf(wantListing[i]))
		}
	}
	haveSections, wantSections := sections(have), sections(want)
	for i := range max(len(haveSections), len(wantSections)) {
		switch {
		case i >= len(haveSections):
			return fmt.Sprintf("NOTICE lacks the section of %s", wantSections[i][0])
		case i >= len(wantSections):
			return fmt.Sprintf("NOTICE holds a section of %s, which the generator does not give", haveSections[i][0])
		}
		haveLines, wantLines := haveSections[i], wantSections[i]
		for n := range max(len(haveLines), len(wantLines)) {
			if n >= len(haveLines) || n >= len(wantLines) || haveLines[n] != wantLines[n] {
				return fmt.Sprintf("NOTICE's section of %s differs from the generator's at its line %d:\n  NOTICE:    %q\n  generator: %q",
					wantLines[0], n+1, lineOf(haveLines, n), lineOf(wantLines, n))
			}
		}
	}
	return "NOTICE differs from the generator's text outside every module's section"
}

// listing gives the lines of NOTICE that list a module and its licences.
func listing(text string) []string {
	head, _, _ := strings.Cut(text, "\n"+rule+"\n")
	var lines []string
	for _, line := range strings.Split(head, "\n") {
		if strings.HasPrefix(line, "  ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// listedModules gives the module of every listing line.
func listedModules(text string) []string {
	var modules []string
	for _, line := range listing(text) {
		modules = append(modules, moduleOf(line))
	}
	return modules
}

func moduleOf(line string) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(line, "  "), "  ")
	return strings.TrimSpace(name)
}

// sections splits NOTICE at its rules into the lines of each part after the
// first, so each part's first line names its module.
func sections(text string) [][]string {
	parts := strings.Split(text, "\n"+rule+"\n")
	var out [][]string
	for _, part := range parts[1:] {
		out = append(out, strings.Split(part, "\n"))
	}
	return out
}

func lineOf(lines []string, n int) string {
	if n < len(lines) {
		return lines[n]
	}
	return ""
}

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// listing names every file and directory under root with its size, in order.
func listing(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, fmt.Sprintf("%s %d %t", relative, info.Size(), entry.IsDir()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestWriteLeavesTheWorkingDirectoryClean runs -write and -check from a
// directory that holds the inputs, named by a relative path, with a costroid
// that writes a rows file and a result object into whatever directory it runs
// in. The directory reads the same afterwards, and the day still passes.
func TestWriteLeavesTheWorkingDirectoryClean(t *testing.T) {
	work := t.TempDir()
	keep(t, filepath.Join(work, "inputs"), passingDay(firstDay))
	keep(t, filepath.Join(work, "inputs"), passingDay("2026-11-11"))

	stub := filepath.Join(t.TempDir(), "costroid")
	script := fmt.Sprintf("#!/bin/sh\n: >costroid-rows.jsonl\n: >costroid-rows.run.json\nexec %q \"$@\"\n", costroid)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	before := listing(t, work)
	t.Chdir(work)
	page := filepath.Join(t.TempDir(), "accuracy.md")
	if got := runAccuracy(t, offline(t), "-write", page, "-inputs", "inputs", "-costroid", stub, "-changelog", changelogFile); got.code != 0 {
		t.Fatalf("-write exited %d: %s", got.code, got.stderr)
	}
	if got := runAccuracy(t, offline(t), "-check", page, "-days", "2", "-inputs", "inputs", "-costroid", stub, "-changelog", changelogFile); got.code != 0 {
		t.Fatalf("-check exited %d: %s", got.code, got.stderr)
	}
	if after := listing(t, work); !slices.Equal(before, after) {
		t.Errorf("the working directory changed.\n  before: %q\n  after:  %q", before, after)
	}
}

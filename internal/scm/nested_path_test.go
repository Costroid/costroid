package scm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// nestedFiles are files below the repository's root, by path, each with how
// its path reaches GitHub's contents API, escaped segment by segment, and
// GitLab's repository files API, escaped whole.
var nestedFiles = []struct {
	path, github, gitlab string
}{
	{".costroid/policy.rego", "/contents/.costroid/policy.rego?ref=", "/repository/files/.costroid%2Fpolicy.rego?ref="},
	{"policy/team packs/a#b.rego", "/contents/policy/team%20packs/a%23b.rego?ref=", "/repository/files/policy%2Fteam%20packs%2Fa%23b.rego?ref="},
}

// TestANestedPathIsEscapedBySegment reads files in directories through each
// host's reader, against stubs that read a path as the host does: GitHub's
// with each / kept and each segment escaped, since whether it reads %2F as a
// directory is not known, and GitLab's as one segment with each / escaped as
// %2F, as its API states. Each file reads whole, and the request names it as
// the host does. A path one level up the tree names no file.
func TestANestedPathIsEscapedBySegment(t *testing.T) {
	ctx := context.Background()
	github, gitlab := scmtest.New(t), scmtest.NewGitLab(t)
	gitlab.Files[sampleSHA] = map[string]string{}
	for _, file := range nestedFiles {
		github.Files[file.path] = "package costroid.pack\n"
		gitlab.Files[sampleSHA][file.path] = "package costroid.pack\n"
	}
	for name, read := range map[string]struct {
		reader interface {
			ReadFile(context.Context, string, string) ([]byte, error)
		}
		requests func() []string
		sent     func(int) string
	}{
		"GitHub": {adapter(t, github), func() []string { return github.Requests }, func(index int) string { return nestedFiles[index].github }},
		"GitLab": {gitLabAdapter(t, gitlab), func() []string { return gitlab.Requests }, func(index int) string { return nestedFiles[index].gitlab }},
	} {
		for index, file := range nestedFiles {
			data, err := read.reader.ReadFile(ctx, file.path, sampleSHA)
			if err != nil || string(data) != "package costroid.pack\n" {
				t.Errorf("%s: %s read as %q, %v", name, file.path, data, err)
			}
			requests := read.requests()
			if sent := requests[len(requests)-1]; !strings.Contains(sent, read.sent(index)) {
				t.Errorf("%s: %s was asked for as %s, want %s", name, file.path, sent, read.sent(index))
			}
		}
		if _, err := read.reader.ReadFile(ctx, "policy.rego", sampleSHA); !errors.Is(err, scm.ErrNotFound) {
			t.Errorf("%s: a file the tree does not hold at its root read: %v", name, err)
		}
	}
}

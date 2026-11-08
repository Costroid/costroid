// Package vcs reads the names a version control system gives a commit.
package vcs

import "regexp"

var commit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// IsCommit reports whether id is a full commit id in lower-case hex: 40
// digits for a SHA-1 repository, 64 for a SHA-256 one.
func IsCommit(id string) bool {
	return commit.MatchString(id)
}

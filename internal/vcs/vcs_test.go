package vcs

import (
	"strings"
	"testing"
)

func TestIsCommitTakesOnlyAFullLowerCaseId(t *testing.T) {
	sha1 := strings.Repeat("0123456789", 4)
	sha256 := strings.Repeat("abcdef0123456789", 4)
	for id, want := range map[string]bool{
		sha1:                           true,
		sha256:                         true,
		"":                             false,
		sha1[:39]:                      false,
		sha1 + "a":                     false,
		sha256[:63]:                    false,
		sha256 + "a":                   false,
		strings.ToUpper(sha256):        false,
		sha1[:39] + "g":                false,
		sha1 + "\n":                    false,
		" " + sha1:                     false,
		sha1 + strings.Repeat("0", 23): false,
	} {
		if got := IsCommit(id); got != want {
			t.Errorf("IsCommit(%q) = %v, want %v", id, got, want)
		}
	}
}

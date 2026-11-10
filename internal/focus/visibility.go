package focus

// TagRepoVisibility is the key version 0.4 of the convention adds: who can
// read the repository a row's run belongs to, as the CI system states it. No
// writer of rows asks a host for it, and a row whose CI system did not state
// it leaves the tag out.
const TagRepoVisibility = "costroid/repo_visibility"

// The values costroid/repo_visibility takes, and no other.
const (
	VisibilityPublic   = "public"
	VisibilityPrivate  = "private"
	VisibilityInternal = "internal"
)

// IsRepoVisibility reports whether value is one of the three values of
// costroid/repo_visibility. A value outside them never reaches a row.
func IsRepoVisibility(value string) bool {
	switch value {
	case VisibilityPublic, VisibilityPrivate, VisibilityInternal:
		return true
	}
	return false
}

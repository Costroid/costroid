package focus

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Tag keys version 0.3 of the convention adds. A caller names each one, and
// no writer of rows sets one on its own.
const (
	TagWorkItem = "costroid/work_item"
	TagArm      = "costroid/arm"
	TagTeam     = "costroid/team"
)

// keyPrefix opens every provider key this product reads. A key pasted where a
// label or an id belongs would otherwise reach every row and every ledger.
const keyPrefix = "sk-"

// ReadsLikeKey reports whether a value opens as every provider key this
// product reads does. A value that does never reaches a row.
func ReadsLikeKey(value string) bool {
	return strings.HasPrefix(value, keyPrefix)
}

// MaxDataLength caps any string that came from a pull request.
const MaxDataLength = 128

// DataPattern is the character allowlist every string from a pull request
// passes. It holds no character that can close a code span, open a tag or
// start a mention.
var DataPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+_-]*$`)

// NamedTags are the tags a caller names, by the key --tag takes for each.
var NamedTags = map[string]string{
	"work_item": TagWorkItem,
	"arm":       TagArm,
	"team":      TagTeam,
}

// workItemPattern is the one form costroid/work_item takes: an issue key, an
// upper-case project key, a hyphen and a number, as in ENG-142.
var workItemPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)

// maxWorkItem is the row contract's limit on costroid/work_item.
const maxWorkItem = 64

// ErrKeyShaped is a named tag's value that reads like a provider key.
var ErrKeyShaped = errors.New("takes a label, never a key: this value reads like a key")

// CheckNamedTag holds the value of one of NamedTags to its rule. The error
// says what the tag takes and never repeats the value.
func CheckNamedTag(tag, value string) error {
	switch {
	case ReadsLikeKey(value):
		return ErrKeyShaped
	case len(value) > MaxDataLength || !DataPattern.MatchString(value):
		return fmt.Errorf("takes 1 to %d letters, digits and . _ : / + -, opening with a letter or a digit", MaxDataLength)
	case tag == TagWorkItem && (len(value) > maxWorkItem || !workItemPattern.MatchString(value)):
		return fmt.Errorf("takes an issue key of at most %d characters: upper-case letters, digits and underscores, a hyphen and a number, as in ENG-142", maxWorkItem)
	}
	return nil
}

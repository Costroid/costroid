package comment

import "regexp"

// maxDataLength caps any string that came from the pull request.
const maxDataLength = 128

// notShown stands in for a string that failed the allowlist. The whole string
// is replaced, because a partly cleaned string can still mean something.
const notShown Data = "not-shown"

var dataPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+_-]*$`)

// Data is a string that originated in the pull request. Clean is the only way
// to make one from untrusted text, and the allowlist holds no character that
// can close a code span, open a tag or start a mention.
type Data string

// Clean returns text when it passes the allowlist and the length cap.
func Clean(text string) Data {
	if len(text) > maxDataLength || !dataPattern.MatchString(text) {
		return notShown
	}
	return Data(text)
}

// Code renders the string as a Markdown code span, so it never renders as
// Markdown or HTML.
func (d Data) Code() string {
	return "`" + string(d) + "`"
}

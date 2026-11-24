package policy

import (
	"strings"
	"testing"
)

// TestAFaultKeepsALineForEachThingTheCompilerFound holds what a reader is
// shown of the error a pack's child stated: the compiler's lines one to a
// line, without the lines of the pack's source it quotes under a fault, each
// cleaned of what a terminal or a log would act on and cut at 160 bytes, and
// at most eight of them, the last saying how many more the compiler wrote.
func TestAFaultKeepsALineForEachThingTheCompilerFound(t *testing.T) {
	two := "bundle activation failed: 2 errors occurred:\npack.rego:3: rego_type_error: undefined function http.send\npack.rego:5: rego_type_error: undefined function opa.runtime"
	if got := faultLines(two); got != two {
		t.Errorf("two faults read\n%s\nwant\n%s", got, two)
	}
	quoted := "1 error occurred: pack.rego:3: rego_parse_error: illegal number format\n\t1bad := true\n\t^"
	if got, want := faultLines(quoted), "1 error occurred: pack.rego:3: rego_parse_error: illegal number format"; got != want {
		t.Errorf("a fault with the source quoted under it reads %q, want %q", got, want)
	}
	hostile := "pack.rego:2: \x1b[31m::error::forged\r\n\n  pack.rego:4: a `mark` and a | pipe  "
	if got, want := faultLines(hostile), "pack.rego:2: ??31m::error::forged\npack.rego:4: a ?mark? and a ? pipe"; got != want {
		t.Errorf("a fault with control characters reads %q, want %q", got, want)
	}
	long := faultLines(strings.Repeat("x", 400) + "\nshort")
	if first, second, _ := strings.Cut(long, "\n"); len(first) != maxMessageBytes || second != "short" {
		t.Errorf("a line of 400 bytes is kept as %d, and the next as %q", len(first), second)
	}
	var many []string
	for range 12 {
		many = append(many, "pack.rego:3: rego_type_error: undefined function http.send")
	}
	lines := strings.Split(faultLines("12 errors occurred:\n"+strings.Join(many, "\n")), "\n")
	if len(lines) != maxFaultLines || lines[0] != "12 errors occurred:" || lines[maxFaultLines-1] != "and 6 more lines" {
		t.Errorf("thirteen lines are kept as %d, the last %q", len(lines), lines[len(lines)-1])
	}
	if got := strings.Split(faultLines("8 lines\n"+strings.Join(many[:7], "\n")), "\n"); len(got) != maxFaultLines || strings.HasPrefix(got[7], "and ") {
		t.Errorf("eight lines are kept as %d, the last %q", len(got), got[len(got)-1])
	}
	if faultLines("") != "" {
		t.Errorf("no error reads %q", faultLines(""))
	}
}

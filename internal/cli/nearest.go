package cli

import (
	"fmt"
	"strings"
)

// Nearest answers the names among names that typed most likely stands for,
// or none when none is near: the names one edit away, or else, for a word of
// five letters or more, the names two edits away, and otherwise the one name
// typed opens.
func Nearest(typed string, names []string) []string {
	limit := 1
	if len(typed) > 4 {
		limit = 2
	}
	var near []string
	best := limit
	for _, name := range names {
		switch distance := edits(typed, name); {
		case distance < best:
			near, best = []string{name}, distance
		case distance == best:
			near = append(near, name)
		}
	}
	if len(near) > 0 || len(typed) < 3 {
		return near
	}
	for _, name := range names {
		if strings.HasPrefix(name, typed) {
			near = append(near, name)
		}
	}
	if len(near) > 1 {
		return nil
	}
	return near
}

// NearestLine names what is nearest to a mistyped word as one sentence: kind
// is command or flag, and before stands before each name. It is empty when
// near is.
func NearestLine(kind, before string, near []string) string {
	named := make([]string, len(near))
	for i, name := range near {
		named[i] = before + name
	}
	switch len(named) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("The nearest %s is %s.", kind, named[0])
	}
	return fmt.Sprintf("The nearest %ss are %s and %s.", kind, strings.Join(named[:len(named)-1], ", "), named[len(named)-1])
}

// edits counts the single-character insertions, deletions, substitutions and
// swaps of neighbours that turn a into b.
func edits(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	var before []int
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
			if before != nil && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				current[j] = min(current[j], before[j-2]+1)
			}
		}
		if before == nil {
			before = make([]int, len(b)+1)
		}
		before, previous, current = previous, current, before
	}
	return previous[len(b)]
}

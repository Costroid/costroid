package main

import (
	"errors"
	"flag"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/focus"
)

// tagKeys are the keys --tag takes, each the name of a tag the row contract
// defines.
var tagKeys = map[string]string{
	"work_item": focus.TagWorkItem,
	"arm":       focus.TagArm,
	"team":      focus.TagTeam,
}

// workItemPattern is the one form costroid/work_item takes: an issue key, an
// upper-case project key, a hyphen and a number, as in ENG-142.
var workItemPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)

// maxWorkItem is the row contract's limit on costroid/work_item.
const maxWorkItem = 64

const rowFlagsHelp = `--tag KEY=VALUE puts the tag costroid/KEY on every row, once per key. KEY is
work_item, an issue key such as ENG-142; arm, an experiment's arm; or team.
A value is 1 to 128 letters, digits and . _ : / + -, opens with a letter or a
digit, and is never a key.

--focus-profile strict writes EffectiveCost as 0 on every row, as BilledCost
always is, and the measured figure in x_CostroidMeasuredCost, for a reader that
adds these rows to the provider's own FOCUS data. The default profile keeps the
figure in EffectiveCost. Send default rows to Costroid Platform and costroid
export. docs/tags.md and docs/focus-profile.md describe both.
`

// rowFlags are what a caller adds to every row a command writes.
type rowFlags struct {
	tags    *[]string
	profile *string
}

func addRowFlags(set *flag.FlagSet) rowFlags {
	flags := rowFlags{tags: &[]string{}}
	set.Func("tag", "KEY=VALUE, a tag on every row, with KEY one of work_item, arm and team", func(value string) error {
		*flags.tags = append(*flags.tags, value)
		return nil
	})
	flags.profile = set.String("focus-profile", string(focus.ProfileDefault), "default or strict: strict writes EffectiveCost as 0 and the figure in x_CostroidMeasuredCost")
	return flags
}

// shape reads the flags. A value is checked after parsing, so a refused one is
// never printed: it can be a key pasted by mistake.
func (r rowFlags) shape() (focus.Shape, error) {
	profile := focus.Profile(*r.profile)
	if !slices.Contains(focus.Profiles, profile) {
		return focus.Shape{}, fmt.Errorf("--focus-profile takes default or strict, not %q", *r.profile)
	}
	shape := focus.Shape{Profile: profile}
	for _, given := range *r.tags {
		name, value, found := strings.Cut(given, "=")
		key, named := tagKeys[name]
		switch {
		case !found:
			return focus.Shape{}, errors.New("--tag takes KEY=VALUE")
		case !named:
			return focus.Shape{}, errors.New("--tag takes the key work_item, arm or team, because the row contract defines no other")
		}
		if _, twice := shape.Tags[key]; twice {
			return focus.Shape{}, fmt.Errorf("--tag %s is given twice, and a row holds one", name)
		}
		if err := checkTagValue(name, value); err != nil {
			return focus.Shape{}, err
		}
		if shape.Tags == nil {
			shape.Tags = map[string]string{}
		}
		shape.Tags[key] = value
	}
	return shape, nil
}

// checkTagValue holds a value to the allowlist every string from a pull request
// passes, then to its key's own form.
func checkTagValue(name, value string) error {
	switch {
	case readsLikeKey(value):
		return fmt.Errorf("--tag %s takes a label, never a key: this value reads like a key", name)
	case comment.Clean(value) != comment.Data(value):
		return fmt.Errorf("--tag %s takes 1 to 128 letters, digits and . _ : / + -, opening with a letter or a digit", name)
	case name == "work_item" && (len(value) > maxWorkItem || !workItemPattern.MatchString(value)):
		return fmt.Errorf("--tag work_item takes an issue key of at most %d characters: upper-case letters, digits and underscores, a hyphen and a number, as in ENG-142", maxWorkItem)
	}
	return nil
}

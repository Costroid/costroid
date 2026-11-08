package main

import (
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/focus"
)

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
		key, named := focus.NamedTags[name]
		switch {
		case !found:
			return focus.Shape{}, errors.New("--tag takes KEY=VALUE")
		case !named:
			return focus.Shape{}, errors.New("--tag takes the key work_item, arm or team, because the row contract defines no other")
		}
		if _, twice := shape.Tags[key]; twice {
			return focus.Shape{}, fmt.Errorf("--tag %s is given twice, and a row holds one", name)
		}
		if err := focus.CheckNamedTag(key, value); err != nil {
			return focus.Shape{}, fmt.Errorf("--tag %s %w", name, err)
		}
		if shape.Tags == nil {
			shape.Tags = map[string]string{}
		}
		shape.Tags[key] = value
	}
	return shape, nil
}

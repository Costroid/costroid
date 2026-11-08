package focus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Tag keys version 0.3 of the convention adds. A caller names each one, and
// no writer of rows sets one on its own.
const (
	TagWorkItem = "costroid/work_item"
	TagArm      = "costroid/arm"
	TagTeam     = "costroid/team"
)

// Profile is how a row states the figure Costroid measured.
type Profile string

// The profiles a row can be written in.
const (
	// ProfileDefault keeps the figure in EffectiveCost, where Costroid
	// Platform reads it.
	ProfileDefault Profile = "default"
	// ProfileStrict writes EffectiveCost as 0, as BilledCost always is, and
	// moves the figure to x_CostroidMeasuredCost, so a reader that merges the
	// rows with the provider's own FOCUS data counts the cost once.
	ProfileStrict Profile = "strict"
)

// Profiles are the profiles in the order help names them.
var Profiles = []Profile{ProfileDefault, ProfileStrict}

// ColumnMeasuredCost is the column a strict row carries the default profile's
// EffectiveCost in, byte for byte.
const ColumnMeasuredCost = "x_CostroidMeasuredCost"

const (
	columnEffectiveCost = "EffectiveCost"
	columnTags          = "Tags"
	zeroCost            = `"0"`
)

// Shape is what a caller adds to every row a command writes: the tags it
// names and the profile.
type Shape struct {
	Tags    map[string]string
	Profile Profile
}

// errPartialRow is a write that ends inside a row. Writer writes whole lines,
// so the shape is never applied to part of one.
var errPartialRow = errors.New("focus: a write to shaped rows ends inside a row")

// Shaped returns a writer that takes rows as Writer writes them, whole JSON
// lines, and writes each to out with the shape applied. A shape that changes
// nothing returns out itself, so a default row reaches out as Writer wrote it.
func Shaped(out io.Writer, shape Shape) io.Writer {
	if len(shape.Tags) == 0 && shape.Profile != ProfileStrict {
		return out
	}
	return shaper{out: out, shape: shape}
}

type shaper struct {
	out   io.Writer
	shape Shape
}

func (s shaper) Write(p []byte) (int, error) {
	if !bytes.HasSuffix(p, []byte{'\n'}) {
		return 0, errPartialRow
	}
	var shaped bytes.Buffer
	encoder := json.NewEncoder(&shaped)
	encoder.SetEscapeHTML(false)
	for line := range bytes.Lines(p) {
		columns, err := s.shape.apply(line)
		if err != nil {
			return 0, err
		}
		if err := encoder.Encode(columns); err != nil {
			return 0, fmt.Errorf("focus: write a shaped row: %w", err)
		}
	}
	if _, err := s.out.Write(shaped.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// apply reads one row's columns and returns them shaped. The columns encode in
// the order Row declares them, because Row declares them sorted.
func (s Shape) apply(line []byte) (map[string]any, error) {
	var columns map[string]json.RawMessage
	if err := json.Unmarshal(line, &columns); err != nil {
		return nil, fmt.Errorf("focus: shape a row: %w", err)
	}
	shaped := make(map[string]any, len(columns)+1)
	for name, value := range columns {
		shaped[name] = value
	}
	if len(s.Tags) > 0 {
		tags, err := s.tagged(columns[columnTags])
		if err != nil {
			return nil, err
		}
		shaped[columnTags] = tags
	}
	if s.Profile == ProfileStrict {
		measured, found := columns[columnEffectiveCost]
		if !found {
			return nil, fmt.Errorf("focus: shape a row: it has no %s", columnEffectiveCost)
		}
		if _, found := columns[ColumnMeasuredCost]; found {
			return nil, fmt.Errorf("focus: shape a row: it already has %s", ColumnMeasuredCost)
		}
		shaped[ColumnMeasuredCost] = measured
		shaped[columnEffectiveCost] = json.RawMessage(zeroCost)
	}
	return shaped, nil
}

// tagged adds the shape's tags to a row's own. A tag the row already holds is
// never overwritten.
func (s Shape) tagged(raw json.RawMessage) (map[string]string, error) {
	tags := map[string]string{}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, fmt.Errorf("focus: shape a row: %s: %w", columnTags, err)
	}
	if tags == nil {
		tags = map[string]string{}
	}
	for key, value := range s.Tags {
		if _, held := tags[key]; held {
			return nil, fmt.Errorf("focus: shape a row: it already holds the tag %s", key)
		}
		tags[key] = value
	}
	return tags, nil
}

// Package config checks a costroid.yml against the published schema.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/focus"
)

const schemaURL = "costroid.schema.json"

// Problem is one place a file breaks the schema.
type Problem struct {
	// Path is where, as a JSON pointer. Empty means the document.
	Path    string
	Message string
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// Validate checks a file's bytes. An empty document is a valid file: every key
// is optional. A file that is empty, ~, null or only --- reads as absent, and a
// section with no value reads as that section absent, as the report reads
// them. The error is for what could not be read; a file that was read and
// breaks the schema answers Problems.
func Validate(data []byte) ([]Problem, error) {
	schema, err := compile()
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return []Problem{{Message: "not YAML: " + firstLine(err.Error())}}, nil
	}
	var document any
	if err := root.Decode(&document); err != nil {
		return []Problem{{Message: "not YAML: " + firstLine(err.Error())}}, nil
	}
	if document == nil {
		return nil, nil
	}
	if err := dropNullSections(document); err != nil {
		return nil, err
	}
	forms, err := amountForms(&root)
	if err != nil {
		return nil, err
	}
	forms = append(forms, dayForms(&root)...)
	models, err := modelForms(&root)
	if err != nil {
		return nil, err
	}
	forms = append(forms, models...)
	instance, err := jsonValue(document)
	switch {
	case err != nil && len(forms) > 0:
		// An amount such as .inf has no JSON form, and its own sentence says
		// what the file holds.
		return withForms(nil, forms), nil
	case err != nil:
		return []Problem{{Message: err.Error()}}, nil
	}
	return withForms(problemsOf(schema.Validate(instance)), forms), nil
}

// dropNullSections removes each section the file names with no value, such as
// approval: alone, which the report reads as that section's defaults. A
// section is a top-level key the schema states as a mapping. YAML decodes a
// mapping with a key that is not a string to map[any]any.
func dropNullSections(document any) error {
	sections, err := sectionKeys()
	if err != nil {
		return err
	}
	switch mapping := document.(type) {
	case map[string]any:
		for key, value := range mapping {
			if value == nil && sections[key] {
				delete(mapping, key)
			}
		}
	case map[any]any:
		for key, value := range mapping {
			if name, ok := key.(string); ok && value == nil && sections[name] {
				delete(mapping, key)
			}
		}
	}
	return nil
}

// sectionKeys are the top-level keys the schema states as a mapping.
func sectionKeys() (map[string]bool, error) {
	var document struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &document); err != nil {
		return nil, fmt.Errorf("config: read the schema: %w", err)
	}
	keys := map[string]bool{}
	for key, property := range document.Properties {
		if property.Type == "object" {
			keys[key] = true
		}
	}
	return keys, nil
}

// daysSentence is the approval rule's own sentence for an
// approval.expires_days it cannot read, so validate names it in the same
// words.
const daysSentence = "approval.expires_days must be a whole number of days from 1 to 90, written in plain digits such as 7"

// integerPattern is a plain non-negative decimal integer, as the approval
// rule reads one.
var integerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// dayForms finds an approval.expires_days the approval rule cannot read from
// its text: a YAML float such as 7.0, a string, or a whole number written as
// 0x10, 0o17, 017, 1_000 or +7. The schema reads the value a JSON parser would
// make of it, 7, 16, 15, 1000 or 7, and only the YAML node still says how it
// was written.
func dayForms(root *yaml.Node) []Problem {
	mapping := valueOf(root.Content[0], "approval")
	if mapping == nil {
		return nil
	}
	value := valueOf(mapping, "expires_days")
	if value == nil || value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
		return nil
	}
	if value.Tag != "!!int" || !integerPattern.MatchString(value.Value) {
		return []Problem{{Path: "/approval/expires_days", Message: daysSentence}}
	}
	return nil
}

// amountSentence is the policy's own sentence for an amount it cannot read,
// so validate names it in the same words.
const amountSentence = `policy.%s must be a plain decimal of at most %d fractional digits, such as 5, 2.50 or "2.50"`

// amountTags are the YAML tags a plain decimal resolves to: 5 is an integer,
// 2.50 a float and "2.50" a string.
var amountTags = []string{"!!int", "!!float", "!!str"}

// amountForms finds each policy amount the policy cannot read from its text,
// such as 1e3, 0x10, 017 or 1_000. The schema reads the value a JSON parser
// would make of it, 1000, 16, 15 or 1000, and only the YAML node still says how
// it was written. The text is held to the schema's own pattern and length.
func amountForms(root *yaml.Node) ([]Problem, error) {
	mapping := valueOf(root.Content[0], "policy")
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	amounts, err := amountKeys()
	if err != nil {
		return nil, err
	}
	var problems []Problem
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := resolved(mapping.Content[i]).Value, resolved(mapping.Content[i+1])
		form, ok := amounts[key]
		if !ok || value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
			continue
		}
		if !slices.Contains(amountTags, value.Tag) || len(value.Value) > form.maxLength || !form.pattern.MatchString(value.Value) {
			problems = append(problems, Problem{Path: "/policy/" + key, Message: fmt.Sprintf(amountSentence, key, focus.MaxScale)})
		}
	}
	return problems, nil
}

// textForm is how the schema states a value's text: an amount, or an entry
// of policy.models.
type textForm struct {
	pattern   *regexp.Regexp
	maxLength int
}

// amountKeys are the policy keys the schema states as an amount, a decimal
// quoted or not, each with the pattern and length its text must meet.
func amountKeys() (map[string]textForm, error) {
	var document struct {
		Properties struct {
			Policy struct {
				Properties map[string]struct {
					Type      any    `json:"type"`
					Pattern   string `json:"pattern"`
					MaxLength int    `json:"maxLength"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &document); err != nil {
		return nil, fmt.Errorf("config: read the schema: %w", err)
	}
	keys := map[string]textForm{}
	for key, property := range document.Properties.Policy.Properties {
		types, ok := property.Type.([]any)
		if !ok || !slices.Contains(types, any("number")) || !slices.Contains(types, any("string")) {
			continue
		}
		pattern, err := regexp.Compile(property.Pattern)
		if err != nil || property.Pattern == "" || property.MaxLength == 0 {
			return nil, fmt.Errorf("config: the schema states policy.%s with no usable pattern and length", key)
		}
		keys[key] = textForm{pattern: pattern, maxLength: property.MaxLength}
	}
	return keys, nil
}

// modelsSentence is the policy's own sentence for a policy.models it cannot
// read, so validate names it in the same words. The schema's description of
// the key states it too.
const modelsSentence = "policy.models must list distinct model ids, each of at most %d characters, or prefixes that end in *"

// modelForms holds policy.models to the policy's reading of its YAML nodes.
// The schema reads the list a JSON parser would make of it, and YAML makes a
// string of more than a string: a date such as 2026-11-14, a !!binary value,
// or a scalar under a tag of the file's own such as !x. The policy reads an
// entry only when YAML resolves it as a string, so validate refuses a list
// that holds any other, as it refuses an empty list, a repeated entry and an
// entry outside the schema's pattern and length. It names each in the
// policy's sentence, in place of what the schema says.
func modelForms(root *yaml.Node) ([]Problem, error) {
	mapping := valueOf(root.Content[0], "policy")
	if mapping == nil {
		return nil, nil
	}
	value := valueOf(mapping, "models")
	if value == nil {
		return nil, nil
	}
	form, err := modelEntryForm()
	if err != nil {
		return nil, err
	}
	refused := []Problem{{Path: "/policy/models", Message: fmt.Sprintf(modelsSentence, form.maxLength)}}
	if value.Kind != yaml.SequenceNode || len(value.Content) == 0 {
		return refused, nil
	}
	seen := make([]string, 0, len(value.Content))
	for _, entry := range value.Content {
		entry = resolved(entry)
		if entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" || len(entry.Value) > form.maxLength ||
			!form.pattern.MatchString(entry.Value) || slices.Contains(seen, entry.Value) {
			return refused, nil
		}
		seen = append(seen, entry.Value)
	}
	return nil, nil
}

// modelEntryForm is how the schema states an entry of policy.models.
func modelEntryForm() (textForm, error) {
	var document struct {
		Properties struct {
			Policy struct {
				Properties struct {
					Models struct {
						Items struct {
							Pattern   string `json:"pattern"`
							MaxLength int    `json:"maxLength"`
						} `json:"items"`
					} `json:"models"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &document); err != nil {
		return textForm{}, fmt.Errorf("config: read the schema: %w", err)
	}
	items := document.Properties.Policy.Properties.Models.Items
	pattern, err := regexp.Compile(items.Pattern)
	if err != nil || items.Pattern == "" || items.MaxLength == 0 {
		return textForm{}, errors.New("config: the schema states policy.models with no usable pattern and length")
	}
	return textForm{pattern: pattern, maxLength: items.MaxLength}, nil
}

// valueOf is the value of a mapping's key, nil when the node is not a mapping
// or lacks the key. An alias reads as the node it names, as the policy and
// the approval rule read it.
func valueOf(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if resolved(mapping.Content[i]).Value == key {
			return resolved(mapping.Content[i+1])
		}
	}
	return nil
}

// resolved is the node an alias names, and any other node itself.
func resolved(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

// withForms replaces what the schema says about a value written in a form
// its rule cannot read, and about any part of that value, with that rule's
// sentence.
func withForms(problems, forms []Problem) []Problem {
	for _, form := range forms {
		problems = slices.DeleteFunc(problems, func(problem Problem) bool {
			return problem.Path == form.Path || strings.HasPrefix(problem.Path, form.Path+"/")
		})
	}
	problems = append(problems, forms...)
	slices.SortFunc(problems, func(a, b Problem) int { return strings.Compare(a.String(), b.String()) })
	return problems
}

func compile() (*jsonschema.Schema, error) {
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(costroid.ConfigSchema))
	if err != nil {
		return nil, fmt.Errorf("config: read the schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, parsed); err != nil {
		return nil, fmt.Errorf("config: load the schema: %w", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("config: compile the schema: %w", err)
	}
	return schema, nil
}

// jsonValue turns a YAML document into what a JSON parser would have made of it.
// A mapping with a key that is not a string has no JSON form.
func jsonValue(document any) (any, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("every mapping key must be a string: %w", err)
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
}

func problemsOf(err error) []Problem {
	if err == nil {
		return nil
	}
	failure, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []Problem{{Message: firstLine(err.Error())}}
	}
	var problems []Problem
	collect(failure.BasicOutput(), &problems)
	slices.SortFunc(problems, func(a, b Problem) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(problems)
}

func collect(unit *jsonschema.OutputUnit, problems *[]Problem) {
	if unit == nil {
		return
	}
	if unit.Error != nil {
		*problems = append(*problems, Problem{Path: unit.InstanceLocation, Message: unit.Error.String()})
	}
	for i := range unit.Errors {
		collect(&unit.Errors[i], problems)
	}
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

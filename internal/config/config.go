// Package config checks a costroid.yml against the published schema.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
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
// is optional. The error is for what could not be read; a file that was read
// and breaks the schema answers Problems.
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
	instance, err := jsonValue(document)
	if err != nil {
		return []Problem{{Message: err.Error()}}, nil
	}
	floats, err := floatAmounts(&root)
	if err != nil {
		return nil, err
	}
	return withFloats(problemsOf(schema.Validate(instance)), floats), nil
}

// amountSentence is the policy's own sentence for an amount it cannot read,
// so validate refuses a float in the same words.
const amountSentence = `policy.%s must be a whole number or a quoted decimal of at most %d fractional digits, such as 5 or "5.00"`

// floatAmounts finds each policy amount whose value YAML reads as a float,
// such as 5.0 or 1e3, which the policy refuses. JSON has no float type of its
// own, so the schema reads 5.0 as the integer 5, and only the YAML node still
// says how the value was written.
func floatAmounts(root *yaml.Node) ([]Problem, error) {
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
		key, value := mapping.Content[i].Value, mapping.Content[i+1]
		if amounts[key] && value.Kind == yaml.ScalarNode && value.Tag == "!!float" {
			problems = append(problems, Problem{Path: "/policy/" + key, Message: fmt.Sprintf(amountSentence, key, focus.MaxScale)})
		}
	}
	return problems, nil
}

// amountKeys are the policy keys the schema states as an amount: a quoted
// decimal or a whole number.
func amountKeys() (map[string]bool, error) {
	var document struct {
		Properties struct {
			Policy struct {
				Properties map[string]struct {
					Type any `json:"type"`
				} `json:"properties"`
			} `json:"policy"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(costroid.ConfigSchema, &document); err != nil {
		return nil, fmt.Errorf("config: read the schema: %w", err)
	}
	keys := map[string]bool{}
	for key, property := range document.Properties.Policy.Properties {
		if types, ok := property.Type.([]any); ok && slices.Contains(types, any("integer")) && slices.Contains(types, any("string")) {
			keys[key] = true
		}
	}
	return keys, nil
}

func valueOf(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// withFloats replaces what the schema says about a float amount with the
// policy's sentence.
func withFloats(problems, floats []Problem) []Problem {
	for _, float := range floats {
		problems = slices.DeleteFunc(problems, func(problem Problem) bool { return problem.Path == float.Path })
	}
	problems = append(problems, floats...)
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

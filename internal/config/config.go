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
	var document any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return []Problem{{Message: "not YAML: " + firstLine(err.Error())}}, nil
	}
	if document == nil {
		return nil, nil
	}
	instance, err := jsonValue(document)
	if err != nil {
		return []Problem{{Message: err.Error()}}, nil
	}
	return problemsOf(schema.Validate(instance)), nil
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

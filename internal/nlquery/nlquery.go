// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package nlquery constructs natural-language translation prompts and parses
// their structured plans. It performs no I/O and has no access to stored rows.
package nlquery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
)

// Endpoint is one existing API resource a plan may name.
type Endpoint struct {
	Path string
}

// Endpoints is the closed natural-language and export resource vocabulary.
var Endpoints = map[string]Endpoint{
	"costs-daily":    {Path: "/api/v1/costs/daily"},
	"costs-summary":  {Path: "/api/v1/costs/summary"},
	"anomalies":      {Path: "/api/v1/anomalies"},
	"tokens":         {Path: "/api/v1/usage/tokens/daily"},
	"usage":          {Path: "/api/v1/usage/metrics/daily"},
	"unit-economics": {Path: "/api/v1/unit-economics/daily"},
}

// EndpointList is the stable user-facing spelling of Endpoints.
const EndpointList = "costs-daily, costs-summary, anomalies, tokens, usage, unit-economics"

// Plan is the only model-produced shape the query executor accepts. Pointer
// fields preserve the distinction between absent/null and a present value.
type Plan struct {
	Endpoint string  `json:"endpoint"`
	Start    *string `json:"start"`
	End      *string `json:"end"`
	GroupBy  *string `json:"groupBy"`
	TagKey   *string `json:"tagKey"`
	Currency *string `json:"currency"`
	Provider *string `json:"provider"`
	Metric   *string `json:"metric"`
}

// Values contains only the discovered metadata values permitted in a prompt.
type Values struct {
	Providers  []string `json:"providers"`
	TagKeys    []string `json:"tagKeys"`
	Currencies []string `json:"currencies"`
	Metrics    []string `json:"metrics"`
}

type promptDocument struct {
	Instruction string       `json:"instruction"`
	Question    string       `json:"question"`
	Today       string       `json:"today"`
	Schema      promptSchema `json:"schema"`
	Values      Values       `json:"values"`
}

type promptSchema struct {
	ObjectOnly bool     `json:"objectOnly"`
	Endpoints  []string `json:"endpoints"`
	GroupBy    []string `json:"groupBy"`
	DateFormat string   `json:"dateFormat"`
	Nullable   []string `json:"nullable"`
}

// BuildPrompt returns a deterministic JSON prompt containing only the user's
// question, the current date, the static plan schema, and the supplied
// permitted value lists.
//
// today is supplied by the caller rather than read here, both to keep this
// package free of ambient state and because the date is load-bearing: a
// question phrased as "last month" or "the last 90 days" can only be resolved
// against a known today. Some endpoints inject the date into their own chat
// template and some do not, so relying on that would make the resolved window
// depend on which endpoint an operator happened to configure, and a wrong
// window is the worst failure this feature has: it produces a confident answer
// over the wrong period rather than an error. Supplying it makes the window a
// function of this machine's clock and nothing else.
func BuildPrompt(question, today string, values Values) ([]byte, error) {
	endpoints := make([]string, 0, len(Endpoints))
	for name := range Endpoints {
		endpoints = append(endpoints, name)
	}
	sort.Strings(endpoints)
	values.Providers = sortedCopy(values.Providers)
	values.TagKeys = sortedCopy(values.TagKeys)
	values.Currencies = sortedCopy(values.Currencies)
	values.Metrics = sortedCopy(values.Metrics)
	doc := promptDocument{
		Instruction: "Translate the question into exactly one JSON object matching the schema. Resolve any relative date in the question against today. Every value is a single string or null, never an array or an object. Use null for every parameter that is not needed. Return JSON only.",
		Question:    question,
		Today:       today,
		Schema: promptSchema{
			ObjectOnly: true,
			Endpoints:  endpoints,
			GroupBy:    []string{"allocation", "provider", "region", "service", "subaccount", "tag"},
			DateFormat: "YYYY-MM-DD",
			Nullable:   []string{"start", "end", "groupBy", "tagKey", "currency", "provider", "metric"},
		},
		Values: values,
	}
	return json.Marshal(doc)
}

func sortedCopy(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

// ParseReply accepts exactly one bare JSON object with no unknown fields or
// trailing token. It never searches surrounding text for an embedded object.
func ParseReply(reply []byte) (Plan, error) {
	dec := json.NewDecoder(bytes.NewReader(reply))
	dec.DisallowUnknownFields()
	var plan Plan
	if err := dec.Decode(&plan); err != nil {
		return Plan{}, errors.New("the model reply was not a single JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Plan{}, errors.New("the model reply was not a single JSON object")
	}
	return plan, nil
}

// Query re-encodes typed plan fields. Nil values are omitted entirely.
func (p Plan) Query() string {
	q := url.Values{}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{name: "start", value: p.Start}, {name: "end", value: p.End},
		{name: "groupBy", value: p.GroupBy}, {name: "tagKey", value: p.TagKey},
		{name: "currency", value: p.Currency}, {name: "provider", value: p.Provider},
		{name: "metric", value: p.Metric},
	} {
		if field.value != nil {
			q.Set(field.name, *field.value)
		}
	}
	return q.Encode()
}

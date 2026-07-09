// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package allocation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Parse decodes and validates an allocation rules document from r, returning
// the single validated Dimension (this slice requires exactly one).
//
// It rejects unknown fields loudly (a misspelled "operater" fails with the
// decoder's actionable error; Go's field matching is case-insensitive, so
// case-variant spellings of correct names are accepted — stdlib behavior). An
// empty document, trailing data after the object, and any validation failure
// each produce an actionable, field-naming message. Parse reads r fully but
// stores nothing.
func Parse(r io.Reader) (Dimension, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Dimension{}, errors.New("allocation rules file is empty; expected a JSON object with a \"dimensions\" array")
		}
		return Dimension{}, fmt.Errorf("parsing allocation rules: %w", err)
	}

	// A second Decode must hit EOF: anything else is trailing data after the
	// single rules object.
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Dimension{}, errors.New("trailing data after the rules object; the file must contain exactly one JSON object")
	}

	if err := cfg.validate(); err != nil {
		return Dimension{}, err
	}
	return cfg.Dimensions[0], nil
}

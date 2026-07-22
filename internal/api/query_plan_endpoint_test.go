// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"testing"

	"github.com/Costroid/costroid/internal/nlquery"
)

func TestQueryPlanEndpointDocumentsEveryNLQueryEndpoint(t *testing.T) {
	for endpoint := range nlquery.Endpoints {
		if !QueryPlanEndpoint(endpoint).Valid() {
			t.Errorf("nlquery endpoint %q is absent from the generated contract enum", endpoint)
		}
	}

	// Go cannot enumerate generated enum constants, so this direction cannot
	// detect a contract enum member with no corresponding nlquery.Endpoints entry.
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package nlquery

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseReplyRequiresOneBareObject(t *testing.T) {
	valid := `{"endpoint":"costs-summary","start":null,"end":null,"groupBy":null,"tagKey":null,"currency":null,"provider":null,"metric":null}`
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{name: "leading prose no salvage", reply: "here is your plan: " + valid},
		{name: "code fence", reply: "```json\n" + valid + "\n```"},
		{name: "array", reply: "[" + valid + "]"},
		{name: "trailing object", reply: valid + `{}`},
		{name: "unknown key", reply: `{"endpoint":"costs-summary","extra":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseReply([]byte(tc.reply)); err == nil || err.Error() != "the model reply was not a single JSON object" {
				t.Fatalf("ParseReply error = %v", err)
			}
		})
	}
	if plan, err := ParseReply([]byte(valid)); err != nil || plan.Endpoint != "costs-summary" {
		t.Fatalf("valid reply: plan = %+v, err = %v", plan, err)
	}
}

func TestQueryOmitsNullFields(t *testing.T) {
	start := "2026-07-01"
	plan := Plan{Endpoint: "costs-summary", Start: &start}
	if got, want := plan.Query(), "start=2026-07-01"; got != want {
		t.Fatalf("Query() = %q, want %q", got, want)
	}
	if strings.Contains(plan.Query(), "currency") || strings.Contains(plan.Query(), "provider") {
		t.Fatalf("nil fields leaked into query %q", plan.Query())
	}
}

func TestPurePackageDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, forbidden := range []string{"net/http", "github.com/Costroid/costroid/internal/api", "github.com/Costroid/costroid/internal/storage"} {
		for _, dependency := range strings.Fields(string(output)) {
			if dependency == forbidden {
				t.Fatalf("pure package transitively imports %s", forbidden)
			}
		}
	}
}

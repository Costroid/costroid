// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

// whitelistKeys is the exact, fixed field set of a Message. A test that a
// regression could not silently pass: adding any field to Message fails the
// exact-count assertion below.
var whitelistKeys = []string{
	"kind", "source", "connector", "tenant", "outcome", "error",
	"periodsProcessed", "periodsSkipped", "recordsIngested", "startedAt", "finishedAt",
}

func TestBuildMessageWhitelistAndCardinalClean(t *testing.T) {
	run := storage.SyncRun{
		SourceName: "aws-prod", Connector: "aws-focus-s3", TenantID: "default",
		Outcome: "error", Error: "429 Too Many Requests",
		StartedAt:        time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC),
		FinishedAt:       time.Date(2026, 7, 17, 3, 5, 0, 0, time.UTC),
		PeriodsProcessed: 2, PeriodsSkipped: 1, RecordsIngested: 0,
	}
	msg := buildMessage(run, KindFailing)

	if msg.StartedAt != "2026-07-17T03:00:00Z" || msg.FinishedAt != "2026-07-17T03:05:00Z" {
		t.Fatalf("timestamps not RFC3339 UTC: started=%q finished=%q", msg.StartedAt, msg.FinishedAt)
	}

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(whitelistKeys) {
		t.Fatalf("payload has %d fields %v, want exactly the %d whitelisted keys", len(fields), mapKeys(fields), len(whitelistKeys))
	}
	allowed := make(map[string]bool, len(whitelistKeys))
	for _, k := range whitelistKeys {
		allowed[k] = true
		if _, ok := fields[k]; !ok {
			t.Errorf("payload missing whitelisted field %q", k)
		}
	}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("payload carries non-whitelisted field %q", k)
		}
	}
	// No cost amount ("$" / decimal money marker) and no credential marker of
	// our own. (The error field is a benign non-cost string here so the
	// assertion is about our payload, not a connector's error text.)
	for _, forbidden := range []string{"$", "Bearer"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("payload contains forbidden token %q: %s", forbidden, body)
		}
	}
}

func TestBuildMessageForcesUTC(t *testing.T) {
	plusTwo := time.FixedZone("UTC+2", 2*60*60)
	run := storage.SyncRun{
		SourceName: "s", Connector: "c", TenantID: "default", Outcome: "success",
		StartedAt:  time.Date(2026, 7, 17, 5, 0, 0, 0, plusTwo),
		FinishedAt: time.Date(2026, 7, 17, 5, 30, 0, 0, plusTwo),
	}
	msg := buildMessage(run, KindRecovered)
	if msg.StartedAt != "2026-07-17T03:00:00Z" || msg.FinishedAt != "2026-07-17T03:30:00Z" {
		t.Fatalf("non-UTC input not normalized to Z: started=%q finished=%q", msg.StartedAt, msg.FinishedAt)
	}
}

func TestSlackTextCarriesSourceAndOutcomeNoEmDash(t *testing.T) {
	run := storage.SyncRun{SourceName: "openai-cost", Connector: "openai-cost", TenantID: "default", Outcome: "error", Error: "429 Too Many Requests"}
	text := slackText(buildMessage(run, KindFailing))
	for _, want := range []string{"openai-cost", "error", "FAILING"} {
		if !strings.Contains(text, want) {
			t.Errorf("slack text %q missing %q", text, want)
		}
	}
	if strings.Contains(text, "—") {
		t.Errorf("slack text contains an em dash: %q", text)
	}
	recovered := slackText(buildMessage(storage.SyncRun{SourceName: "s", Connector: "c", Outcome: "success"}, KindRecovered))
	if !strings.Contains(recovered, "RECOVERED") || !strings.Contains(recovered, "success") {
		t.Errorf("recovered slack text = %q", recovered)
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

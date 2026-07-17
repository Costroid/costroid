// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

type fixedSyncSchedule []SyncScheduleSource

func (s fixedSyncSchedule) SyncSchedule() []SyncScheduleSource {
	return append([]SyncScheduleSource{}, s...)
}

func TestSyncStatusEndpointEnabledMerge(t *testing.T) {
	zone := time.FixedZone("test", 3*60*60)
	started := time.Date(2026, 7, 17, 10, 0, 0, 0, zone)
	finished := started.Add(2 * time.Minute)
	lastSuccess := finished
	store := &fakeStore{syncStatuses: []storage.SyncStatus{
		{
			Latest: storage.SyncRun{
				SourceName: "primary", Connector: "focus-csv", TenantID: "default",
				StartedAt: started, FinishedAt: finished, Outcome: "success",
				PeriodsProcessed: 2, PeriodsSkipped: 1, RecordsIngested: 4,
			},
			LastSuccessAt: &lastSuccess,
		},
		{Latest: storage.SyncRun{
			SourceName: "removed", Connector: "aws-focus", TenantID: "acme",
			StartedAt: started, FinishedAt: finished, Outcome: "error", Error: "file missing",
		}},
	}}
	next := finished.Add(time.Hour)
	schedule := fixedSyncSchedule{
		{Name: "primary", Connector: "focus-csv", Tenant: "default", Interval: "1h", NextRunAt: next},
		{Name: "new", Connector: "openai-cost", Tenant: "acme", Interval: "24h", NextRunAt: next},
	}
	recorder := httptest.NewRecorder()
	NewHandler("test", testStatic(), store, "", WithSyncSchedule(schedule)).ServeHTTP(
		recorder, httptest.NewRequest(http.MethodGet, "/api/v1/sync/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response SyncStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Enabled || len(response.Sources) != 3 {
		t.Fatalf("response = %+v", response)
	}
	primary, fresh, removed := response.Sources[0], response.Sources[1], response.Sources[2]
	if primary.Name != "primary" || primary.Interval == nil || *primary.Interval != "1h" || primary.NextRunAt == nil {
		t.Fatalf("primary live state = %+v", primary)
	}
	if primary.LastRun == nil || primary.LastRun.Outcome != Success || primary.LastRun.PeriodsProcessed != 2 || primary.LastRun.PeriodsSkipped != 1 || primary.LastRun.RecordsIngested != 4 {
		t.Fatalf("primary history = %+v", primary.LastRun)
	}
	if primary.LastRun.StartedAt.Format(time.RFC3339) != "2026-07-17T07:00:00Z" || primary.LastRun.FinishedAt.Format(time.RFC3339) != "2026-07-17T07:02:00Z" {
		t.Fatalf("timestamps are not RFC3339 UTC: %+v", primary.LastRun)
	}
	if primary.LastSuccessAt == nil || primary.LastSuccessAt.Format(time.RFC3339) != "2026-07-17T07:02:00Z" {
		t.Fatalf("lastSuccessAt = %v", primary.LastSuccessAt)
	}
	if fresh.Name != "new" || fresh.LastRun != nil || fresh.LastSuccessAt != nil {
		t.Fatalf("configured source without history = %+v", fresh)
	}
	if removed.Name != "removed" || removed.Interval != nil || removed.NextRunAt != nil || removed.LastRun == nil || removed.LastRun.Error == nil || *removed.LastRun.Error != "file missing" {
		t.Fatalf("history-only source = %+v", removed)
	}
	if !strings.Contains(recorder.Body.String(), `"startedAt":"2026-07-17T07:00:00Z"`) {
		t.Fatalf("wire timestamp is not RFC3339 UTC: %s", recorder.Body.String())
	}
}

func TestSyncStatusEndpointDisabledGroupingAndEmpty(t *testing.T) {
	base := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	t.Run("history grouped by source and tenant", func(t *testing.T) {
		store := &fakeStore{syncStatuses: []storage.SyncStatus{
			{Latest: storage.SyncRun{SourceName: "same", Connector: "focus-csv", TenantID: "acme", StartedAt: base, FinishedAt: base, Outcome: "error"}},
			{Latest: storage.SyncRun{SourceName: "same", Connector: "focus-csv", TenantID: "default", StartedAt: base, FinishedAt: base, Outcome: "success"}},
		}}
		recorder := httptest.NewRecorder()
		NewHandler("test", testStatic(), store, "").ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/sync/status", nil))
		var response SyncStatusResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Enabled || len(response.Sources) != 2 || response.Sources[0].Tenant == response.Sources[1].Tenant {
			t.Fatalf("response = %+v, want two tenant-keyed history rows", response)
		}
		for _, source := range response.Sources {
			if source.Interval != nil || source.NextRunAt != nil {
				t.Fatalf("disabled source leaked live scheduling fields: %+v", source)
			}
		}
	})
	t.Run("empty store", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		NewHandler("test", testStatic(), &fakeStore{}, "").ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/sync/status", nil))
		if got := recorder.Body.String(); got != "{\"enabled\":false,\"sources\":[]}\n" {
			t.Fatalf("empty response = %q", got)
		}
	})
}

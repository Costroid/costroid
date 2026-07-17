// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/storage"
)

type notifyingSyncRunRecorder struct {
	store *storage.DuckDB
	runs  chan storage.SyncRun
}

func (r notifyingSyncRunRecorder) RecordSyncRun(ctx context.Context, run storage.SyncRun) error {
	if err := r.store.RecordSyncRun(ctx, run); err != nil {
		return err
	}
	r.runs <- run
	return nil
}

func TestScheduledFocusCSVStatusIntegration(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	healthyPath := "../../testdata/focus-csv/focus-1.4.csv"
	missingPath := t.TempDir() + "/does-not-exist.csv"
	document := fmt.Sprintf(`{"sources":[
		{"name":"healthy","connector":"focus-csv","path":%q,"focusVersion":"1.4","interval":"24h"},
		{"name":"broken","connector":"focus-csv","path":%q,"focusVersion":"1.4","interval":"24h"}
	]}`, healthyPath, missingPath)
	cfg, err := parseSources(strings.NewReader(document))
	if err != nil {
		_ = store.Close()
		t.Fatalf("parseSources: %v", err)
	}
	clock := newFakeSchedulerClock(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	recorded := make(chan storage.SyncRun, 2)
	scheduler := newIngestScheduler(ctx, clock, notifyingSyncRunRecorder{store: store, runs: recorded},
		cfg.sources, runScheduledSource(store), nil)
	scheduler.Start()
	defer func() {
		scheduler.Stop()
		_ = store.Close()
	}()

	first, second := <-recorded, <-recorded
	if first.SourceName != "healthy" || first.Outcome != "success" || first.RecordsIngested == 0 {
		t.Fatalf("healthy run = %+v", first)
	}
	if second.SourceName != "broken" || second.Outcome != "error" || !strings.Contains(second.Error, "does-not-exist.csv") {
		t.Fatalf("broken run = %+v", second)
	}

	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	handler := api.NewHandler("test", static, store, "", api.WithSyncSchedule(scheduler))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/sync/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response api.SyncStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Enabled || len(response.Sources) != 2 {
		t.Fatalf("response = %+v", response)
	}
	byName := map[string]api.SyncSourceStatus{}
	for _, source := range response.Sources {
		byName[source.Name] = source
	}
	if byName["healthy"].LastRun == nil || byName["healthy"].LastRun.Outcome != api.Success || byName["healthy"].LastSuccessAt == nil {
		t.Fatalf("healthy status = %+v", byName["healthy"])
	}
	if byName["broken"].LastRun == nil || byName["broken"].LastRun.Outcome != api.Error || byName["broken"].LastSuccessAt != nil {
		t.Fatalf("broken status = %+v", byName["broken"])
	}
}

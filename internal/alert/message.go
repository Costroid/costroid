// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package alert delivers scheduled-ingest failure notifications to
// operator-configured webhook and Slack channels (N2 phase A). It is active
// only under `serve --sync` with configured channels: there is no default or
// built-in endpoint, so an unconfigured Costroid notifies nowhere.
//
// # Cardinal Rule
//
// An alert carries cost & usage METADATA only. Message is a fixed whitelist of
// operational fields built by a pure function over a storage.SyncRun (source,
// connector, and tenant identity, the run outcome, operational counts, RFC3339
// timestamps, and the connector's own error text). It never carries a cost
// amount, a credential, or any AI prompt or response content. The error text is
// the same string the operator already reads from GET /api/v1/sync/status; this
// package adds no figure of its own to it.
package alert

import (
	"time"

	"github.com/Costroid/costroid/internal/storage"
)

// TransitionKind labels why an alert is sent, per the edge-triggered
// storm-control state machine (see Notifier).
type TransitionKind string

const (
	// KindFailing is the healthy to failing edge: the first failing run.
	KindFailing TransitionKind = "failing"
	// KindReminder is a still-failing run at least the reminder interval after
	// the last alert for the same source.
	KindReminder TransitionKind = "reminder"
	// KindRecovered is the failing to healthy edge: the first success after a
	// failing streak.
	KindRecovered TransitionKind = "recovered"
)

// Message is the alert payload. Its fields are a fixed whitelist by
// construction: operational metadata only, never a cost amount, a credential,
// or AI content. It is marshalled verbatim as the webhook JSON body and
// summarized into the Slack text.
type Message struct {
	Kind      TransitionKind `json:"kind"`
	Source    string         `json:"source"`
	Connector string         `json:"connector"`
	Tenant    string         `json:"tenant"`
	Outcome   string         `json:"outcome"`
	// Error is the passed-through storage.SyncRun.Error: the connector's own
	// operational error text, already credential-scrubbed and already exposed to
	// the operator via GET /api/v1/sync/status. This package adds no figure of
	// its own to it (see the package Cardinal Rule note).
	Error            string `json:"error"`
	PeriodsProcessed int64  `json:"periodsProcessed"`
	PeriodsSkipped   int64  `json:"periodsSkipped"`
	RecordsIngested  int64  `json:"recordsIngested"`
	StartedAt        string `json:"startedAt"`
	FinishedAt       string `json:"finishedAt"`
}

// buildMessage is the PURE payload builder: a storage.SyncRun plus a transition
// kind become a Message with no side effect and no field beyond the whitelist.
// Timestamps are rendered RFC3339 in UTC.
func buildMessage(run storage.SyncRun, kind TransitionKind) Message {
	return Message{
		Kind:             kind,
		Source:           run.SourceName,
		Connector:        run.Connector,
		Tenant:           run.TenantID,
		Outcome:          run.Outcome,
		Error:            run.Error,
		PeriodsProcessed: run.PeriodsProcessed,
		PeriodsSkipped:   run.PeriodsSkipped,
		RecordsIngested:  run.RecordsIngested,
		StartedAt:        run.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt:       run.FinishedAt.UTC().Format(time.RFC3339),
	}
}

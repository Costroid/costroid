// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package gcpfocusbq

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

func TestTimestampFromMicrosExactAndRejectsScientific(t *testing.T) {
	timestamp, err := timestampFromMicros("1777591800123456", "ChargePeriodStart")
	if err != nil {
		t.Fatalf("timestampFromMicros: %v", err)
	}
	if got := timestamp.Format(time.RFC3339Nano); got != "2026-04-30T23:30:00.123456Z" {
		t.Fatalf("timestamp = %q, want exact microseconds", got)
	}
	if _, err := timestampFromMicros("1.777591800123456E9", "ChargePeriodStart"); err == nil ||
		!strings.Contains(err.Error(), "scientific/float timestamps are rejected") ||
		!strings.Contains(err.Error(), "ChargePeriodStart") {
		t.Fatalf("scientific timestamp error = %v", err)
	}
}

func TestPinnedPreviewSchemaCounts(t *testing.T) {
	var focusColumns, extensions, repeated int
	seen := map[string]bool{}
	for _, field := range PinnedFields {
		if seen[field.Name] {
			t.Fatalf("duplicate pinned field %q", field.Name)
		}
		seen[field.Name] = true
		if strings.HasPrefix(field.Name, "x_") {
			extensions++
		} else {
			focusColumns++
		}
		if field.Repeated {
			repeated++
		}
	}
	if len(PinnedFields) != 55 || focusColumns != 37 || extensions != 18 || repeated != 5 {
		t.Fatalf("pinned schema = total %d, FOCUS %d, extensions %d, repeated %d; want 55/37/18/5",
			len(PinnedFields), focusColumns, extensions, repeated)
	}
}

func TestServiceAccountRejectionsAreActionableAndNeverEcho(t *testing.T) {
	t.Run("authorized_user", func(t *testing.T) {
		marker := "AUTHORIZED-USER-CONTENT-MARKER"
		raw := []byte(`{"type":"authorized_user","refresh_token":"` + marker + `"}`)
		_, err := parseServiceAccount(raw)
		if err == nil || !strings.Contains(err.Error(), `type must be "service_account"`) || strings.Contains(err.Error(), marker) {
			t.Fatalf("error = %v, want expected type and no marker", err)
		}
	})
	t.Run("malformed_private_key", func(t *testing.T) {
		marker := "PRIVATE-KEY-MATERIAL-MARKER"
		raw, _ := json.Marshal(map[string]string{
			"type": "service_account", "client_email": "canary@example.test", "private_key": marker,
		})
		_, err := parseServiceAccount(raw)
		if err == nil || !strings.Contains(err.Error(), "PKCS#8 RSA key") || strings.Contains(err.Error(), marker) {
			t.Fatalf("error = %v, want actionable scrubbed key parse failure", err)
		}
	})
}

func TestNonLoopbackHTTPRejectedBeforeCredentialParsing(t *testing.T) {
	marker := []byte("credential-marker-that-must-not-be-parsed")
	_, err := NewClient(nil, "http://example.com", DefaultTokenURL, marker)
	if err == nil || strings.Contains(err.Error(), string(marker)) {
		t.Fatalf("error = %v", err)
	}
	// With a real HTTP client, endpoint validation still precedes malformed
	// credential parsing and names the non-loopback refusal.
	_, err = NewClient(&httpClientStub, "http://example.com", DefaultTokenURL, marker)
	if err == nil || !strings.Contains(err.Error(), "non-loopback") || strings.Contains(err.Error(), string(marker)) {
		t.Fatalf("error = %v, want non-loopback refusal without credential bytes", err)
	}
}

var httpClientStub = http.Client{}

func TestLabelsToTagsRejectsDuplicateKeys(t *testing.T) {
	raw := json.RawMessage(`[{"v":{"f":[{"v":"team"},{"v":"a"}]}},{"v":{"f":[{"v":"team"},{"v":"b"}]}}]`)
	_, err := labelsToTags(raw)
	if err == nil || !strings.Contains(err.Error(), `duplicate label key "team"`) {
		t.Fatalf("duplicate labels error = %v", err)
	}
}

func TestGapFillOnlyKnownIdentities(t *testing.T) {
	rec := focus.RawRecord{}
	GapFill(rec)
	want := focus.RawRecord{
		"ServiceCategory":   "Other",
		"InvoiceIssuerName": "Google Cloud",
		"ProviderName":      "Google Cloud",
	}
	if !maps.Equal(rec, want) {
		t.Fatalf("GapFill = %#v, want %#v", rec, want)
	}
	rec = focus.RawRecord{"ProviderName": "partner", "BilledCost": ""}
	GapFill(rec)
	if rec["ProviderName"] != "partner" || rec["BilledCost"] != "" {
		t.Fatalf("GapFill overwrote source identity or fabricated money: %#v", rec)
	}
}

// TestBigQueryScopeLiteralPin completes the scope-guard chain: fakebigquery
// validates the JWT scope claim against the shared BigQueryScope constant, so
// only this literal pin makes a mutation of the constant itself (for example to
// the nonexistent auth/bigquery.readonly) fail a test.
func TestBigQueryScopeLiteralPin(t *testing.T) {
	if BigQueryScope != "https://www.googleapis.com/auth/bigquery" {
		t.Fatalf("BigQueryScope = %q, want the literal https://www.googleapis.com/auth/bigquery", BigQueryScope)
	}
}

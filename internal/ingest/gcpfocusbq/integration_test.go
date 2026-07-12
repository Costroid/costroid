// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package gcpfocusbq_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/devtools/fakebigquery"
	"github.com/Costroid/costroid/internal/ingest/gcpfocusbq"
)

func runtimeServiceAccount(t *testing.T, email string) ([]byte, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": email,
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body, &key.PublicKey
}

func TestBigQueryRequestShapeSchemaProbePaginationAndPoll(t *testing.T) {
	fake := fakebigquery.New("../../../testdata/gcp-focus-bq/fixture")
	fake.SchemaAdditions = []string{"x_FuturePreviewColumn"}
	credential, public := runtimeServiceAccount(t, "shape@example.test")
	fake.AllowServiceAccount("shape@example.test", public)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	client, err := gcpfocusbq.NewClient(http.DefaultClient, server.URL+"/bigquery/v2/", server.URL+"/token", credential)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	coords := gcpfocusbq.Coordinates{
		DatasetProject: "billing-host", Dataset: "gcp_billing_immutable_demo_EU",
		Table: "gcp_billing_export_focus_demo", Location: "EU", JobProject: "query-project", Since: "2026-05",
	}
	periods, err := gcpfocusbq.Discover(context.Background(), client, coords, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(periods) != 2 || periods[0].Billing != "2026-05" || periods[1].Billing != "2026-06" {
		t.Fatalf("periods = %#v", periods)
	}
	probe := client.ProbeResult()
	if probe.TimePartitioning || len(probe.AdditiveColumns) != 1 || probe.AdditiveColumns[0] != "x_FuturePreviewColumn" {
		t.Fatalf("probe = %#v", probe)
	}

	reader, err := periods[0].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("May Records: %v", err)
	}
	first, err := reader.Next()
	if err != nil {
		t.Fatalf("May row 1: %v", err)
	}
	if got := first.Record["ChargePeriodStart"]; got != "2026-04-30T23:30:00.123456Z" {
		t.Errorf("ChargePeriodStart = %q", got)
	}
	if got := first.Record["BilledCost"]; got != "1.123456789012345678" {
		t.Errorf("BilledCost = %q", got)
	}
	if got := first.Record["EffectiveCost"]; got != "1.123456789012345678" {
		t.Errorf("EffectiveCost = %q", got)
	}
	if got := first.Record["Tags"]; got != `{"env":"prod","team":"platform"}` {
		t.Errorf("Tags = %q", got)
	}
	if _, read := first.Record["x_Credits"]; read {
		t.Error("x_Credits unexpectedly reached the FOCUS raw record")
	}
	second, err := reader.Next()
	if err != nil {
		t.Fatalf("May row 2: %v", err)
	}
	if second.Record["ProviderName"] != "Google Cloud" || second.Record["ServiceCategory"] != "Other" || second.Record["InvoiceIssuerName"] != "Google Cloud" {
		t.Errorf("gap-filled row = %#v", second.Record)
	}
	if second.Record["x_ExportTime"] != "" {
		t.Errorf("null x_ExportTime = %q", second.Record["x_ExportTime"])
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("May terminal error = %v", err)
	}
	_ = reader.Close()

	reader, err = periods[1].Conn.Records(context.Background())
	if err != nil {
		t.Fatalf("June Records/poll: %v", err)
	}
	count := 0
	for {
		_, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("June row %d: %v", count+1, err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("June rows = %d, want 3 across delayed poll + enforced pages", count)
	}
	calls := strings.Join(fake.Calls(), "\n")
	for _, want := range []string{"token iss=shape@example.test", "tables.get", "jobs.query aggregate", "jobs.query period=2026-05", "jobs.query period=2026-06", "jobs.getQueryResults period=2026-06 offset=0", "jobs.getQueryResults period=2026-06 offset=2"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls missing %q:\n%s", want, calls)
		}
	}

	// Removing any selected column must fail at the probe before another
	// aggregate query is accepted.
	fake.MissingColumn = "BilledCost"
	_, err = gcpfocusbq.Discover(context.Background(), client, coords, nil)
	if err == nil || !strings.Contains(err.Error(), "missing selected column(s): BilledCost") {
		t.Fatalf("missing-column error = %v", err)
	}
}

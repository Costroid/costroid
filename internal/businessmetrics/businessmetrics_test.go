// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package businessmetrics

import (
	"strings"
	"testing"
	"time"
)

func TestParseValidAndHeaderOnly(t *testing.T) {
	rows, err := Parse(strings.NewReader("date,metric,quantity\n2026-05-02,requests served,12345678901234567.89\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != 1 || !rows[0].MetricDay.Equal(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)) || rows[0].MetricName != "requests served" || rows[0].Quantity.String() != "12345678901234567.89" {
		t.Fatalf("rows = %+v", rows)
	}
	empty, err := Parse(strings.NewReader("date,metric,quantity\n"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("header-only Parse = (%+v, %v), want empty success", empty, err)
	}
}

func TestParseRejectsEveryValidationClass(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want []string
	}{
		{name: "empty file", csv: "", want: []string{"empty", "date,metric,quantity"}},
		{name: "header names", csv: "metric,date,quantity\n", want: []string{"header", "exactly date,metric,quantity"}},
		{name: "header field count", csv: "date,metric\n", want: []string{"header", "exactly date,metric,quantity"}},
		{name: "data field count", csv: "date,metric,quantity\n2026-05-01,requests\n", want: []string{"record 1", "wrong number of fields"}},
		{name: "date width", csv: "date,metric,quantity\n2026-5-01,requests,1\n", want: []string{"record 1 date", "exactly YYYY-MM-DD"}},
		{name: "date invalid", csv: "date,metric,quantity\n2026-02-30,requests,1\n", want: []string{"record 1 date", "exactly YYYY-MM-DD"}},
		{name: "empty metric", csv: "date,metric,quantity\n2026-05-01,,1\n", want: []string{"record 1 metric", "non-empty"}},
		{name: "metric leading whitespace", csv: "date,metric,quantity\n2026-05-01, requests,1\n", want: []string{"record 1 metric", "leading or trailing whitespace"}},
		{name: "metric trailing whitespace", csv: "date,metric,quantity\n2026-05-01,requests ,1\n", want: []string{"record 1 metric", "leading or trailing whitespace"}},
		{name: "quantity invalid", csv: "date,metric,quantity\n2026-05-01,requests,nope\n", want: []string{"record 1 quantity", "valid decimal"}},
		{name: "quantity zero", csv: "date,metric,quantity\n2026-05-01,requests,0\n", want: []string{"record 1 quantity", "greater than zero"}},
		{name: "quantity negative", csv: "date,metric,quantity\n2026-05-01,requests,-1\n", want: []string{"record 1 quantity", "greater than zero"}},
		{name: "fractional capacity", csv: "date,metric,quantity\n2026-05-01,requests,0.0000000000000000001\n", want: []string{"record 1 quantity", "more than 18 fractional digits", "never rounds"}},
		{name: "integer capacity", csv: "date,metric,quantity\n2026-05-01,requests,100000000000000000000\n", want: []string{"record 1 quantity", "more than 20 integer digits", "never truncates"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.csv))
			if err == nil {
				t.Fatal("Parse = nil error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestParseRejectsDuplicateByDataRecordNumber(t *testing.T) {
	input := "date,metric,quantity\n" +
		"2026-05-01,requests,1\n" +
		"2026-05-02,\"metric with\na quoted newline\",2\n" +
		"2026-05-01,requests,3\n"
	_, err := Parse(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "record 3 duplicates") || !strings.Contains(err.Error(), "record 1") || !strings.Contains(err.Error(), "requests") {
		t.Fatalf("duplicate error = %v, want records 3 and 1 plus metric", err)
	}
}

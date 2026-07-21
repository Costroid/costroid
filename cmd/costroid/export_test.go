// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exportFixtureDir = "../../testdata/export"

func readExportFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(exportFixtureDir, name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func assertCSVMatchesExpected(t *testing.T, got []byte, expectedName string) {
	t.Helper()
	wantWithBOM := readExportFixture(t, expectedName)
	if len(wantWithBOM) < 3 || !bytes.Equal(wantWithBOM[:3], utf8BOM) {
		t.Fatalf("%s must start with UTF-8 BOM", expectedName)
	}
	// --out form: serializers + BOM
	if !bytes.Equal(append(utf8BOM, got...), wantWithBOM) {
		t.Fatalf("csv+BOM != %s\ngot:\n%q\nwant:\n%q", expectedName, append(utf8BOM, got...), wantWithBOM)
	}
	// stdout form: expected minus BOM
	if !bytes.Equal(got, wantWithBOM[3:]) {
		t.Fatalf("csv stdout != %s without BOM\ngot:\n%q\nwant:\n%q", expectedName, got, wantWithBOM[3:])
	}
}

func TestDailyCostsToCSVGolden(t *testing.T) {
	var costs dailyCostsWire
	if err := json.Unmarshal(readExportFixture(t, "daily-costs.json"), &costs); err != nil {
		t.Fatal(err)
	}
	assertCSVMatchesExpected(t, dailyCostsToCSV(costs), "daily-costs.expected.csv")
}

func TestLessByUTF16SortTrap(t *testing.T) {
	// U+1F600 (emoji) sorts BEFORE U+E000 (PUA) in UTF-16 code units
	// (0xD83D < 0xE000) but AFTER it in UTF-8 byte order. A sort.Strings
	// implementation puts the PUA key first and fails the golden.
	emoji := "😀emoji"
	pua := "\ue000pua"
	if !lessByUTF16(emoji, pua) {
		t.Fatalf("lessByUTF16(%q, %q) = false, want true (UTF-16 order)", emoji, pua)
	}
	if lessByUTF16(pua, emoji) {
		t.Fatalf("lessByUTF16(%q, %q) = true, want false", pua, emoji)
	}
	keys := []string{"Beta", emoji, "Alpha, Inc", pua}
	sortByUTF16(keys)
	want := []string{"Alpha, Inc", "Beta", emoji, pua}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("sortByUTF16 = %v, want %v", keys, want)
	}
}

func TestTokensPivotAndCSVGolden(t *testing.T) {
	var raw []dailyTokenUsageWire
	if err := json.Unmarshal(readExportFixture(t, "tokens.json"), &raw); err != nil {
		t.Fatal(err)
	}
	var wantDays []tokenDayGroup
	if err := json.Unmarshal(readExportFixture(t, "tokens.days.json"), &wantDays); err != nil {
		t.Fatal(err)
	}
	gotDays := pivotTokensByDate(raw)
	gotJSON, err := json.Marshal(gotDays)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(wantDays)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("pivotTokensByDate semantic mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
	assertCSVMatchesExpected(t, dailyTokensToCSV(gotDays), "tokens.expected.csv")
}

func TestUsageMetricsToCSVGolden(t *testing.T) {
	var rows []dailyUsageMetricWire
	if err := json.Unmarshal(readExportFixture(t, "usage.json"), &rows); err != nil {
		t.Fatal(err)
	}
	assertCSVMatchesExpected(t, usageMetricsToCSV(rows), "usage.expected.csv")
}

func TestUnitEconomicsToCSVGolden(t *testing.T) {
	var economics unitEconomicsWire
	if err := json.Unmarshal(readExportFixture(t, "unit-economics.json"), &economics); err != nil {
		t.Fatal(err)
	}
	assertCSVMatchesExpected(t, unitEconomicsToCSV(economics), "unit-economics.expected.csv")
}

func TestAnomaliesToCSVGolden(t *testing.T) {
	var a anomaliesWire
	if err := json.Unmarshal(readExportFixture(t, "anomalies.json"), &a); err != nil {
		t.Fatal(err)
	}
	assertCSVMatchesExpected(t, anomaliesToCSV(a), "anomalies.expected.csv")
}

func TestCostsSummaryToCSVAbsentAndPresent(t *testing.T) {
	prev := "10"
	delta := "5"
	summary := costsSummaryWire{
		Keys: []costSummaryKeyWire{
			{Key: "beta", Total: "15", PreviousTotal: &prev, Delta: &delta},
			{Key: "alpha", Total: "3"}, // absent previousTotal/delta = empty cells
		},
	}
	got := costsSummaryToCSV(summary)
	want := []byte("Key,Total,Previous total,Delta\r\n" +
		"beta,15,10,5\r\n" +
		"alpha,3,,\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("costsSummaryToCSV =\n%q\nwant\n%q", got, want)
	}
	// Fabricating 0 for absence would corrupt newness semantics.
	if bytes.Contains(got, []byte("alpha,3,0,0")) {
		t.Fatal("absent previousTotal/delta must not become 0")
	}
}

func TestEmptyResourceCSVHeaders(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{"costs-daily", dailyCostsToCSV(dailyCostsWire{}), "Date,Total (net)\r\n"},
		{"tokens", dailyTokensToCSV(nil), "Date,Total\r\n"},
		{"usage", usageMetricsToCSV(nil), "Date,Service,Tier,Metric,Unit,Quantity\r\n"},
		{"unit-economics", unitEconomicsToCSV(unitEconomicsWire{}), "Date,Cost,Quantity,Unit cost\r\n"},
		{"costs-summary", costsSummaryToCSV(costsSummaryWire{}), "Key,Total,Previous total,Delta\r\n"},
		{"anomalies", anomaliesToCSV(anomaliesWire{}), "Date,Scope,Key,Direction,Observed,Median,MAD,Scaled MAD,Threshold,Deviation\r\n"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Errorf("%s empty = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestCSVQuotingRFC4180(t *testing.T) {
	if got := csvField(`a,b`); got != `"a,b"` {
		t.Errorf("comma: %q", got)
	}
	if got := csvField(`say "hi"`); got != `"say ""hi"""` {
		t.Errorf("quote: %q", got)
	}
	if got := csvField("line\nbreak"); got != "\"line\nbreak\"" {
		t.Errorf("lf: %q", got)
	}
	if got := csvField("plain"); got != "plain" {
		t.Errorf("plain: %q", got)
	}
}

func TestSumIntegerStrings(t *testing.T) {
	if got := sumIntegerStrings([]string{"1", "2"}); got == nil || *got != "3" {
		t.Fatalf("1+2 = %v", got)
	}
	if got := sumIntegerStrings([]string{"x", "1"}); got != nil {
		t.Fatalf("x+1 should be nil, got %v", got)
	}
	if got := sumIntegerStrings([]string{"-5"}); got != nil {
		t.Fatalf("signed -5 is not a non-neg integer, got %v", got)
	}
	if got := sumIntegerStrings(nil); got == nil || *got != "0" {
		t.Fatalf("empty = %v, want 0", got)
	}
	// Pairwise fold recovery: x,1,2 -> 3
	cell := "x"
	for _, incoming := range []string{"1", "2"} {
		if summed := sumIntegerStrings([]string{cell, incoming}); summed != nil {
			cell = *summed
		} else {
			cell = incoming
		}
	}
	if cell != "3" {
		t.Fatalf("pairwise fold x,1,2 = %q, want 3", cell)
	}
}

func TestUsageDocumentsExport(t *testing.T) {
	err := run(nil)
	if err == nil {
		t.Fatal("run(nil) = nil")
	}
	blob := err.Error()
	for _, want := range []string{
		"export",
		"costs-daily",
		"costs-summary",
		"anomalies",
		"tokens",
		"usage",
		"unit-economics",
		"group-by",
		"service|provider|allocation|subaccount|region|tag",
		"stop 'costroid serve'",
		"BOM",
		"One-shot",
		"scheduling",
		"--format",
		"csv|json",
		"db-encryption-key-file",
	} {
		if !strings.Contains(blob, want) {
			t.Errorf("top-level usage missing %q", want)
		}
	}
}

// --- end-to-end: real store + real handler via runCLI ---

const exportSeedPath = "../../testdata/export/seed-aws-focus.csv.gz"

// seedExportStore ingests the purpose-written FOCUS seed into a fresh data dir.
func seedExportStore(t *testing.T) {
	t.Helper()
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportSeedPath}, "")
	if err != nil {
		t.Fatalf("seed ingest: %v\nout: %s", err, out)
	}
}

func TestExportCostsDailyRoundTrip(t *testing.T) {
	seedExportStore(t)

	out, err := runCLI([]string{"export", "costs-daily"}, "")
	if err != nil {
		t.Fatalf("export costs-daily: %v\nout: %s", err, out)
	}
	// Full-capture equality: silent success, no BOM, CRLF, quoted comma name,
	// 18-decimal cost verbatim.
	want := "Date,\"alpha, corp\",beta corp,Total (net)\r\n" +
		"2026-05-15,50.5,100.123456789012345678,150.623456789012345678\r\n"
	if out != want {
		t.Fatalf("costs-daily stdout:\n%q\nwant:\n%q", out, want)
	}
	if strings.HasPrefix(out, "\ufeff") || bytes.HasPrefix([]byte(out), utf8BOM) {
		t.Fatal("stdout must not carry a BOM")
	}
}

func TestExportEveryResourceSmoke(t *testing.T) {
	seedExportStore(t)

	t.Run("costs-summary", func(t *testing.T) {
		// Wire order is total-desc: beta before alpha. previousTotal/delta
		// absent on a single-window seed -> empty cells.
		out, err := runCLI([]string{"export", "costs-summary"}, "")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		want := "Key,Total,Previous total,Delta\r\n" +
			"beta corp,100.123456789012345678,,\r\n" +
			"\"alpha, corp\",50.5,,\r\n"
		if out != want {
			t.Fatalf("costs-summary:\n%q\nwant:\n%q", out, want)
		}
	})

	t.Run("anomalies-header", func(t *testing.T) {
		out, err := runCLI([]string{"export", "anomalies"}, "")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		wantHeader := "Date,Scope,Key,Direction,Observed,Median,MAD,Scaled MAD,Threshold,Deviation\r\n"
		if out != wantHeader {
			// Seed is too short for the detector to flag rows; header-only is expected.
			if !strings.HasPrefix(out, wantHeader) {
				t.Fatalf("anomalies missing header:\n%q", out)
			}
		}
	})

	t.Run("tokens", func(t *testing.T) {
		out, err := runCLI([]string{"export", "tokens"}, "")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		want := "Date,beta corp,Total\r\n" +
			"2026-05-15,1500,1500\r\n"
		if out != want {
			t.Fatalf("tokens:\n%q\nwant:\n%q", out, want)
		}
	})

	t.Run("unit-economics", func(t *testing.T) {
		metricsPath := filepath.Join(t.TempDir(), "metrics.csv")
		if err := os.WriteFile(metricsPath, []byte("date,metric,quantity\n2026-05-15,requests,2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runCLI([]string{"metrics", "import", "--path", metricsPath}, ""); err != nil {
			t.Fatalf("metrics import: %v\n%s", err, out)
		}
		out, err := runCLI([]string{"export", "unit-economics", "--metric", "requests"}, "")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		// unitCost = 150.623456789012345678 / 2 = 75.311728394506172839
		want := "Date,Cost,Quantity,Unit cost\r\n" +
			"2026-05-15,150.623456789012345678,2,75.311728394506172839\r\n"
		if out != want {
			t.Fatalf("unit-economics:\n%q\nwant:\n%q", out, want)
		}
	})

	t.Run("usage-empty", func(t *testing.T) {
		// usage_metrics is populated only by AI-vendor connectors; the aws-focus
		// seed leaves it empty. Assert the collapsed header-only CSV.
		out, err := runCLI([]string{"export", "usage"}, "")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		want := "Date,Service,Tier,Metric,Unit,Quantity\r\n"
		if out != want {
			t.Fatalf("usage:\n%q\nwant:\n%q", out, want)
		}
	})
}

func TestExportFormatJSON(t *testing.T) {
	seedExportStore(t)

	out1, err := runCLI([]string{"export", "costs-daily", "--format", "json"}, "")
	if err != nil {
		t.Fatalf("%v\n%s", err, out1)
	}
	if !strings.HasPrefix(out1, "{") {
		t.Fatalf("json should start with {{, got %q", out1[:min(20, len(out1))])
	}
	if !strings.Contains(out1, `"100.123456789012345678"`) {
		t.Fatal("json must contain the 18-decimal cost as a string")
	}
	var probe any
	if err := json.Unmarshal([]byte(out1), &probe); err != nil {
		t.Fatalf("json parse: %v", err)
	}
	out2, err := runCLI([]string{"export", "costs-daily", "--format", "json"}, "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if out1 != out2 {
		t.Fatal("json export is not deterministic across invocations")
	}
	if bytes.HasPrefix([]byte(out1), utf8BOM) {
		t.Fatal("json must never carry a BOM")
	}
}

func TestExportOutFile(t *testing.T) {
	seedExportStore(t)
	dir := t.TempDir()

	csvPath := filepath.Join(dir, "out.csv")
	out, err := runCLI([]string{"export", "costs-daily", "--out", csvPath}, "")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out != "" {
		t.Fatalf("--out must leave stdout empty, got %q", out)
	}
	body, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(body, utf8BOM) {
		t.Fatalf("csv --out must start with BOM, got %q", body[:min(8, len(body))])
	}
	if !bytes.Contains(body, []byte("100.123456789012345678")) {
		t.Fatal("csv --out missing 18-decimal cost")
	}

	jsonPath := filepath.Join(dir, "out.json")
	out, err = runCLI([]string{"export", "costs-daily", "--format", "json", "--out", jsonPath}, "")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out != "" {
		t.Fatalf("json --out must leave stdout empty, got %q", out)
	}
	jbody, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(jbody, utf8BOM) {
		t.Fatal("json --out must not carry a BOM")
	}
	if !bytes.HasPrefix(jbody, []byte("{")) {
		t.Fatalf("json --out should start with {{, got %q", jbody[:min(8, len(jbody))])
	}
}

func TestExportFlagErrorMatrix(t *testing.T) {
	seedExportStore(t)

	t.Run("unknown resource", func(t *testing.T) {
		out, err := runCLI([]string{"export", "nope"}, "")
		if err == nil {
			t.Fatal("expected error")
		}
		blob := err.Error() + out
		if !strings.Contains(blob, "unknown export resource") {
			t.Fatalf("want unknown resource message, got %q", blob)
		}
		for _, r := range []string{"costs-daily", "costs-summary", "anomalies", "tokens", "usage", "unit-economics"} {
			if !strings.Contains(blob, r) {
				t.Errorf("error should list %q: %s", r, blob)
			}
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		out, err := runCLI([]string{"export", "costs-daily", "--not-a-flag"}, "")
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, errReported) {
			t.Fatalf("want errReported, got %v", err)
		}
		if !strings.Contains(out, "flag provided but not defined") {
			t.Fatalf("want flag error in captured output, got %q", out)
		}
	})

	t.Run("tag without tag-key relays API", func(t *testing.T) {
		out, err := runCLI([]string{"export", "costs-daily", "--group-by", "tag"}, "")
		if err == nil {
			t.Fatal("expected error")
		}
		blob := err.Error() + out
		if !strings.Contains(blob, "groupBy=tag requires the tagKey parameter") {
			t.Fatalf("want API tagKey message, got %q", blob)
		}
	})

	t.Run("allocation unconfigured relays API", func(t *testing.T) {
		// Point allocation rules at a missing path so the API's unconfigured
		// path is reached (flag precedence over UserConfigDir).
		missing := filepath.Join(t.TempDir(), "no-such-rules.json")
		out, err := runCLI([]string{
			"export", "costs-daily",
			"--group-by", "allocation",
			"--allocation-rules", missing,
		}, "")
		if err == nil {
			t.Fatal("expected error")
		}
		blob := err.Error() + out
		// Missing file surfaces a path-naming 400 from the API.
		if !strings.Contains(blob, "allocation") {
			t.Fatalf("want allocation error, got %q", blob)
		}
	})

	t.Run("metric omitted relays API", func(t *testing.T) {
		out, err := runCLI([]string{"export", "unit-economics"}, "")
		if err == nil {
			t.Fatal("expected error")
		}
		blob := err.Error() + out
		if !strings.Contains(blob, "metric") {
			t.Fatalf("want metric error, got %q", blob)
		}
	})
}

func TestExportEncryptedStore(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)

	// Quote-bearing key: must never appear in any output or error text.
	keyValue := `export-key's-"quote"-value`
	keyPath := filepath.Join(t.TempDir(), "db.key")
	if err := os.WriteFile(keyPath, []byte(keyValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed plaintext, then convert offline to encrypted.
	if out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportSeedPath}, ""); err != nil {
		t.Fatalf("plaintext seed: %v\n%s", err, out)
	}
	if out, err := runCLI([]string{"store", "encrypt", "--new-db-encryption-key-file", keyPath}, ""); err != nil {
		t.Fatalf("encrypt: %v\n%s", err, out)
	}

	t.Setenv("COSTROID_DB_ENCRYPTION_KEY_FILE", keyPath)
	out, err := runCLI([]string{"export", "costs-daily"}, "")
	if err != nil {
		t.Fatalf("encrypted export via env: %v\n%s", err, out)
	}
	if !strings.Contains(out, "100.123456789012345678") {
		t.Fatalf("encrypted export missing cost: %q", out)
	}
	if strings.Contains(out, keyValue) || strings.Contains(out, "quote") {
		t.Fatal("key material leaked into export output")
	}

	// Flag override with correct key also works.
	out, err = runCLI([]string{"export", "costs-daily", "--db-encryption-key-file", keyPath}, "")
	if err != nil {
		t.Fatalf("flag key export: %v\n%s", err, out)
	}
	if !strings.Contains(out, "100.123456789012345678") {
		t.Fatalf("flag export missing cost: %q", out)
	}

	// Wrong key is classified; key value never appears.
	wrongPath := filepath.Join(t.TempDir(), "wrong.key")
	wrongKey := `wrong-key's-"quote"`
	if err := os.WriteFile(wrongPath, []byte(wrongKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COSTROID_DB_ENCRYPTION_KEY_FILE", "")
	out, err = runCLI([]string{"export", "costs-daily", "--db-encryption-key-file", wrongPath}, "")
	if err == nil {
		t.Fatal("wrong key should fail")
	}
	blob := err.Error() + out
	if !strings.Contains(blob, "provided key is wrong") {
		t.Fatalf("want classified wrong-key message, got %q", blob)
	}
	if strings.Contains(blob, wrongKey) || strings.Contains(blob, keyValue) {
		t.Fatalf("key material leaked into error: %q", blob)
	}
}

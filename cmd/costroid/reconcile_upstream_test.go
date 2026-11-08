package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/gateway"
)

// writeGatewayLedger writes the rows usage.json matches as a gateway run
// writes them, each naming the run's key by its id.
func writeGatewayLedger(t *testing.T) string {
	t.Helper()
	path := writeLedger(t, 100000)
	rows, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runKey := gateway.KeyID(gateway.NewKey())
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(rows), "apikey_01ExampleCi", runKey)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReconcileAsksForTheKeyAGatewaySendsWith(t *testing.T) {
	rows := writeGatewayLedger(t)
	doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
	code, _, stderr := runReconcile(t, doer, environment, "--rows", rows, "--from", "2026-10-14", "--to", "2026-10-14")
	if code != exitFailure || !strings.Contains(stderr, "4 anthropic rows were drawn on a gateway's run keys") || !strings.Contains(stderr, "--upstream-credential anthropic=ID") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if doer.requested != 0 {
		t.Errorf("%d requests went out before the credential was named", doer.requested)
	}
	code, stdout, stderr := runReconcile(t, doer, environment, "--rows", rows, "--from", "2026-10-14", "--to", "2026-10-14", "--upstream-credential", "anthropic=apikey_01ExampleCi")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "anthropic  apikey_01ExampleCi  2026-10-14  input_uncached") || !strings.Contains(stdout, "the provider key the gateway holds") {
		t.Errorf("the table does not compare the run under the gateway's key:\n%s", stdout)
	}
}

func TestReconcileRefusesAnUpstreamCredentialItCannotRead(t *testing.T) {
	for _, values := range [][]string{
		{"apikey_01ExampleCi"},
		{"anthropic="},
		{"google=key_Example"},
		{"anthropic=apikey_01ExampleCi", "anthropic=apikey_02ExampleCi"},
	} {
		args := []string{"--rows", writeGatewayLedger(t), "--from", "2026-10-14", "--to", "2026-10-14"}
		for _, value := range values {
			args = append(args, "--upstream-credential", value)
		}
		doer := &replayDoer{t: t, usage: "usage.json", cost: "cost-at-list.json"}
		code, _, stderr := runReconcile(t, doer, environment, args...)
		if code != exitFailure || !strings.Contains(stderr, "upstream-credential") || doer.requested != 0 {
			t.Errorf("%v: exit %d, %d requests, stderr %q", values, code, doer.requested, stderr)
		}
	}
}

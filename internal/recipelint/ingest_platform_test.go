package recipelint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPlatformToken = "costroid_it_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"

// stubExport stands for costroid export: it keeps its arguments, says whether
// the token reached its environment, prints a warning and exits as told.
func (s *shell) stubExport() []string {
	s.t.Helper()
	s.stub("costroid", `printf '%s\n' "$@" > "`+s.root+`/export"
[ "${COSTROID_PLATFORM_TOKEN:-}" = "`+testPlatformToken+`" ] && touch "`+s.root+`/token"
echo "costroid: warning: Costroid Platform refused batch 1-2-3: 402 PLAN_WRITES_REFUSED. Nothing in the rows is wrong" >&2
exit ${STUB_EXPORT_EXIT:-0}`)
	rows := filepath.Join(s.root, "rows.jsonl")
	if err := os.WriteFile(rows, []byte("{}\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return []string{
		"COSTROID_ROWS=" + rows,
		"COSTROID_PLATFORM_URL=https://app.costroid.example",
		"COSTROID_PLATFORM_INSTALLATION_ID=github-owner:7204411",
		"COSTROID_PLATFORM_TOKEN=" + testPlatformToken,
	}
}

func TestIngestSendsToThePlatformThroughExport(t *testing.T) {
	s := newShell(t)
	env := s.stubExport()
	code, output := s.run("ingest.sh", env...)
	if code != 0 || !strings.Contains(output, "::warning::costroid: Costroid Platform refused batch 1-2-3: 402 PLAN_WRITES_REFUSED.") {
		t.Fatalf("exit %d: %s", code, output)
	}
	argv := read(t, filepath.Join(s.root, "export"))
	want := "export\n--rows\n" + filepath.Join(s.root, "rows.jsonl") + "\n--platform-url\nhttps://app.costroid.example\n--installation-id\ngithub-owner:7204411\n"
	if argv != want {
		t.Errorf("argv:\n%s", argv)
	}
	if _, err := os.Stat(filepath.Join(s.root, "token")); err != nil {
		t.Error("the token did not reach costroid export's environment")
	}
	if strings.Contains(output, testPlatformToken) || strings.Contains(output, "did not take") {
		t.Errorf("output: %s", output)
	}
}

func TestIngestWarnsWhenThePlatformTakesNothing(t *testing.T) {
	s := newShell(t)
	env := s.stubExport()
	code, output := s.run("ingest.sh", append(env, "STUB_EXPORT_EXIT=1")...)
	if code != 0 || !strings.Contains(output, "::warning::costroid: Costroid Platform did not take the rows") {
		t.Fatalf("exit %d: %s", code, output)
	}
}

func TestIngestToThePlatformNeedsTheTokenAndTheInstallation(t *testing.T) {
	for variable, line := range map[string]string{
		"COSTROID_PLATFORM_TOKEN":           "::warning::costroid: platform-token is empty, so no rows were sent to Costroid Platform",
		"COSTROID_PLATFORM_INSTALLATION_ID": "::warning::costroid: installation-id is empty, so no rows were sent to Costroid Platform",
	} {
		t.Run(variable, func(t *testing.T) {
			s := newShell(t)
			env := append(s.stubExport(), variable+"=")
			code, output := s.run("ingest.sh", env...)
			if code != 0 || !strings.Contains(output, line) {
				t.Fatalf("exit %d: %s", code, output)
			}
			if _, err := os.Stat(filepath.Join(s.root, "export")); !os.IsNotExist(err) {
				t.Fatal("costroid export ran")
			}
		})
	}
}

// A warning for the ledger never skips the platform.
func TestIngestSendsToBothWhenTheLedgerRefuses(t *testing.T) {
	s := newShell(t)
	env := append(s.stubOIDC(), s.stubExport()...)
	code, output := s.run("ingest.sh", append(env, "STUB_POST_EXIT=22")...)
	if code != 0 || !strings.Contains(output, "::warning::costroid: the ledger refused the rows") {
		t.Fatalf("exit %d: %s", code, output)
	}
	if _, err := os.Stat(filepath.Join(s.root, "export")); err != nil {
		t.Fatalf("costroid export did not run after the ledger's warning: %s", output)
	}
}

func TestIngestWithNeitherDestinationSendsNothing(t *testing.T) {
	s := newShell(t)
	env := append(s.stubExport(), "COSTROID_PLATFORM_URL=")
	code, output := s.run("ingest.sh", env...)
	if code != 0 || !strings.Contains(output, "::warning::costroid: ingest-url and platform-url are empty, so no rows were sent") {
		t.Fatalf("exit %d: %s", code, output)
	}
}

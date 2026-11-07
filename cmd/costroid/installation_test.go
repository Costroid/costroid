package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestInstallationIDOneForm feeds export and report the same values: each
// takes a GitHub App installation as github:<id> or as the bare number, and
// each writes github:<id>, export in its header and report in the account link.
func TestInstallationIDOneForm(t *testing.T) {
	const number = "58120394"
	for _, given := range []string{"github:" + number, number} {
		t.Run(given, func(t *testing.T) {
			stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
			code, _, stderr := stub.export(t, stub.githubEnv(), "--installation-id", given)
			if code != 0 || len(stub.exports) != 1 {
				t.Fatalf("export exited %d with %d batches: %s", code, len(stub.exports), stderr)
			}
			if sent := stub.exports[0].Header.Get("Costroid-Installation-Id"); sent != "github:"+number {
				t.Errorf("export sent installation %q, want github:%s", sent, number)
			}
			code, body, stderr := previewWithInstallation(t, given)
			if code != 0 || !strings.Contains(body, "(https://app.costroid.com/?installation=github%3A"+number+")") {
				t.Errorf("report exited %d and its account link does not name github:%s: %s\n%s", code, number, stderr, body)
			}
		})
	}
}

// TestInstallationIDOtherForms holds what only one command takes: export also
// sends an owner or a GitLab namespace as given, and report's link names a
// GitHub App installation only, so it refuses any other form.
func TestInstallationIDOtherForms(t *testing.T) {
	for _, given := range []string{"github-owner:7204411", "gitlab-namespace:4410287"} {
		stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
		code, _, stderr := stub.export(t, stub.githubEnv(), "--installation-id", given)
		if code != 0 || len(stub.exports) != 1 || stub.exports[0].Header.Get("Costroid-Installation-Id") != given {
			t.Errorf("export of %s exited %d: %s", given, code, stderr)
		}
	}
	for _, given := range []string{"github:0", "github:12x", "gitlab:4242", "-1"} {
		stub := newPlatformStub(t, http.StatusAccepted, `{"status":"accepted"}`)
		code, _, stderr := stub.export(t, stub.githubEnv(), "--installation-id", given)
		if code != exitFailure || len(stub.exports) != 0 || !strings.Contains(stderr, "--installation-id must be github:<id>, a bare GitHub App installation number") {
			t.Errorf("export of %q exited %d: %s", given, code, stderr)
		}
	}
	for _, given := range []string{"github-owner:7204411", "gitlab-namespace:4410287", "github:0", "github:12x", "gitlab:4242", "-1"} {
		code, body, stderr := previewWithInstallation(t, given)
		if code != 0 || strings.Contains(body, "installation=") || !strings.Contains(stderr, "so the comment links no account") {
			t.Errorf("report of %q exited %d: %s%s", given, code, stderr, body)
		}
	}
}

// previewWithInstallation prints the comment report would post for the demo's
// rows, outside CI, and returns report's exit code, stdout and stderr.
func previewWithInstallation(t *testing.T, installation string) (int, string, string) {
	t.Helper()
	outsideCI(t)
	t.Setenv(installationVariable, "")
	rows := absolute(t, demoRowsFile)
	t.Chdir(t.TempDir())
	return execute(t, "report", "--preview", "--rows", rows, "--installation-id", installation)
}

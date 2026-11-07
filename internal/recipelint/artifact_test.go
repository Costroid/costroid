package recipelint

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const downloadStep = `      - uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2
        with:
          name: costroid-rows
          path: PATH
`

// downloadWorkflow is a job that fetches an artifact into path and nothing
// else, so the only finding it can draw is the download's.
func downloadWorkflow(path string) string {
	return "on: pull_request\npermissions: {}\njobs:\n  report:\n    runs-on: ubuntu-24.04\n    permissions:\n      checks: write\n    steps:\n" +
		strings.Replace(downloadStep, "PATH", path, 1)
}

func downloadAction(path string) string {
	return "name: x\nruns:\n  using: composite\n  steps:\n" + strings.ReplaceAll(strings.Replace(downloadStep, "PATH", path, 1), "      ", "    ")
}

func TestDownloadNeverIntoRunnerTemp(t *testing.T) {
	refused := []string{
		"${{ runner.temp }}",
		"${{runner.temp}}",
		"${{ runner.temp }}/",
		"${{ runner.temp }}/.",
		"${{ runner.temp }}/costroid-rows-in/..",
		"${{ runner.temp }}/../work",
		"$RUNNER_TEMP",
		"${RUNNER_TEMP}/",
		"'${{ runner.temp }}'",
	}
	accepted := []string{
		"${{ runner.temp }}/costroid-rows-in",
		"${{ runner.temp }}/costroid-follow-up/",
		"$RUNNER_TEMP/costroid-rows-in",
		"${{ github.workspace }}/rows",
	}
	for _, shape := range []struct {
		name  string
		build func(string) string
	}{{"workflow", downloadWorkflow}, {"action", downloadAction}} {
		for _, path := range refused {
			t.Run(shape.name+"/refuses "+path, func(t *testing.T) {
				report := LintBytes("x.yml", []byte(shape.build(path)))
				if !slices.Contains(rules(report.Findings), RuleArtifactPath) {
					t.Fatalf("want %s, got %v", RuleArtifactPath, report.Findings)
				}
			})
		}
		for _, path := range accepted {
			t.Run(shape.name+"/accepts "+path, func(t *testing.T) {
				if report := LintBytes("x.yml", []byte(shape.build(path))); len(report.Findings) != 0 {
					t.Fatalf("findings: %v", report.Findings)
				}
			})
		}
	}
}

// The rule reads the shipped files' own shape: the action's fetch and the
// follow-up recipe's download, each pointed back at runner.temp.
func TestDownloadIntoRunnerTempBitesTheShippedFiles(t *testing.T) {
	for file, change := range map[string]edit{
		filepath.Join(actionDir, "action.yml"):            {"", "path: ${{ runner.temp }}/costroid-rows-in\n", "path: ${{ runner.temp }}\n", RuleArtifactPath},
		filepath.Join(recipesDir, "github-follow-up.yml"): {"", "path: ${{ runner.temp }}/costroid-follow-up\n", "path: ${{ runner.temp }}\n", RuleArtifactPath},
	} {
		t.Run(filepath.Base(file), func(t *testing.T) {
			expectRule(t, read(t, file), change)
		})
	}
}

func TestAgentCheckoutDropsCredentials(t *testing.T) {
	const keep = "        with:\n          persist-credentials: false\n"
	for _, change := range []edit{
		{"no with", keep, "", RuleAgentCheckoutCredentials},
		{"credentials kept", "persist-credentials: false", "persist-credentials: true", RuleAgentCheckoutCredentials},
		{"another input alone", "persist-credentials: false", "fetch-depth: 0", RuleAgentCheckoutCredentials},
	} {
		t.Run(change.name, func(t *testing.T) {
			expectRule(t, goodWorkflow, change)
		})
	}
	for _, name := range []string{"github-claude-code.yml", "github-claude-code-action.yml", "github-codex.yml", "github-claude-code-litellm.yml"} {
		t.Run(name, func(t *testing.T) {
			expectRule(t, read(t, filepath.Join(recipesDir, name)), edit{"", "          persist-credentials: false\n", "", RuleAgentCheckoutCredentials})
		})
	}
	reportCheckout := strings.Replace(goodWorkflow, "      checks: write\n    steps:\n", "      checks: write\n    steps:\n      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n", 1)
	if reportCheckout == goodWorkflow {
		t.Fatal("goodWorkflow's report job no longer has the steps this test adds a checkout to")
	}
	if got := rules(LintBytes("x.yml", []byte(reportCheckout)).Findings); slices.Contains(got, RuleAgentCheckoutCredentials) {
		t.Fatalf("a checkout in a job with no agent was refused: %v", got)
	}
}

// The agent's answer rides in the rows artifact only when the workflow sets
// post-review to true, and only as a plain file. Any other setting refuses an
// artifact that holds it, as it refuses any third entry.
func TestArtifactAdmitsTheReviewOnlyWhenOptedIn(t *testing.T) {
	files := map[string]string{"costroid-rows.jsonl": "{}\n", "costroid-rows.run.json": "{}", "costroid-review.md": "An answer."}
	for _, stage := range []string{"report", "ingest"} {
		for setting, admitted := range map[string]bool{"true": true, "false": false, "": false, "TRUE": false, "unset": false} {
			t.Run(stage+"/"+setting, func(t *testing.T) {
				r := newRelease(t)
				dir := rowsIn(t, r.root, files)
				outputs := filepath.Join(r.root, "github-output")
				env := []string{"COSTROID_STAGE=" + stage, "COSTROID_VERSION=" + testVersion, "COSTROID_SHA256=" + sumOf(releaseBinary), "GITHUB_OUTPUT=" + outputs}
				if setting != "unset" {
					env = append(env, "COSTROID_POST_REVIEW="+setting)
				}
				code, output := r.install(env...)
				refused := strings.Contains(output, "costroid: the costroid-rows artifact holds 1 entries besides the rows and the run object")
				switch {
				case admitted:
					if code != 0 || refused || !r.installed() || read(t, filepath.Join(dir, "costroid-review.md")) != "An answer." {
						t.Fatalf("exit %d, refused %v: %s", code, refused, output)
					}
				case stage == "ingest":
					if code != 1 || !refused || r.installed() {
						t.Fatalf("exit %d, refused %v, installed %v: %s", code, refused, r.installed(), output)
					}
				default:
					if code != 0 || !refused || read(t, outputs) != "refused=costroid-review.md\nrefused_count=1\n" {
						t.Fatalf("exit %d, refused %v: %s", code, refused, output)
					}
					if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
						t.Fatalf("the refused artifact is still there: %v, %v", left, err)
					}
				}
			})
		}
	}
	target := filepath.Join(t.TempDir(), "secret")
	write(t, target, "kept")
	for name, place := range map[string]func(dir string) error{
		"a folder": func(dir string) error { return os.MkdirAll(filepath.Join(dir, "costroid-review.md", "x"), 0o755) },
		"a link":   func(dir string) error { return os.Symlink(target, filepath.Join(dir, "costroid-review.md")) },
	} {
		t.Run("opted in/"+name, func(t *testing.T) {
			r := newRelease(t)
			dir := rowsIn(t, r.root, map[string]string{"costroid-rows.jsonl": "{}\n"})
			if err := place(dir); err != nil {
				t.Fatal(err)
			}
			code, output := r.install("COSTROID_STAGE=ingest", "COSTROID_POST_REVIEW=true", "COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sumOf(releaseBinary))
			if code != 1 || r.installed() || !strings.Contains(output, "costroid-review.md") {
				t.Fatalf("exit %d: %s", code, output)
			}
		})
	}
}

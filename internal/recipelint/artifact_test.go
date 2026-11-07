package recipelint

import (
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

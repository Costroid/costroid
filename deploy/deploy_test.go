// Package deploy holds the tests of the packaging files beside it.
package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/recipelint"
)

const (
	dockerfile = "../Dockerfile"
	composed   = "compose.yaml"
	chart      = "helm/costroid"
	workflows  = "../.github/workflows"
	install    = "../docs/install.md"

	egressVariable = "COSTROID_EGRESS_ALLOW"
)

var pinned = regexp.MustCompile(`@sha256:[0-9a-f]{64}\b`)

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEveryBaseImageIsPinnedByDigest(t *testing.T) {
	for _, line := range strings.Split(string(read(t, dockerfile)), "\n") {
		if strings.HasPrefix(line, "FROM ") && !pinned.MatchString(line) {
			t.Errorf("unpinned: %s", line)
		}
	}
}

func TestTheImageRunsAsANonRootUser(t *testing.T) {
	if !regexp.MustCompile(`(?m)^USER 65532:65532$`).Match(read(t, dockerfile)) {
		t.Error("no numeric non-root USER")
	}
}

func TestComposeAndTheChartRunOnePostgres(t *testing.T) {
	var file struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(read(t, composed), &file); err != nil {
		t.Fatal(err)
	}
	digest := pinned.FindString(file.Services["postgres"].Image)
	if digest == "" {
		t.Fatalf("postgres is unpinned: %s", file.Services["postgres"].Image)
	}
	if !bytes.Contains(read(t, filepath.Join(chart, "values.yaml")), []byte(strings.TrimPrefix(digest, "@"))) {
		t.Errorf("the chart's Postgres is not %s", digest)
	}
}

func TestComposeAndTheChartCreateTheSameRoles(t *testing.T) {
	if !bytes.Equal(read(t, "postgres-init/10-roles.sh"), read(t, filepath.Join(chart, "files/10-roles.sh"))) {
		t.Error("the two copies of 10-roles.sh differ")
	}
}

func TestComposeStatesTheEgressNote(t *testing.T) {
	if !bytes.Contains(read(t, composed), []byte("Egress.")) {
		t.Error("compose.yaml has no egress note")
	}
}

func TestTheEgressListIsPresentAndEmptyInComposeAndTheChart(t *testing.T) {
	var file struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(read(t, composed), &file); err != nil {
		t.Fatal(err)
	}
	if got, ok := file.Services["costroid"].Environment[egressVariable]; !ok || got != "${"+egressVariable+":-}" {
		t.Errorf("compose reads %q, present %t", got, ok)
	}
	var values struct {
		EgressAllow *[]string `yaml:"egressAllow"`
	}
	if err := yaml.Unmarshal(read(t, filepath.Join(chart, "values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	if values.EgressAllow == nil || len(*values.EgressAllow) != 0 {
		t.Errorf("the chart's egressAllow reads %v", values.EgressAllow)
	}
	deployment := read(t, filepath.Join(chart, "templates/deployment.yaml"))
	if !regexp.MustCompile(`- name: ` + egressVariable + `\n\s+value: \{\{ join "," \.Values\.egressAllow \| quote \}\}`).Match(deployment) {
		t.Error("the chart does not pass egressAllow to the service")
	}
}

func TestEachFileSaysWhatOIDCIngestNeedsOnTheList(t *testing.T) {
	for _, path := range []string{composed, filepath.Join(chart, "values.yaml"), install} {
		text := read(t, path)
		for _, want := range []string{"token.actions.githubusercontent.com", "gitlab.com"} {
			if !bytes.Contains(text, []byte(want)) {
				t.Errorf("%s does not name %s", path, want)
			}
		}
	}
}

func TestTheDocsCallTheFirewallTheStrongerControl(t *testing.T) {
	for _, path := range []string{composed, install} {
		text := strings.Join(strings.Fields(strings.ReplaceAll(string(read(t, path)), "#", "")), " ")
		if !strings.Contains(text, "does not hold against code running in the process") || !strings.Contains(text, "the stronger control") {
			t.Errorf("%s dropped the sentence on the stronger control", path)
		}
		if strings.Contains(text, "does not enforce") || strings.Contains(text, "no egress report") {
			t.Errorf("%s still says enforcement does not exist", path)
		}
	}
}

// TestWorkflowsPassTheRecipeLinter puts the two workflows under the pin rules.
// A release signs with its OIDC identity, so id-token-scope, a rule about agents,
// does not apply to it. No agent runs in either file.
func TestWorkflowsPassTheRecipeLinter(t *testing.T) {
	report, err := recipelint.Lint([]string{workflows})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 2 {
		t.Fatalf("linted %d files", report.Files)
	}
	for _, finding := range report.Findings {
		if finding.Rule == "id-token-scope" && filepath.Base(finding.Path) == "release.yml" {
			continue
		}
		t.Errorf("%s", finding)
	}
}

func TestEveryActionIsPinnedBySHAWithItsRelease(t *testing.T) {
	uses := regexp.MustCompile(`uses:\s*(\S+)(.*)`)
	full := regexp.MustCompile(`@[0-9a-f]{40}$`)
	release := regexp.MustCompile(`^\s*# v[0-9]`)
	for _, name := range []string{"ci.yml", "release.yml"} {
		found := 0
		for _, match := range uses.FindAllSubmatch(read(t, filepath.Join(workflows, name)), -1) {
			found++
			if !full.Match(match[1]) || !release.Match(match[2]) {
				t.Errorf("%s: %s%s", name, match[1], match[2])
			}
		}
		if found == 0 {
			t.Errorf("%s: no uses lines, so this test proved nothing", name)
		}
	}
}

func TestReleaseRefusesTheTagAnOldInstallFetches(t *testing.T) {
	workflow := read(t, filepath.Join(workflows, "release.yml"))
	if !bytes.Contains(workflow, []byte("$TAG == v0.7.0")) {
		t.Error("release.yml does not refuse v0.7.0")
	}
	for _, job := range []string{"binaries", "image"} {
		if !regexp.MustCompile(`(?m)^  ` + job + `:\n    needs: guard$`).Match(workflow) {
			t.Errorf("%s does not wait for the guard", job)
		}
	}
}

func TestReleaseSignsAndWritesAnSPDXSBOM(t *testing.T) {
	workflow := string(read(t, filepath.Join(workflows, "release.yml")))
	for _, want := range []string{"cosign sign-blob --yes", "cosign sign --yes", "cosign attest --yes --type spdxjson"} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release.yml has no %q", want)
		}
	}
	if sboms := strings.Count(workflow, "format: spdx-json"); sboms != 2 {
		t.Errorf("%d SPDX SBOMs, want one for the binaries and one for the image", sboms)
	}
}

func TestTheChartAndTheFirstReleaseShareAVersion(t *testing.T) {
	if !bytes.Contains(read(t, filepath.Join(chart, "Chart.yaml")), []byte("appVersion: 1.0.0-alpha.1")) {
		t.Error("the chart does not name 1.0.0-alpha.1")
	}
}

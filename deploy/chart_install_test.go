package deploy

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const chartInstall = "../scripts/chart-install.sh"

// kindNodeImages holds, for each kind release the chart-install job may pin,
// the node image that release's notes publish for kind's default Kubernetes,
// by digest. The notes say a node image is guaranteed for a release only by
// its digest. v0.33.0:
// https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0
var kindNodeImages = map[string]string{
	"v0.33.0": "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5",
}

// TestChartInstallRunsTheNodeImageItsKindReleasePublishes holds the script's
// node image and the job's kind release together, so neither moves alone.
func TestChartInstallRunsTheNodeImageItsKindReleasePublishes(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Env map[string]string `yaml:"env"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(read(t, filepath.Join(workflows, "ci.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	version := workflow.Jobs["chart-install"].Env["KIND_VERSION"]
	if version == "" {
		t.Fatal("the chart-install job pins no KIND_VERSION")
	}
	want, ok := kindNodeImages[version]
	if !ok {
		t.Fatalf("the job pins kind %s, and kindNodeImages has no image for it: add the one its release notes publish", version)
	}
	script := string(read(t, chartInstall))
	match := regexp.MustCompile(`(?m)^node_image=(\S+)$`).FindStringSubmatch(script)
	if match == nil {
		t.Fatal("scripts/chart-install.sh sets no node_image")
	}
	if match[1] != want {
		t.Errorf("scripts/chart-install.sh runs %s; kind %s publishes %s", match[1], version, want)
	}
	if note := "# kind " + version + " publishes this image for its nodes"; !strings.Contains(script, note) {
		t.Errorf("scripts/chart-install.sh does not say %q", note)
	}
}

// TestTheChartsStartupProbeOutlastsTheServicesWait holds the chart's startup
// probe to more time than the service waits for Postgres at start, as
// docs/serve.md states both, and docs/ledger.md to stating the pair. A probe that gave up first would restart a
// service still waiting, which the chart install counts as a failure.
func TestTheChartsStartupProbeOutlastsTheServicesWait(t *testing.T) {
	deployment := string(read(t, filepath.Join(chart, "templates", "deployment.yaml")))
	probe := regexp.MustCompile(`(?m)^ +startupProbe:\n(?: +[a-zA-Z].*\n)*? +periodSeconds: (\d+)\n +failureThreshold: (\d+)$`).FindStringSubmatch(deployment)
	if probe == nil {
		t.Fatal("the chart's deployment states no startupProbe with periodSeconds and failureThreshold")
	}
	period, _ := strconv.Atoi(probe[1])
	failures, _ := strconv.Atoi(probe[2])
	page := string(read(t, "../docs/serve.md"))
	bound := regexp.MustCompile(`It tries every \d+ seconds for up to (\d+) seconds`).FindStringSubmatch(page)
	allows := regexp.MustCompile(`The chart's startup probe allows (\d+) seconds`).FindStringSubmatch(page)
	if bound == nil || allows == nil {
		t.Fatal("docs/serve.md does not state the service's wait and the probe's")
	}
	waits, _ := strconv.Atoi(bound[1])
	if allows[1] != strconv.Itoa(period*failures) || waits >= period*failures {
		t.Errorf("the probe allows %d × %d seconds, docs/serve.md says %s, and the service waits %d", period, failures, allows[1], waits)
	}
	if pair := fmt.Sprintf("It waits up to %d seconds, and the chart's startup probe allows %d,", waits, period*failures); !strings.Contains(string(read(t, ledger)), pair) {
		t.Errorf("docs/ledger.md does not say %q", pair)
	}
}

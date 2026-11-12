package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// releaseJob is one job of release.yml, with what it waits for and what its
// token may do.
type releaseJob struct {
	name        string
	needs       []string
	permissions map[string]string
	steps       []releaseStep
}

// releaseJobs reads release.yml's jobs in the order the file lists them.
func releaseJobs(t *testing.T) []releaseJob {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal(read(t, filepath.Join(workflows, "release.yml")), &root); err != nil {
		t.Fatal(err)
	}
	var jobs *yaml.Node
	for i := 0; i+1 < len(root.Content[0].Content); i += 2 {
		if root.Content[0].Content[i].Value == "jobs" {
			jobs = root.Content[0].Content[i+1]
		}
	}
	if jobs == nil {
		t.Fatal("release.yml has no jobs")
	}
	var found []releaseJob
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		var body struct {
			Needs       yaml.Node         `yaml:"needs"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []releaseStep     `yaml:"steps"`
		}
		if err := jobs.Content[i+1].Decode(&body); err != nil {
			t.Fatal(err)
		}
		job := releaseJob{name: jobs.Content[i].Value, permissions: body.Permissions, steps: body.Steps}
		switch body.Needs.Kind {
		case yaml.ScalarNode:
			job.needs = []string{body.Needs.Value}
		case yaml.SequenceNode:
			if err := body.Needs.Decode(&job.needs); err != nil {
				t.Fatal(err)
			}
		}
		found = append(found, job)
	}
	return found
}

// waitsFor reports whether job runs only after gate, through any chain of
// needs.
func waitsFor(jobs []releaseJob, job, gate string) bool {
	byName := map[string]releaseJob{}
	for _, j := range jobs {
		byName[j.name] = j
	}
	seen := map[string]bool{}
	queue := slices.Clone(byName[job].needs)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next == gate {
			return true
		}
		if !seen[next] {
			seen[next] = true
			queue = append(queue, byName[next].needs...)
		}
	}
	return false
}

func stepRunning(t *testing.T, job releaseJob, command string) releaseStep {
	t.Helper()
	for _, step := range job.steps {
		if strings.Contains(step.Run, command) {
			return step
		}
	}
	t.Fatalf("the %s job runs no %q", job.name, command)
	return releaseStep{}
}

// stub writes an executable script named name into dir.
func stub(t *testing.T, dir, name, body string) {
	t.Helper()
	write(t, filepath.Join(dir, name), []byte("#!/usr/bin/env bash\n"+body))
	if err := os.Chmod(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runStepFor runs one step's script and returns its exit code and output.
func runStepFor(t *testing.T, step releaseStep, dir string, env []string) (int, string) {
	t.Helper()
	command := exec.Command("bash", "-e", "-o", "pipefail", "-c", step.Run)
	command.Dir = dir
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil && command.ProcessState == nil {
		t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), string(output)
}

// TestReleaseWaitsForCI holds release.yml's first job to a successful ci run
// on a push of the tagged commit, read with a token that may read Actions
// and nothing else, and every other job to wait for it. It runs the job's
// step against a stand-in gh that answers as the API would.
func TestReleaseWaitsForCI(t *testing.T) {
	jobs := releaseJobs(t)
	gate := jobs[0]
	if len(gate.permissions) != 1 || gate.permissions["actions"] != "read" {
		t.Errorf("the first job, %s, holds %v, want actions: read alone", gate.name, gate.permissions)
	}
	if len(gate.needs) != 0 {
		t.Errorf("the first job waits for %v", gate.needs)
	}
	for _, job := range jobs[1:] {
		if !waitsFor(jobs, job.name, gate.name) {
			t.Errorf("the %s job does not wait for %s", job.name, gate.name)
		}
	}

	step := stepRunning(t, gate, "gh api")
	const tagged = "61a5b2e3836a940838e127311ed15f3087f3d6d5"
	for name, check := range map[string]struct {
		runs string
		code int
	}{
		"the tagged commit's green run":    {tagged + " completed success\n", 0},
		"a green run of another commit":    {"47ee64dcf7d74266b494f05e706f0942090d61c0 completed success\n", 1},
		"a run of the commit that failed":  {tagged + " completed failure\n", 1},
		"a run of the commit still going":  {tagged + " in_progress \n", 1},
		"no run at all":                    {"", 1},
		"a green run beside another's run": {"47ee64dcf7d74266b494f05e706f0942090d61c0 completed success\n" + tagged + " completed success\n", 0},
	} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			tools := filepath.Join(work, "tools")
			if err := os.Mkdir(tools, 0o755); err != nil {
				t.Fatal(err)
			}
			stub(t, tools, "gh", `printf '%s\n' "$@" >"$STUB_ARGS"
printf '%s' "$STUB_RUNS"
`)
			args := filepath.Join(work, "args")
			code, output := runStepFor(t, step, work, append(os.Environ(),
				"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_SHA="+tagged, "GITHUB_REPOSITORY=Costroid/costroid", "GH_TOKEN=unused",
				"STUB_ARGS="+args, "STUB_RUNS="+check.runs,
			))
			if code != check.code {
				t.Fatalf("exit %d, want %d: %s", code, check.code, output)
			}
			if code != 0 && !strings.Contains(output, "::error::"+tagged+" has no successful ci run") {
				t.Errorf("the refusal does not say why in one line: %s", output)
			}
			asked := strings.Split(strings.TrimSpace(string(read(t, args))), "\n")
			for _, want := range []string{"repos/Costroid/costroid/actions/workflows/ci.yml/runs", "head_sha=" + tagged, "event=push", "status=success"} {
				if !slices.Contains(asked, want) {
					t.Errorf("gh was not asked for %s: %v", want, asked)
				}
			}
		})
	}
}

// TestImageBuildPassesTheCommit holds the image's build to the commit, as the
// release binaries are built: the Dockerfile links the COMMIT argument into
// main.commit, and the release workflow and make image pass it.
func TestImageBuildPassesTheCommit(t *testing.T) {
	dockerfileText := string(read(t, dockerfile))
	build, _, _ := strings.Cut(dockerfileText, "\nFROM gcr.io/")
	if !regexp.MustCompile(`(?m)^ARG COMMIT=?$`).MatchString(build) {
		t.Error("the build stage declares no COMMIT argument")
	}
	goBuild := regexp.MustCompile(`(?s)RUN [^\n]*go build .*?-o /out/costroid`).FindString(strings.ReplaceAll(build, "\\\n", " "))
	if !strings.Contains(goBuild, "-X main.commit=$COMMIT") {
		t.Errorf("the image's go build links no commit: %q", goBuild)
	}

	var image releaseJob
	for _, job := range releaseJobs(t) {
		if job.name == "image" {
			image = job
		}
	}
	var buildArgs string
	for _, step := range image.steps {
		if strings.HasPrefix(step.Uses, "docker/build-push-action@") {
			buildArgs = step.With["build-args"]
		}
	}
	if !slices.Contains(strings.Split(buildArgs, "\n"), "COMMIT=${{ github.sha }}") {
		t.Errorf("the release's image build passes %q, without COMMIT=${{ github.sha }}", buildArgs)
	}
	if !strings.Contains(string(read(t, "../Makefile")), "--build-arg COMMIT=$(COMMIT)") {
		t.Error("make image passes no COMMIT")
	}
}

// TestTheImageIsTaggedOnlyAfterItIsSigned holds the image job to a push by
// digest, and the one command that names the image by its version to a job
// that runs after the signing job.
func TestTheImageIsTaggedOnlyAfterItIsSigned(t *testing.T) {
	jobs := releaseJobs(t)
	tagging := 0
	for _, job := range jobs {
		for _, step := range job.steps {
			if strings.HasPrefix(step.Uses, "docker/build-push-action@") {
				if step.With["push"] == "true" || strings.Contains(step.With["tags"], ":") {
					t.Errorf("the %s job pushes a tag: push %q, tags %q", job.name, step.With["push"], step.With["tags"])
				}
				if !strings.Contains(step.With["outputs"], "push-by-digest=true") {
					t.Errorf("the %s job does not push by digest: %q", job.name, step.With["outputs"])
				}
			}
			if strings.Contains(step.Run, "imagetools create") {
				tagging++
				if !waitsFor(jobs, job.name, "sign") {
					t.Errorf("the %s job tags the image without waiting for sign", job.name)
				}
			}
		}
	}
	if tagging != 1 {
		t.Errorf("%d jobs tag the image, want one", tagging)
	}
	for _, job := range jobs {
		if job.name == "publish" && !waitsFor(jobs, "publish", "tag") {
			t.Error("the release is published before the image has its tag")
		}
	}
}

// TestReleaseNotesOpenWithTheVersionsChanges runs the guard's changelog step
// and the publish step: the notes hold the version's section and no other,
// above the install text and the image, and a version with no section stops
// the release.
func TestReleaseNotesOpenWithTheVersionsChanges(t *testing.T) {
	var guard, publish releaseJob
	for _, job := range releaseJobs(t) {
		switch job.name {
		case "guard":
			guard = job
		case "publish":
			publish = job
		}
	}
	section := stepRunning(t, guard, "CHANGELOG.md")
	changelog := "# Changelog\n\nEach release is dated by its tag.\n\n" +
		"## 1.0.0-alpha.100 (2026-11-13)\n\n### Fixes\n\n- The newest fix.\n\n" +
		"## 1.0.0-alpha.10 (2026-11-05)\n\n### What you can now do\n\n- Read a run.\n\n### Fixes\n\n- A fix.\n\n" +
		"## 1.0.0-alpha.1 (2026-11-01)\n\n- The first release.\n"

	work := t.TempDir()
	write(t, filepath.Join(work, "CHANGELOG.md"), []byte(changelog))
	code, output := runStepFor(t, section, work, append(os.Environ(), "VERSION=1.0.0-alpha.10"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	changes := "### What you can now do\n\n- Read a run.\n\n### Fixes\n\n- A fix.\n"
	if got := string(read(t, filepath.Join(work, "changes.md"))); got != changes {
		t.Fatalf("the changes read\n%q\nwant\n%q", got, changes)
	}

	tools, dist := filepath.Join(work, "tools"), filepath.Join(work, "dist")
	for _, dir := range []string{tools, dist, filepath.Join(work, "changes")} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(work, "changes.md"), filepath.Join(work, "changes", "changes.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dist, "SHA256SUMS"), []byte("sums\n"))
	stub(t, tools, "gh", `cp notes.md "$STUB_NOTES"`+"\n")
	notes := filepath.Join(work, "published.md")
	digest := "sha256:" + strings.Repeat("0", 64)
	code, output = runStepFor(t, stepRunning(t, publish, "gh release create"), work, append(os.Environ(),
		"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TAG=v1.0.0-alpha.10", "GH_REPO=Costroid/costroid", "GH_TOKEN=unused", "DIGEST="+digest, "STUB_NOTES="+notes,
	))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	published := string(read(t, notes))
	install := strings.Index(published, "\nInstall: download `costroid-install.sh`")
	image := strings.Index(published, "\nImage: `ghcr.io/costroid/costroid@"+digest+"`")
	if !strings.HasPrefix(published, changes+"\n") || install < 0 || image < install {
		t.Errorf("the notes do not open with the changes, then the install text, then the image:\n%s", published)
	}

	for _, version := range []string{"1.0.0-alpha.11", "1.0.0-alpha"} {
		t.Run("no section for "+version, func(t *testing.T) {
			work := t.TempDir()
			write(t, filepath.Join(work, "CHANGELOG.md"), []byte(changelog))
			code, output := runStepFor(t, section, work, append(os.Environ(), "VERSION="+version))
			if code == 0 || !strings.Contains(output, "::error::CHANGELOG.md has no section for "+version+", so nothing is published") {
				t.Errorf("exit %d: %s", code, output)
			}
		})
	}
}

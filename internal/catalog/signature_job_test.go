package catalog

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	catalogdata "github.com/Costroid/costroid/catalog"
	"go.yaml.in/yaml/v3"
)

// repositoryRoot is the module's root, from this package's directory.
const repositoryRoot = "../.."

// workflowStep is one step of a GitHub Actions job.
type workflowStep struct {
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
	Run  string            `yaml:"run"`
}

// workflowJobs reads the jobs of one of the repository's workflows.
func workflowJobs(t *testing.T, name string) map[string][]workflowStep {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot, ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	jobs := map[string][]workflowStep{}
	for job, body := range workflow.Jobs {
		jobs[job] = body.Steps
	}
	return jobs
}

// cosignInstaller is the step that installs cosign, from the steps given.
func cosignInstaller(t *testing.T, workflow string, steps []workflowStep) workflowStep {
	t.Helper()
	for _, step := range steps {
		if strings.HasPrefix(step.Uses, "sigstore/cosign-installer@") {
			return step
		}
	}
	t.Fatalf("%s installs no cosign", workflow)
	return workflowStep{}
}

// signatureJobScript is the script of CI's catalog-signature job, and the
// job's cosign installer step.
func signatureJobScript(t *testing.T) (string, workflowStep) {
	t.Helper()
	steps, found := workflowJobs(t, "ci.yml")["catalog-signature"]
	if !found {
		t.Fatal("ci.yml has no catalog-signature job")
	}
	var scripts []string
	for _, step := range steps {
		if step.Run != "" {
			scripts = append(scripts, step.Run)
		}
	}
	if len(scripts) != 1 {
		t.Fatalf("the catalog-signature job runs %d scripts, want one", len(scripts))
	}
	return scripts[0], cosignInstaller(t, "ci.yml's catalog-signature job", steps)
}

// runSignatureJob runs the job's script in dir as GitHub's bash shell runs a
// step, with a cosign that records its arguments and exits with cosignExit
// in place of the real one. It returns the recorded arguments and the
// script's output.
func runSignatureJob(t *testing.T, script, dir string, cosignExit string) ([]string, string, error) {
	t.Helper()
	bin := t.TempDir()
	record := filepath.Join(bin, "cosign.args")
	recorder := "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$COSIGN_RECORD\"\nexit \"$COSIGN_EXIT\"\n"
	if err := os.WriteFile(filepath.Join(bin, "cosign"), []byte(recorder), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"COSIGN_RECORD="+record,
		"COSIGN_EXIT="+cosignExit,
	)
	output, err := command.CombinedOutput()
	recorded, readErr := os.ReadFile(record)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	return strings.Fields(string(recorded)), string(output), err
}

// TestCISignatureJobNamesTheEmbeddedRelease holds CI's catalog-signature job
// to the release a run prices with: it runs the job's script over the
// repository with a recording cosign, which must be asked to verify that
// release's bundle over its SHA256SUMS under the catalog's release workflow
// at the release's tag and the GitHub Actions issuer, and sha256sum must
// check every sheet the binary carries for that release. The job installs
// cosign as the release workflow does, so both use one cosign. The real
// verification runs on GitHub.
func TestCISignatureJobNamesTheEmbeddedRelease(t *testing.T) {
	script, installer := signatureJobScript(t)
	releaseInstaller := workflowStep{}
	for _, steps := range workflowJobs(t, "release.yml") {
		for _, step := range steps {
			if strings.HasPrefix(step.Uses, "sigstore/cosign-installer@") {
				releaseInstaller = step
			}
		}
	}
	if installer.Uses != releaseInstaller.Uses || !maps.Equal(installer.With, releaseInstaller.With) {
		t.Errorf("the catalog-signature job installs cosign with %s %v, and the release workflow with %s %v", installer.Uses, installer.With, releaseInstaller.Uses, releaseInstaller.With)
	}

	version := catalogdata.Version
	directory := "catalog/" + version + "/"
	args, output, err := runSignatureJob(t, script, repositoryRoot, "0")
	if err != nil {
		t.Fatalf("the catalog-signature job fails over the repository: %v\n%s", err, output)
	}
	want := []string{
		"verify-blob",
		"--bundle", directory + "SHA256SUMS.sigstore.json",
		"--certificate-identity", releaseIdentity(version),
		"--certificate-oidc-issuer", actionsIssuer,
		directory + "SHA256SUMS",
	}
	if !slices.Equal(args, want) {
		t.Errorf("the catalog-signature job runs cosign %q, want cosign %q", args, want)
	}
	if _, err := fs.Stat(catalogdata.Signatures, version+"/SHA256SUMS.sigstore.json"); err != nil {
		t.Errorf("the binary carries no bundle for release %s: %v", version, err)
	}
	sheets, err := fs.Glob(catalogdata.Sheets, version+"/*.json")
	if err != nil {
		t.Fatal(err)
	}
	checked := strings.Split(strings.TrimSpace(output), "\n")
	for _, sheet := range sheets {
		if line := path.Base(sheet) + ": OK"; !slices.Contains(checked, line) {
			t.Errorf("the catalog-signature job's sha256sum does not check %s; it printed %q", sheet, checked)
		}
	}
	if len(sheets) == 0 || len(checked) != len(sheets) {
		t.Errorf("the catalog-signature job checked %d lines, and release %s carries %d sheets", len(checked), version, len(sheets))
	}
}

// TestCISignatureJobFailsOnATamperedSheetOrSignature holds that the
// catalog-signature job fails when cosign refuses the bundle, and when a
// sheet differs from the signed manifest, run over a copy of the release.
func TestCISignatureJobFailsOnATamperedSheetOrSignature(t *testing.T) {
	script, _ := signatureJobScript(t)
	version := catalogdata.Version
	copyRelease := func(t *testing.T) (string, string) {
		root := t.TempDir()
		release := filepath.Join(root, "catalog", version)
		if err := os.MkdirAll(release, 0o755); err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(filepath.Join(repositoryRoot, "catalog", version))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(repositoryRoot, "catalog", version, file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(release, file.Name()), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root, release
	}

	root, _ := copyRelease(t)
	if _, output, err := runSignatureJob(t, script, root, "0"); err != nil {
		t.Fatalf("the catalog-signature job fails over an untouched copy: %v\n%s", err, output)
	}
	if _, output, err := runSignatureJob(t, script, root, "1"); err == nil {
		t.Errorf("the catalog-signature job passes when cosign refuses the bundle:\n%s", output)
	}

	root, release := copyRelease(t)
	sheets, err := filepath.Glob(filepath.Join(release, "*-*.json"))
	if err != nil || len(sheets) == 0 {
		t.Fatalf("release %s copy holds no sheet: %v", version, err)
	}
	data, err := os.ReadFile(sheets[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sheets[0], append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, output, err := runSignatureJob(t, script, root, "0"); err == nil {
		t.Errorf("the catalog-signature job passes with a sheet that differs from the signed manifest:\n%s", output)
	}
}

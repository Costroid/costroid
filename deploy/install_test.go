package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	installScript  = "../scripts/install.sh"
	installVersion = "1.0.0-alpha.99"
	installCommit  = "0123456789abcdef0123456789abcdef01234567"
)

// stampedInstallScript runs the release workflow's own step that writes the
// install script for a release, and returns the script it wrote.
func stampedInstallScript(t *testing.T) []byte {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(read(t, filepath.Join(workflows, "release.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	var stamp *releaseStep
	for i, step := range workflow.Jobs["binaries"].Steps {
		if strings.Contains(step.Run, "scripts/install.sh") {
			stamp = &workflow.Jobs["binaries"].Steps[i]
		}
	}
	if stamp == nil {
		t.Fatal("the binaries job writes no install script")
	}
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(work, "scripts", "install.sh"), read(t, installScript))
	runStep(t, *stamp, work, append(os.Environ(), "VERSION="+installVersion, "GITHUB_SHA="+installCommit))
	return read(t, filepath.Join(work, "dist", "costroid-install.sh"))
}

// installRun runs an install script against the scratch release r serves,
// with uname reporting Linux on machine, and PATH holding the stubs and only
// the system tools the script needs, so a cosign on this machine stays out.
type installRun struct {
	r       *release
	script  []byte
	machine string
	cosign  bool
}

func (run installRun) start(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()
	r := run.r
	r.script(filepath.Join(r.bin, "uname"), `case $1 in -s) echo Linux ;; -m) echo `+run.machine+` ;; esac`)
	if run.cosign {
		r.script(filepath.Join(r.bin, "cosign"), `printf '%s\n' "$@" > "$STUB_ROOT/cosign"
echo cosign >> "$STUB_ROOT/urls"
exit ${STUB_COSIGN_EXIT:-0}`)
	}
	system := filepath.Join(r.root, "system")
	if err := os.Mkdir(system, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"bash", "sh", "mktemp", "rm", "awk", "cut", "sha256sum", "mkdir", "cp", "chmod", "mv"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(system, tool)); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(r.root, "costroid-install.sh")
	write(t, script, run.script)
	command := exec.Command(filepath.Join(system, "sh"), append([]string{script}, args...)...)
	command.Dir = r.root
	command.Env = append([]string{"PATH=" + r.bin + ":" + system, "HOME=" + r.root, "TMPDIR=" + r.root, "STUB_ROOT=" + r.root}, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil && command.ProcessState == nil {
		t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), output.String()
}

// TestInstallScriptChecksTheSums runs the script a release carries, as the
// release workflow writes it, against a scratch release. It installs the
// binary only after cosign accepts the signature on SHA256SUMS for the
// release's tag and commit, and only when the binary matches its line.
func TestInstallScriptChecksTheSums(t *testing.T) {
	stamped := stampedInstallScript(t)
	for _, line := range []string{"version=" + installVersion, "commit=" + installCommit} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).Match(stamped) {
			t.Fatalf("the release's script holds no line %q", line)
		}
	}

	r := newRelease(t)
	code, output := installRun{r: r, script: stamped, machine: "x86_64", cosign: true}.start(t, nil)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	installed := filepath.Join(r.root, ".local", "bin", "costroid")
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0o755 || string(read(t, installed)) != releaseBytes {
		t.Fatalf("installed %v, %v", info, err)
	}
	args := strings.Split(strings.TrimSpace(string(read(t, filepath.Join(r.root, "cosign")))), "\n")
	scratch := filepath.Dir(args[len(args)-1])
	want := []string{
		"verify-blob", "--bundle", filepath.Join(scratch, "SHA256SUMS.sigstore.json"),
		"--certificate-identity", releaseIdentity + installVersion,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--certificate-github-workflow-sha", installCommit, filepath.Join(scratch, "SHA256SUMS"),
	}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Errorf("cosign ran with\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
	base := "https://github.com/Costroid/costroid/releases/download/v" + installVersion + "/"
	if order := string(read(t, filepath.Join(r.root, "urls"))); order != base+"SHA256SUMS\n"+base+"SHA256SUMS.sigstore.json\ncosign\n"+base+"costroid-linux-amd64\n" {
		t.Errorf("fetched and checked in this order:\n%s", order)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch directory %s is left: %v", scratch, err)
	}
	for _, said := range []string{"costroid: installed v" + installVersion + " at " + installed + ", checked by its signature", "is not on your PATH"} {
		if !strings.Contains(output, said) {
			t.Errorf("the script did not say %q:\n%s", said, output)
		}
	}

	otherLine := sumOf([]byte("arm64")) + "  costroid-linux-arm64\n"
	refusals := []struct {
		name    string
		run     installRun
		env     []string
		swap    string
		sums    string
		cause   string
		fetched string
	}{
		{name: "a signature cosign refuses", run: installRun{script: stamped, machine: "x86_64", cosign: true}, env: []string{"STUB_COSIGN_EXIT=1"},
			cause: "costroid: the signature on SHA256SUMS of v" + installVersion + " does not verify, so nothing was installed", fetched: "cosign\n"},
		{name: "a binary off its line", run: installRun{script: stamped, machine: "x86_64", cosign: true}, swap: "a swapped binary",
			cause: "costroid: costroid-linux-amd64 does not match its line in the signed SHA256SUMS of v" + installVersion + ", so nothing was installed", fetched: base + "costroid-linux-amd64\n"},
		{name: "sums with no line for this machine", run: installRun{script: stamped, machine: "x86_64", cosign: true}, sums: otherLine,
			cause: "costroid: SHA256SUMS of v" + installVersion + " holds no line for costroid-linux-amd64", fetched: "cosign\n"},
		{name: "a machine no release is built for", run: installRun{script: stamped, machine: "riscv64", cosign: true},
			cause: "costroid: no release for riscv64"},
		{name: "no cosign", run: installRun{script: stamped, machine: "x86_64"},
			cause: "costroid: cosign is not on PATH, and nothing is installed without its check"},
		{name: "the repository's own copy", run: installRun{script: read(t, installScript), machine: "x86_64", cosign: true},
			cause: "costroid: this copy names no release"},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			r := newRelease(t)
			if refusal.swap != "" {
				r.file("costroid-linux-amd64", refusal.swap)
			}
			if refusal.sums != "" {
				r.file("SHA256SUMS", refusal.sums)
			}
			refusal.run.r = r
			target := filepath.Join(r.root, "target")
			code, output := refusal.run.start(t, refusal.env, target)
			if code != 1 || !strings.Contains(output, refusal.cause) {
				t.Fatalf("exit %d: %s", code, output)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("a refused release made the target directory: %v", err)
			}
			fetched, _ := os.ReadFile(filepath.Join(r.root, "urls"))
			if refusal.fetched == "" && len(fetched) != 0 || !strings.HasSuffix(string(fetched), refusal.fetched) {
				t.Errorf("the last step before the refusal was not %q:\n%s", refusal.fetched, fetched)
			}
		})
	}
}

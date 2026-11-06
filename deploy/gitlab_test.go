package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	gitLabTemplate  = "../recipes/gitlab-ci.yml"
	recipesDir      = "../recipes"
	releaseIdentity = "https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v"
	releaseBytes    = "the released bytes"
)

// gitLabTemplateFile is the part of the template the install reads.
type gitLabTemplateFile struct {
	Variables map[string]string `yaml:"variables"`
	Install   struct {
		Variables    map[string]string `yaml:"variables"`
		BeforeScript []string          `yaml:"before_script"`
	} `yaml:".costroid-install"`
}

func readGitLabTemplate(t *testing.T) (string, gitLabTemplateFile) {
	t.Helper()
	source := string(read(t, gitLabTemplate))
	var file gitLabTemplateFile
	if err := yaml.Unmarshal([]byte(source), &file); err != nil {
		t.Fatal(err)
	}
	return source, file
}

// recipesRelease is the release every GitHub recipe pins the action to.
func recipesRelease(t *testing.T) string {
	t.Helper()
	pin := regexp.MustCompile(`Costroid/costroid/action@[0-9a-f]{40} # v(\S+)`)
	names, err := filepath.Glob(filepath.Join(recipesDir, "github-*.yml"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no GitHub recipes: %v", err)
	}
	releases := map[string]bool{}
	for _, name := range names {
		for _, match := range pin.FindAllStringSubmatch(string(read(t, name)), -1) {
			releases[match[1]] = true
		}
	}
	if len(releases) != 1 {
		t.Fatalf("the GitHub recipes pin %d releases: %v", len(releases), releases)
	}
	for release := range releases {
		return release
	}
	return ""
}

// release is a scratch release server: curl is a stub that copies the file a
// URL names from a directory, and cosign is a stub the template downloads.
type release struct {
	t    *testing.T
	root string
	bin  string
	dir  string
}

func newRelease(t *testing.T) *release {
	t.Helper()
	root := t.TempDir()
	r := &release{t: t, root: root, bin: filepath.Join(root, "bin"), dir: filepath.Join(root, "release")}
	for _, dir := range []string{r.bin, r.dir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.script(filepath.Join(r.bin, "apt-get"), "exit 0")
	r.script(filepath.Join(r.bin, "curl"), `while [ $# -gt 0 ]; do
  case $1 in --output) out=$2; shift ;; https://*) echo "$1" >> "$STUB_ROOT/urls"; url=$1 ;; esac
  shift
done
cp "$STUB_ROOT/release/${url##*/}" "$out"`)
	r.script(filepath.Join(r.dir, "cosign-linux-amd64"), `printf '%s\n' "$@" > "$STUB_ROOT/cosign"
exit ${STUB_COSIGN_EXIT:-0}`)
	r.file("costroid-linux-amd64", releaseBytes)
	r.file("SHA256SUMS.sigstore.json", "{}")
	r.file("SHA256SUMS", fmt.Sprintf("%s  costroid-linux-arm64\n%s  costroid-linux-amd64\n", sumOf([]byte("arm64")), sumOf([]byte(releaseBytes))))
	return r
}

func (r *release) script(path, body string) {
	r.t.Helper()
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

// file puts one asset in the release.
func (r *release) file(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// cosignPin is the variable that pins the stub cosign the release serves.
func (r *release) cosignPin() string {
	return "COSIGN_SHA256=" + sumOf(read(r.t, filepath.Join(r.dir, "cosign-linux-amd64")))
}

func sumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// install runs the template's before_script with the binary's destination in
// the scratch directory, and the template's variables as GitLab sets them.
func (r *release) install(file gitLabTemplateFile, env ...string) (int, string) {
	r.t.Helper()
	installed := filepath.Join(r.root, "costroid")
	script := strings.ReplaceAll(strings.Join(file.Install.BeforeScript, "\n"), "/usr/local/bin/costroid", installed)
	command := exec.Command("bash", "-eo", "pipefail", "-c", script)
	command.Dir = r.root
	command.Env = []string{"PATH=" + r.bin + ":" + os.Getenv("PATH"), "HOME=" + r.root, "TMPDIR=" + r.root, "STUB_ROOT=" + r.root}
	for _, variables := range []map[string]string{file.Variables, file.Install.Variables} {
		for key, value := range variables {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Env = append(command.Env, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil && command.ProcessState == nil {
		r.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), output.String()
}

func (r *release) installed() (string, bool) {
	data, err := os.ReadFile(filepath.Join(r.root, "costroid"))
	return string(data), err == nil
}

// TestGitLabTemplateNoHandHash holds the GitLab template to a release no one
// types a checksum for. It pins the release the GitHub recipes pin, and takes
// the binary's SHA-256 from that release's SHA256SUMS once cosign shows the
// release workflow signed it at the release's tag. A refused signature, a
// binary off its line or a cosign off its pin installs nothing.
func TestGitLabTemplateNoHandHash(t *testing.T) {
	source, file := readGitLabTemplate(t)
	if strings.Contains(source, "COSTROID_SHA256") || strings.Contains(source, ":?set COSTROID_") {
		t.Fatal("the template still asks for a hand-set release variable")
	}
	version := recipesRelease(t)
	if file.Variables["COSTROID_VERSION"] != version {
		t.Fatalf("the template pins %q, the GitHub recipes %q", file.Variables["COSTROID_VERSION"], version)
	}
	if pinned := file.Install.Variables["COSIGN_SHA256"]; !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pinned) {
		t.Fatalf("cosign is pinned to %q", pinned)
	}

	r := newRelease(t)
	code, output := r.install(file, r.cosignPin())
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	if got, ok := r.installed(); !ok || got != releaseBytes {
		t.Fatalf("installed %q, %v", got, ok)
	}
	args := string(read(t, filepath.Join(r.root, "cosign")))
	for _, want := range []string{"verify-blob", releaseIdentity + version + "\n", "https://token.actions.githubusercontent.com\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("cosign ran without %q:\n%s", want, args)
		}
	}
	if urls := string(read(t, filepath.Join(r.root, "urls"))); !strings.Contains(urls, "/releases/download/v"+version+"/SHA256SUMS\n") {
		t.Errorf("fetched:\n%s", urls)
	}

	for name, refuse := range map[string]struct {
		env   []string
		swap  string
		cause string
	}{
		"a signature cosign refuses": {env: []string{"STUB_COSIGN_EXIT=1"}, cause: "does not verify"},
		"a binary off its line":      {swap: "a swapped binary", cause: "does not match its line"},
		"a cosign off its pin":       {env: []string{"COSIGN_SHA256=" + sumOf([]byte("another cosign"))}, cause: "does not match its pinned SHA-256"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			if refuse.swap != "" {
				r.file("costroid-linux-amd64", refuse.swap)
			}
			code, output := r.install(file, append([]string{r.cosignPin()}, refuse.env...)...)
			if code == 0 || !strings.Contains(output, refuse.cause) {
				t.Fatalf("exit %d: %s", code, output)
			}
			if _, ok := r.installed(); ok {
				t.Fatal("a refused release was installed")
			}
		})
	}
}

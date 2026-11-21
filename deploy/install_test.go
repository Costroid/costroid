package deploy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	installScript  = "../scripts/install.sh"
	installPage    = "../docs/install.md"
	installVersion = "1.0.0-alpha.99"
	installCommit  = "0123456789abcdef0123456789abcdef01234567"
	releaseBase    = "https://github.com/Costroid/costroid/releases/download/v" + installVersion + "/"
	// pathMark and fetchedMark are what the stub cosign on PATH and the stub
	// cosign the release serves write into the log of fetches when they run.
	pathMark    = "the cosign on PATH ran"
	fetchedMark = "the downloaded cosign ran"
	// unstated stands for a cosign on PATH whose version names no GitVersion.
	unstated = "-"
	// nextTail is what the script's last line says after the command it names.
	nextTail = " demo prints the comment a pull request gets."
)

var (
	cosignVersionLine = regexp.MustCompile(`(?m)^cosign_version=(v\S+)$`)
	cosignPinLine     = regexp.MustCompile(`(?m)^  ([a-z]+-[a-z0-9]+)\) cosign_sha256=([0-9a-f]{64}) ;;$`)
	buildTargets      = regexp.MustCompile(`for target in ([a-z0-9/ ]+); do`)
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

// scriptCosign is the cosign version the script pins, and its SHA-256 for
// each platform, as os-arch.
func scriptCosign(t *testing.T) (string, map[string]string) {
	t.Helper()
	script := read(t, installScript)
	version := cosignVersionLine.FindSubmatch(script)
	if version == nil {
		t.Fatal("the script pins no cosign version")
	}
	pins := map[string]string{}
	for _, pin := range cosignPinLine.FindAllSubmatch(script, -1) {
		pins[string(pin[1])] = string(pin[2])
	}
	return string(version[1]), pins
}

func cosignBase(t *testing.T) string {
	t.Helper()
	version, _ := scriptCosign(t)
	return "https://github.com/sigstore/cosign/releases/download/" + version + "/"
}

// withPin is script with its cosign pin for platform replaced by sum.
func withPin(t *testing.T, script []byte, platform, sum string) []byte {
	t.Helper()
	line := regexp.MustCompile(`(?m)^(  ` + regexp.QuoteMeta(platform) + `\) cosign_sha256=)[0-9a-f]{64}`)
	if !line.Match(script) {
		t.Fatalf("the script holds no cosign pin for %s", platform)
	}
	return line.ReplaceAll(script, []byte("${1}"+sum))
}

// openingComment is the script's first comment, its lines joined by spaces.
func openingComment(script []byte) string {
	var words []string
	for _, line := range strings.Split(string(script), "\n")[1:] {
		if !strings.HasPrefix(line, "#") {
			break
		}
		words = append(words, strings.TrimSpace(strings.TrimPrefix(line, "#")))
	}
	return strings.Join(words, " ")
}

// serveCosign puts a stub cosign for platform in the release, as the cosign
// release serves it, and returns its SHA-256.
func (r *release) serveCosign(platform string) string {
	r.t.Helper()
	r.script(filepath.Join(r.dir, "cosign-"+platform), `printf '%s\n' "$@" > "$STUB_ROOT/cosign"
echo '`+fetchedMark+`' >> "$STUB_ROOT/urls"
exit ${STUB_COSIGN_EXIT:-0}`)
	return sumOf(read(r.t, filepath.Join(r.dir, "cosign-"+platform)))
}

// serveBinary puts a binary for platform in the release, with SHA256SUMS
// naming it, and returns its bytes. The binary says it ran, with its arguments.
func (r *release) serveBinary(platform string) string {
	r.t.Helper()
	body := "#!/bin/sh\necho \"the installed costroid ran $*\"\n"
	r.file("costroid-"+platform, body)
	if err := os.Chmod(filepath.Join(r.dir, "costroid-"+platform), 0o755); err != nil {
		r.t.Fatal(err)
	}
	r.file("SHA256SUMS", fmt.Sprintf("%s  costroid-linux-riscv64\n%s  costroid-%s\n", sumOf([]byte("riscv64")), sumOf([]byte(body)), platform))
	return body
}

// installRun runs an install script against the scratch release r serves,
// with uname reporting kernel on machine, and PATH holding the stubs and only
// the system tools the script needs, so a cosign on this machine stays out.
// On Darwin, shasum stands in for sha256sum, as on macOS.
type installRun struct {
	r       *release
	script  []byte
	kernel  string
	machine string
	// cosign is the version a cosign on PATH states, unstated for one that
	// states none, or empty for no cosign on PATH.
	cosign string
	// first is a directory put on PATH before the stubs.
	first string
	// noSum leaves both sha256sum and shasum off PATH.
	noSum bool
}

type installResult struct {
	code   int
	output string
	// path is the PATH the script ran with.
	path string
}

func (run installRun) start(t *testing.T, env []string, args ...string) installResult {
	t.Helper()
	r := run.r
	kernel := run.kernel
	if kernel == "" {
		kernel = "Linux"
	}
	r.script(filepath.Join(r.bin, "uname"), `case $1 in -s) echo `+kernel+` ;; -m) echo `+run.machine+` ;; esac`)
	switch run.cosign {
	case "":
	case unstated:
		r.script(filepath.Join(r.bin, "cosign"), `[ "$1" = version ] && exit 0
echo '`+pathMark+`' >> "$STUB_ROOT/urls"`)
	default:
		r.script(filepath.Join(r.bin, "cosign"), `if [ "$1" = version ]; then
  printf '%s\n' '  ______   ______        _______. __    _______ .__   __.' '' 'cosign: A tool for Container Signing, Verification and Storage in an OCI registry.' 'GitVersion:    `+run.cosign+`' 'GitCommit:     unknown'
  exit 0
fi
printf '%s\n' "$@" > "$STUB_ROOT/cosign"
echo '`+pathMark+`' >> "$STUB_ROOT/urls"
exit ${STUB_COSIGN_EXIT:-0}`)
	}
	system := filepath.Join(r.root, "system")
	if err := os.Mkdir(system, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := []string{"bash", "sh", "mktemp", "rm", "awk", "cut", "sha256sum", "mkdir", "cp", "chmod", "mv"}
	if kernel == "Darwin" || run.noSum {
		tools = slices.DeleteFunc(tools, func(tool string) bool { return tool == "sha256sum" })
	}
	if kernel == "Darwin" && !run.noSum {
		sha256sum, err := exec.LookPath("sha256sum")
		if err != nil {
			t.Fatal(err)
		}
		r.script(filepath.Join(r.bin, "shasum"), `[ "$1 $2" = "-a 256" ] || { echo "shasum: want -a 256, got $*" >&2; exit 2; }
shift 2
exec `+sha256sum+` "$@"`)
	}
	for _, tool := range tools {
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
	path := r.bin + ":" + system
	if run.first != "" {
		path = run.first + ":" + path
	}
	command := exec.Command(filepath.Join(system, "sh"), append([]string{script}, args...)...)
	command.Dir = r.root
	command.Env = append([]string{"PATH=" + path, "HOME=" + r.root, "TMPDIR=" + r.root, "STUB_ROOT=" + r.root}, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil && command.ProcessState == nil {
		t.Fatal(err)
	}
	return installResult{code: command.ProcessState.ExitCode(), output: output.String(), path: path}
}

// fetches is the log of downloads and cosign runs, one a line.
func (r *release) fetches() []string {
	data, _ := os.ReadFile(filepath.Join(r.root, "urls"))
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// cosignArgs is what the cosign that ran was called with, one a line.
func (r *release) cosignArgs() []string {
	data, err := os.ReadFile(filepath.Join(r.root, "cosign"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func verifyArgs(scratch string) []string {
	return []string{
		"verify-blob", "--bundle", filepath.Join(scratch, "SHA256SUMS.sigstore.json"),
		"--certificate-identity", releaseIdentity + installVersion,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--certificate-github-workflow-sha", installCommit, filepath.Join(scratch, "SHA256SUMS"),
	}
}

func fetchLine(t *testing.T) string {
	t.Helper()
	version, _ := scriptCosign(t)
	return "cosign " + version + " to its temporary directory, checks it against the SHA-256 it holds and removes it afterwards"
}

// TestInstallScriptChecksTheSums runs the script a release carries, as the
// release workflow writes it, against a scratch release, with cosign 3 on
// PATH. It installs the binary only after cosign accepts the signature on
// SHA256SUMS for the release's tag and commit, and only when the binary
// matches its line, and it says each step before it takes it.
func TestInstallScriptChecksTheSums(t *testing.T) {
	stamped := stampedInstallScript(t)
	for _, line := range []string{"version=" + installVersion, "commit=" + installCommit} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).Match(stamped) {
			t.Fatalf("the release's script holds no line %q", line)
		}
	}

	r := newRelease(t)
	body := r.serveBinary("linux-amd64")
	got := installRun{r: r, script: stamped, machine: "x86_64", cosign: "v3.0.6"}.start(t, nil)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.output)
	}
	directory := filepath.Join(r.root, ".local", "bin")
	installed := filepath.Join(directory, "costroid")
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0o755 || string(read(t, installed)) != body {
		t.Fatalf("installed %v, %v", info, err)
	}
	args := r.cosignArgs()
	scratch := filepath.Dir(args[len(args)-1])
	if want := verifyArgs(scratch); !slices.Equal(args, want) {
		t.Errorf("cosign ran with\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
	}
	if order, want := r.fetches(), []string{releaseBase + "SHA256SUMS", releaseBase + "SHA256SUMS.sigstore.json", pathMark, releaseBase + "costroid-linux-amd64"}; !slices.Equal(order, want) {
		t.Errorf("fetched and checked in this order:\n%s\nwant\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("the scratch directory %s is left: %v", scratch, err)
	}
	want := strings.Join([]string{
		"costroid: checking the signature on SHA256SUMS of v" + installVersion,
		"costroid: downloading costroid-linux-amd64",
		"costroid: installed v" + installVersion + " at " + installed + ", checked by its signature",
		"costroid: " + directory + " is not on your PATH. To add it in this shell, run:",
		`  export PATH="` + directory + `:$PATH"`,
		"Next: " + installed + nextTail,
	}, "\n") + "\n"
	if got.output != want {
		t.Errorf("the script printed\n%s\nwant\n%s", got.output, want)
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
		{name: "a signature cosign refuses", run: installRun{script: stamped, machine: "x86_64", cosign: "v3.0.6"}, env: []string{"STUB_COSIGN_EXIT=1"},
			cause: "costroid: the signature on SHA256SUMS of v" + installVersion + " does not verify, so nothing was installed", fetched: pathMark},
		{name: "a binary off its line", run: installRun{script: stamped, machine: "x86_64", cosign: "v3.0.6"}, swap: "a swapped binary",
			cause: "costroid: costroid-linux-amd64 does not match its line in the signed SHA256SUMS of v" + installVersion + ", so nothing was installed", fetched: releaseBase + "costroid-linux-amd64"},
		{name: "sums with no line for this machine", run: installRun{script: stamped, machine: "x86_64", cosign: "v3.0.6"}, sums: otherLine,
			cause: "costroid: SHA256SUMS of v" + installVersion + " holds no line for costroid-linux-amd64", fetched: pathMark},
		{name: "a machine no release is built for", run: installRun{script: stamped, machine: "riscv64", cosign: "v3.0.6"},
			cause: "costroid: no release for riscv64"},
		{name: "no tool for SHA-256", run: installRun{script: stamped, machine: "x86_64", cosign: "v3.0.6", noSum: true},
			cause: "costroid: neither sha256sum nor shasum is on PATH"},
		{name: "the repository's own copy", run: installRun{script: read(t, installScript), machine: "x86_64", cosign: "v3.0.6"},
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
			got := refusal.run.start(t, refusal.env, target)
			if got.code != 1 || !strings.Contains(got.output, refusal.cause) {
				t.Fatalf("exit %d: %s", got.code, got.output)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("a refused release made the target directory: %v", err)
			}
			fetched := r.fetches()
			if refusal.fetched == "" && len(fetched) != 0 || refusal.fetched != "" && (len(fetched) == 0 || fetched[len(fetched)-1] != refusal.fetched) {
				t.Errorf("the last step before the refusal was not %q:\n%s", refusal.fetched, strings.Join(fetched, "\n"))
			}
		})
	}
}

// TestInstallScriptFetchesCosignAtItsPinWhenNoneIsOnPath runs the script on
// each platform a release is built for with no cosign on PATH. It says so,
// downloads cosign for that platform from the cosign release, runs it only
// once it matches the script's pin for that platform, checks the signature
// with it before it downloads the binary, and leaves no cosign behind. Its
// opening comment names every file it downloads and where from.
func TestInstallScriptFetchesCosignAtItsPinWhenNoneIsOnPath(t *testing.T) {
	stamped := stampedInstallScript(t)
	comment := openingComment(stamped)
	for _, platform := range []struct{ kernel, machine, name string }{
		{"Linux", "x86_64", "linux-amd64"},
		{"Linux", "aarch64", "linux-arm64"},
		{"Darwin", "x86_64", "darwin-amd64"},
		{"Darwin", "arm64", "darwin-arm64"},
	} {
		t.Run(platform.name, func(t *testing.T) {
			r := newRelease(t)
			body := r.serveBinary(platform.name)
			script := withPin(t, stamped, platform.name, r.serveCosign(platform.name))
			got := installRun{r: r, script: script, kernel: platform.kernel, machine: platform.machine}.start(t, nil)
			if got.code != 0 {
				t.Fatalf("exit %d: %s", got.code, got.output)
			}
			if first, _, _ := strings.Cut(got.output, "\n"); first != "costroid: cosign 3 or later is not on PATH, so this script downloads "+fetchLine(t) {
				t.Errorf("the script opened with %q", first)
			}
			if installed := filepath.Join(r.root, ".local", "bin", "costroid"); string(read(t, installed)) != body {
				t.Errorf("installed %q", read(t, installed))
			}
			cosignURL := cosignBase(t) + "cosign-" + platform.name
			binaryURL := releaseBase + "costroid-" + platform.name
			want := []string{cosignURL, releaseBase + "SHA256SUMS", releaseBase + "SHA256SUMS.sigstore.json", fetchedMark, binaryURL}
			if order := r.fetches(); !slices.Equal(order, want) {
				t.Errorf("fetched and checked in this order:\n%s\nwant\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
			}
			args := r.cosignArgs()
			scratch := filepath.Dir(args[len(args)-1])
			if want := verifyArgs(scratch); !slices.Equal(args, want) {
				t.Errorf("the downloaded cosign ran with\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(want, "\n"))
			}
			if _, err := os.Stat(scratch); !os.IsNotExist(err) {
				t.Errorf("the temporary directory %s, with the downloaded cosign, is left: %v", scratch, err)
			}
			for _, url := range []string{cosignURL, releaseBase + "SHA256SUMS", releaseBase + "SHA256SUMS.sigstore.json", binaryURL} {
				named := strings.ReplaceAll(strings.ReplaceAll(url, "v"+installVersion, "v<version>"), platform.name, "<os>-<arch>")
				directory, file := path.Split(named)
				if !strings.Contains(comment, directory) || !strings.Contains(comment, " "+file) {
					t.Errorf("the opening comment does not name %s in %s:\n%s", file, directory, comment)
				}
			}
		})
	}
}

// TestInstallScriptRefusesAFetchedCosignOffItsPin serves a cosign that is not
// the one the script pins. The script never runs it, never downloads it again,
// downloads nothing else and installs nothing.
func TestInstallScriptRefusesAFetchedCosignOffItsPin(t *testing.T) {
	version, _ := scriptCosign(t)
	r := newRelease(t)
	r.serveBinary("linux-amd64")
	r.serveCosign("linux-amd64")
	target := filepath.Join(r.root, "target")
	got := installRun{r: r, script: stampedInstallScript(t), machine: "x86_64"}.start(t, nil, target)
	refusal := "costroid: cosign " + version + " does not match its pinned SHA-256, so nothing was installed\n"
	if got.code != 1 || !strings.HasSuffix(got.output, refusal) {
		t.Fatalf("exit %d, want 1 and the last line %q:\n%s", got.code, refusal, got.output)
	}
	if fetched, want := r.fetches(), []string{cosignBase(t) + "cosign-linux-amd64"}; !slices.Equal(fetched, want) {
		t.Errorf("fetched and ran\n%s\nwant only the one download of cosign", strings.Join(fetched, "\n"))
	}
	if args := r.cosignArgs(); args != nil {
		t.Errorf("the cosign off its pin ran with %v", args)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("a refused cosign made the target directory: %v", err)
	}
}

// TestInstallScriptNeverRunsACosignOlderThanThree puts on PATH a cosign that
// would accept any signature. When it states a version older than 3, or none,
// the script names it, never runs it to verify, and checks the signature with
// the cosign it downloads. A cosign that states 3 or later is the one used.
func TestInstallScriptNeverRunsACosignOlderThanThree(t *testing.T) {
	for _, onPath := range []struct {
		version string
		named   string
		used    bool
	}{
		{version: "v2.2.1", named: "cosign v2.2.1"},
		{version: "2.9.0", named: "cosign 2.9.0"},
		{version: "v1.13.6", named: "cosign v1.13.6"},
		{version: "devel", named: "cosign devel"},
		{version: unstated, named: "cosign of an unknown version"},
		{version: "v3.0.6", used: true},
		{version: "3.1.4", used: true},
		{version: "v10.0.0", used: true},
	} {
		t.Run(onPath.version, func(t *testing.T) {
			r := newRelease(t)
			r.serveBinary("linux-amd64")
			script := withPin(t, stampedInstallScript(t), "linux-amd64", r.serveCosign("linux-amd64"))
			got := installRun{r: r, script: script, machine: "x86_64", cosign: onPath.version}.start(t, nil)
			if got.code != 0 {
				t.Fatalf("exit %d: %s", got.code, got.output)
			}
			fetched := r.fetches()
			if onPath.used {
				if slices.Contains(fetched, fetchedMark) || !slices.Contains(fetched, pathMark) {
					t.Errorf("cosign %s on PATH was not the one that checked:\n%s", onPath.version, strings.Join(fetched, "\n"))
				}
				if strings.Contains(got.output, "downloads cosign") {
					t.Errorf("the script downloads cosign with %s on PATH:\n%s", onPath.version, got.output)
				}
				return
			}
			if slices.Contains(fetched, pathMark) || !slices.Contains(fetched, fetchedMark) {
				t.Errorf("a cosign that states %s checked the signature:\n%s", onPath.version, strings.Join(fetched, "\n"))
			}
			want := "costroid: " + onPath.named + " is on PATH and this check needs 3 or later, so this script downloads " + fetchLine(t)
			if first, _, _ := strings.Cut(got.output, "\n"); first != want {
				t.Errorf("the script opened with\n%s\nwant\n%s", first, want)
			}
		})
	}
}

// TestInstallScriptNamesBothWaysWhenItCannotFetch has the download of cosign
// fail, with no cosign on PATH and with one older than 3. The script installs
// nothing and names both ways forward: cosign 3 or later, or the install
// page's block that installs by hand.
func TestInstallScriptNamesBothWaysWhenItCannotFetch(t *testing.T) {
	version, _ := scriptCosign(t)
	if !strings.Contains(string(read(t, installPage)), "\n## The binary\n") {
		t.Fatal("the install page has no section The binary for the refusal to name")
	}
	refusal := "costroid: could not download cosign " + version + ", so nothing was installed: install cosign 3 or later and run this again, or install costroid by hand as https://github.com/Costroid/costroid/blob/main/docs/install.md#the-binary shows\n"
	for _, onPath := range []string{"", "v2.2.1"} {
		t.Run("cosign on PATH "+onPath, func(t *testing.T) {
			r := newRelease(t)
			r.serveBinary("linux-amd64")
			if err := os.Remove(filepath.Join(r.dir, "cosign-linux-amd64")); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(r.root, "target")
			got := installRun{r: r, script: stampedInstallScript(t), machine: "x86_64", cosign: onPath}.start(t, nil, target)
			if got.code != 1 || !strings.HasSuffix(got.output, refusal) {
				t.Fatalf("exit %d, want 1 and the last line\n%s\ngot\n%s", got.code, refusal, got.output)
			}
			if fetched, want := r.fetches(), []string{cosignBase(t) + "cosign-linux-amd64"}; !slices.Equal(fetched, want) {
				t.Errorf("fetched and ran\n%s\nwant only the one download of cosign", strings.Join(fetched, "\n"))
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("a refusal made the target directory: %v", err)
			}
		})
	}
}

// TestInstallScriptEndsOnACommandThatRuns runs the command the script's last
// line names, as printed, under the PATH the script ran with. Off PATH it is
// the installed binary's full path, and the line above it, pasted, puts the
// directory on PATH. On PATH it is the short form, unless another costroid
// comes first.
func TestInstallScriptEndsOnACommandThatRuns(t *testing.T) {
	run := func(t *testing.T, path, command string) string {
		t.Helper()
		shell := exec.Command("sh", "-c", command)
		shell.Env = []string{"PATH=" + path}
		output, err := shell.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", command, err, output)
		}
		return string(output)
	}
	for _, place := range []string{"off PATH", "on PATH", "behind another costroid"} {
		t.Run(place, func(t *testing.T) {
			r := newRelease(t)
			r.serveBinary("linux-amd64")
			directory := filepath.Join(r.root, "target")
			first := ""
			switch place {
			case "on PATH":
				first = directory
			case "behind another costroid":
				other := filepath.Join(r.root, "other")
				if err := os.Mkdir(other, 0o755); err != nil {
					t.Fatal(err)
				}
				r.script(filepath.Join(other, "costroid"), "echo 'another costroid ran'")
				first = other + ":" + directory
			}
			got := installRun{r: r, script: stampedInstallScript(t), machine: "x86_64", cosign: "v3.0.6", first: first}.start(t, nil, directory)
			if got.code != 0 {
				t.Fatalf("exit %d: %s", got.code, got.output)
			}
			lines := strings.Split(strings.TrimSuffix(got.output, "\n"), "\n")
			last := lines[len(lines)-1]
			command, found := strings.CutPrefix(last, "Next: ")
			command, ended := strings.CutSuffix(command, nextTail)
			if !found || !ended {
				t.Fatalf("the last line is %q", last)
			}
			command += " demo"
			want := filepath.Join(directory, "costroid") + " demo"
			if place == "on PATH" {
				want = "costroid demo"
			}
			if command != want {
				t.Errorf("the last line names %q, want %q", command, want)
			}
			if output := run(t, got.path, command); output != "the installed costroid ran demo\n" {
				t.Errorf("%s printed %q", command, output)
			}
			offPath := slices.Contains(lines, "costroid: "+directory+" is not on your PATH. To add it in this shell, run:")
			if offPath != (place == "off PATH") {
				t.Errorf("%s, the script said the directory is not on PATH: %v\n%s", place, offPath, got.output)
			}
			if place == "off PATH" {
				paste := lines[len(lines)-2]
				if output := run(t, got.path, paste+"; costroid demo"); output != "the installed costroid ran demo\n" {
					t.Errorf("after %s, costroid demo printed %q", paste, output)
				}
			}
		})
	}
}

// TestInstallScriptPinsTheCosignTheGitLabTemplatePins holds the script's
// cosign to the GitLab template's: one version, and a SHA-256 for each
// platform a release is built for, the Linux amd64 one equal to the
// template's. The opening comment names the version it downloads.
func TestInstallScriptPinsTheCosignTheGitLabTemplatePins(t *testing.T) {
	version, pins := scriptCosign(t)
	_, template := readGitLabTemplate(t)
	if version != template.Install.Variables["COSIGN_VERSION"] {
		t.Errorf("the script pins cosign %s, the GitLab template %s", version, template.Install.Variables["COSIGN_VERSION"])
	}
	if pins["linux-amd64"] != template.Install.Variables["COSIGN_SHA256"] {
		t.Errorf("the script pins cosign-linux-amd64 at %s, the GitLab template at %s", pins["linux-amd64"], template.Install.Variables["COSIGN_SHA256"])
	}
	match := buildTargets.FindStringSubmatch(string(read(t, filepath.Join(workflows, "release.yml"))))
	if match == nil {
		t.Fatal("release.yml builds no targets")
	}
	var built []string
	for _, target := range strings.Fields(match[1]) {
		built = append(built, strings.ReplaceAll(target, "/", "-"))
	}
	var held []string
	for platform := range pins {
		held = append(held, platform)
	}
	slices.Sort(built)
	slices.Sort(held)
	if !slices.Equal(held, built) {
		t.Errorf("the script pins cosign for %v, and a release is built for %v", held, built)
	}
	seen := map[string]string{}
	for platform, sum := range pins {
		if other, ok := seen[sum]; ok {
			t.Errorf("%s and %s hold the same SHA-256", platform, other)
		}
		seen[sum] = platform
	}
	if comment := openingComment(read(t, installScript)); !strings.Contains(comment, "https://github.com/sigstore/cosign/releases/download/"+version+"/") {
		t.Errorf("the opening comment does not name cosign %s:\n%s", version, comment)
	}
}

// TestInstallScriptNeverInstallsOnTheChecksumAlone serves a release whose
// binary matches SHA256SUMS, so a checksum alone would pass, and takes away
// each way the signature could be checked. The script never downloads the
// binary, and installs nothing.
func TestInstallScriptNeverInstallsOnTheChecksumAlone(t *testing.T) {
	cases := []struct {
		name    string
		cosign  string
		pin     bool
		absent  bool
		refused bool
		named   bool
	}{
		{name: "cosign 3 on PATH refuses the signature", cosign: "v3.0.6", refused: true},
		{name: "the downloaded cosign refuses the signature", pin: true, refused: true},
		{name: "no cosign, and its download fails", absent: true},
		{name: "no cosign, and its download is off its pin"},
		{name: "a cosign older than 3 that accepts anything, and the download fails", cosign: "v2.2.1", absent: true},
		{name: "a cosign older than 3 that accepts anything, and the download is off its pin", cosign: "v2.2.1"},
		{name: "a cosign that states no version, and the download fails", cosign: unstated, absent: true},
		{name: "an environment that names a cosign which accepts anything, and the download fails", named: true, absent: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRelease(t)
			r.serveBinary("linux-amd64")
			script := stampedInstallScript(t)
			if sum := r.serveCosign("linux-amd64"); c.pin {
				script = withPin(t, script, "linux-amd64", sum)
			}
			if c.absent {
				if err := os.Remove(filepath.Join(r.dir, "cosign-linux-amd64")); err != nil {
					t.Fatal(err)
				}
			}
			var env []string
			if c.refused {
				env = []string{"STUB_COSIGN_EXIT=1"}
			}
			if c.named {
				accepting, err := exec.LookPath("true")
				if err != nil {
					t.Fatal(err)
				}
				env = append(env, "cosign="+accepting)
			}
			target := filepath.Join(r.root, "target")
			got := installRun{r: r, script: script, machine: "x86_64", cosign: c.cosign}.start(t, env, target)
			if got.code != 1 || !strings.Contains(got.output, "so nothing was installed") {
				t.Errorf("exit %d, want 1 and a refusal:\n%s", got.code, got.output)
			}
			if fetched := r.fetches(); slices.Contains(fetched, releaseBase+"costroid-linux-amd64") {
				t.Errorf("the binary was downloaded with no signature checked:\n%s", strings.Join(fetched, "\n"))
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("the target directory was made: %v", err)
			}
		})
	}
}

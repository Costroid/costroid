package recipelint

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	releaseCommit = "c8ab388e1cd410ca9a0cf327f062e0256017d94f"
	releaseBinary = "the released bytes"
	releaseAsset  = "costroid-linux-amd64"
)

// lsRemote is what git ls-remote --tags prints for annotated tags: the tag
// object, then the commit it points at.
const lsRemote = "2703a5a5cbc86bde1a32f5a04ca554988c33c5ec\trefs/tags/v1.2.2\n" +
	"17ec021200c2e0c5dc11ac28fceb7fc834819518\trefs/tags/v1.2.2^{}\n" +
	"0be99bdf73564b9c305bfd027dbf2f2d824f5272\trefs/tags/v" + testVersion + "\n" +
	releaseCommit + "\trefs/tags/v" + testVersion + "^{}\n" +
	"a3eafe5698de9b4803b468854d29fb8bd709f100\trefs/tags/nightly\n"

func sumOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// release is a scratch runner whose curl serves the files of one release, git
// lists its tags and cosign accepts or refuses by STUB_COSIGN_EXIT.
type release struct {
	*shell
	files string
}

func newRelease(t *testing.T) *release {
	t.Helper()
	r := &release{shell: newShell(t), files: filepath.Join(t.TempDir(), "release")}
	if err := os.Mkdir(r.files, 0o755); err != nil {
		t.Fatal(err)
	}
	r.serve(releaseAsset, releaseBinary)
	r.serve("SHA256SUMS", sumOf(releaseBinary)+"  "+releaseAsset+"\n"+sumOf("arm")+"  costroid-linux-arm64\n")
	r.serve("SHA256SUMS.sigstore.json", "{}")
	r.stub("uname", "echo x86_64")
	r.stub("git", `printf '%s\n' "$@" > "`+r.root+`/git-argv"
printf '%s' "$STUB_TAGS"`)
	r.stub("cosign", `printf '%s\n' "$@" > "`+r.root+`/cosign-argv"
exit ${STUB_COSIGN_EXIT:-0}`)
	r.stub("curl", `while [ $# -gt 0 ]; do
  case $1 in --output) out=$2; shift ;; https://*) url=$1 ;; esac
  shift
done
echo "$url" >> "`+r.root+`/urls"
file="`+r.files+`/${url##*/}"
[ -f "$file" ] || exit 22
cp "$file" "$out"`)
	return r
}

func (r *release) serve(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.files, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *release) install(env ...string) (int, string) {
	r.t.Helper()
	return r.run("install.sh", append([]string{
		"RUNNER_TEMP=" + r.root, "GITHUB_PATH=" + filepath.Join(r.root, "github-path"),
		"COSTROID_ACTION_REF=" + releaseCommit, "STUB_TAGS=" + lsRemote,
	}, env...)...)
}

func (r *release) installed() bool {
	_, err := os.Stat(filepath.Join(r.root, "costroid-bin", "costroid"))
	return err == nil
}

func (r *release) fetched() string {
	data, err := os.ReadFile(filepath.Join(r.root, "urls"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
}

func TestInstallWithNoPinTakesTheSignedSumOfTheReleaseAtTheActionsCommit(t *testing.T) {
	r := newRelease(t)
	code, output := r.install()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	if got := read(t, filepath.Join(r.root, "costroid-bin", "costroid")); got != releaseBinary {
		t.Fatalf("installed %q", got)
	}
	if got := read(t, filepath.Join(r.root, "git-argv")); got != "ls-remote\n--tags\nhttps://github.com/Costroid/costroid\n" {
		t.Fatalf("git argv:\n%s", got)
	}
	argv := read(t, filepath.Join(r.root, "cosign-argv"))
	for _, want := range []string{
		"verify-blob\n",
		"--certificate-identity\nhttps://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v" + testVersion + "\n",
		"--certificate-oidc-issuer\nhttps://token.actions.githubusercontent.com\n",
		"--certificate-github-workflow-sha\n" + releaseCommit + "\n",
		"/SHA256SUMS.sigstore.json\n",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("cosign argv lacks %q:\n%s", want, argv)
		}
	}
	if !strings.HasSuffix(argv, "/SHA256SUMS\n") {
		t.Errorf("cosign did not check SHA256SUMS last:\n%s", argv)
	}
	for _, want := range []string{"SHA256SUMS", "SHA256SUMS.sigstore.json", releaseAsset} {
		if !strings.Contains(r.fetched(), "/releases/download/v"+testVersion+"/"+want+"\n") {
			t.Errorf("did not fetch %s from v%s:\n%s", want, testVersion, r.fetched())
		}
	}
	if !strings.Contains(output, "costroid: installing "+testVersion+", the release at this action's commit") {
		t.Errorf("output: %s", output)
	}
}

func TestInstallWithNoPinRefusesASignatureCosignRejects(t *testing.T) {
	r := newRelease(t)
	code, output := r.install("STUB_COSIGN_EXIT=1")
	if code == 0 || !strings.Contains(output, "the signature on SHA256SUMS of v"+testVersion+" does not verify") {
		t.Fatalf("exit %d: %s", code, output)
	}
	if r.installed() || strings.Contains(r.fetched(), releaseAsset) {
		t.Fatalf("it went on after the signature failed:\n%s", r.fetched())
	}
}

func TestInstallWithNoPinRefusesABinaryTheSignedSumsDoNotName(t *testing.T) {
	for name, sums := range map[string]string{
		"another binary": sumOf("a swapped binary") + "  " + releaseAsset + "\n",
		"no line":        sumOf(releaseBinary) + "  costroid-darwin-amd64\n",
		"a longer name":  sumOf(releaseBinary) + "  " + releaseAsset + ".sigstore.json\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			r.serve("SHA256SUMS", sums)
			if code, output := r.install(); code == 0 || r.installed() {
				t.Fatalf("exit %d: %s", code, output)
			}
		})
	}
}

func TestInstallWithNoPinRefusesARefThatIsNoReleaseCommit(t *testing.T) {
	for name, ref := range map[string]string{
		"a local action":    "",
		"a branch":          "main",
		"a tag name":        "v" + testVersion,
		"an untagged SHA":   strings.Repeat("ab", 20),
		"a non-release":     "a3eafe5698de9b4803b468854d29fb8bd709f100",
		"the tag object":    "0be99bdf73564b9c305bfd027dbf2f2d824f5272",
		"a shortened SHA":   releaseCommit[:12],
		"an upper-case SHA": strings.ToUpper(releaseCommit),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			code, output := r.install("COSTROID_ACTION_REF=" + ref)
			if code == 0 || !strings.Contains(output, "pin Costroid/costroid/action to the full commit SHA of a release tag") {
				t.Fatalf("exit %d: %s", code, output)
			}
			if r.fetched() != "" || r.installed() {
				t.Fatalf("it fetched:\n%s", r.fetched())
			}
			if _, err := os.Stat(filepath.Join(r.root, "cosign-argv")); !os.IsNotExist(err) {
				t.Fatal("cosign ran")
			}
		})
	}
}

func TestInstallWithNoPinTakesTheHigherOfTwoReleasesOnOneCommit(t *testing.T) {
	r := newRelease(t)
	tags := lsRemote + "1111111111111111111111111111111111111111\trefs/tags/v" + testVersion + "-rc.1\n" +
		releaseCommit + "\trefs/tags/v" + testVersion + "-rc.1^{}\n"
	if code, output := r.install("STUB_TAGS=" + tags); code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	if got := read(t, filepath.Join(r.root, "cosign-argv")); !strings.Contains(got, "@refs/tags/v"+testVersion+"\n") {
		t.Fatalf("cosign argv:\n%s", got)
	}
}

func TestInstallWithAPinNeverAsksGitOrCosign(t *testing.T) {
	r := newRelease(t)
	code, output := r.install("COSTROID_VERSION="+testVersion, "COSTROID_SHA256="+sumOf(releaseBinary))
	if code != 0 || !r.installed() {
		t.Fatalf("exit %d: %s", code, output)
	}
	for _, name := range []string{"git-argv", "cosign-argv"} {
		if _, err := os.Stat(filepath.Join(r.root, name)); !os.IsNotExist(err) {
			t.Errorf("%s ran", name)
		}
	}
}

func TestInstallRefusesHalfAPin(t *testing.T) {
	for name, env := range map[string][]string{
		"a version alone": {"COSTROID_VERSION=" + testVersion},
		"a sum alone":     {"COSTROID_SHA256=" + sumOf(releaseBinary)},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			code, output := r.install(env...)
			if code == 0 || !strings.Contains(output, "set both version and sha256, or neither") {
				t.Fatalf("exit %d: %s", code, output)
			}
			if r.fetched() != "" || r.installed() {
				t.Fatalf("it fetched:\n%s", r.fetched())
			}
		})
	}
}

// The GitHub recipes leave the install to the action, so a repository needs
// no variable to name a release or its SHA-256.
func TestTheGitHubRecipesPinNoReleaseOfTheirOwn(t *testing.T) {
	for _, name := range []string{"github-claude-code.yml", "github-claude-code-action.yml", "github-codex.yml"} {
		source := read(t, filepath.Join(recipesDir, name))
		documents, err := parse([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, job := range entries(field(documents[0], "jobs")) {
			for _, step := range sequence(field(job.value, "steps")) {
				if costroidStage(step) == "" {
					continue
				}
				found++
				with := field(step, "with")
				for _, input := range []string{"version", "sha256"} {
					if field(with, input) != nil {
						t.Errorf("%s: job %s passes %s", name, job.key.Value, input)
					}
				}
			}
		}
		if found == 0 {
			t.Errorf("%s uses no Costroid step", name)
		}
		if strings.Contains(source, "COSTROID_VERSION") || strings.Contains(source, "COSTROID_SHA256") {
			t.Errorf("%s still names a release variable", name)
		}
	}
}

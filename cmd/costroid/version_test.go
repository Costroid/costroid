package main

import (
	"errors"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	costroid "github.com/Costroid/costroid"
)

// goInstallRelease is the version testdata/go-install.buildinfo records.
const goInstallRelease = "1.0.0-alpha.99"

// goInstallBuild is the build information of a binary that go install wrote
// from github.com/Costroid/costroid/cmd/costroid@v1.0.0-alpha.99, served by a
// module proxy on disk, as debug/buildinfo reads it back.
func goInstallBuild(t *testing.T) *debug.BuildInfo {
	t.Helper()
	info, err := debug.ParseBuildInfo(readFile(t, filepath.Join("testdata", "go-install.buildinfo")))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// withMain is info with another main module version and build settings.
func withMain(info *debug.BuildInfo, path, version string, settings ...debug.BuildSetting) *debug.BuildInfo {
	changed := *info
	changed.Main.Path, changed.Main.Version = path, version
	changed.Settings = append(slices.Clone(info.Settings), settings...)
	return &changed
}

func TestGoInstallBuildReadsItsModuleVersion(t *testing.T) {
	installed := goInstallBuild(t)
	revision := debug.BuildSetting{Key: "vcs.revision", Value: otherCommit}
	clean := debug.BuildSetting{Key: "vcs.modified", Value: "false"}
	builds := []struct {
		name                 string
		version, commit      string
		info                 *debug.BuildInfo
		wantVersion, wantSHA string
	}{
		{"go install at a tag", "dev", "", installed, goInstallRelease, ""},
		{"a release build keeps what the linker stamped", testRelease, testCommit, installed, testRelease, testCommit},
		{"go build in a clean checkout of a tag", "dev", "", withMain(installed, modulePath, "v1.0.0-alpha.13", revision, clean), "1.0.0-alpha.13", otherCommit},
		{"make build in a clean checkout of a tag", "dev", otherCommit, withMain(installed, modulePath, "v1.0.0-alpha.13", revision, clean), "1.0.0-alpha.13", otherCommit},
		{"go install at a branch", "dev", "", withMain(installed, modulePath, "v1.0.0-alpha.13.0.20261108062702-bd317fc4e247"), "dev", ""},
		{"go install before the first tag", "dev", "", withMain(installed, modulePath, "v0.0.0-20261108062702-bd317fc4e247"), "dev", ""},
		{"a checkout with changes", "dev", "", withMain(installed, modulePath, "v1.0.0-alpha.13+dirty", revision), "dev", ""},
		{"a modified checkout the version misses", "dev", "", withMain(installed, modulePath, "v1.0.0-alpha.13", revision, debug.BuildSetting{Key: "vcs.modified", Value: "true"}), "dev", ""},
		{"a build of the module's source with no version", "dev", "", withMain(installed, modulePath, "(devel)"), "dev", ""},
		{"another module's release", "dev", "", withMain(installed, "github.com/someone/costroid", "v1.0.0-alpha.99"), "dev", ""},
	}
	for _, build := range builds {
		t.Run(build.name, func(t *testing.T) {
			gotVersion, gotSHA := moduleBuild(build.version, build.commit, build.info)
			if gotVersion != build.wantVersion || gotSHA != build.wantSHA {
				t.Errorf("version %q, commit %q; want %q, %q", gotVersion, gotSHA, build.wantVersion, build.wantSHA)
			}
		})
	}

	replaced := withMain(installed, modulePath, "v1.0.0-alpha.99")
	replaced.Main.Replace = &debug.Module{Path: "../costroid"}
	if gotVersion, _ := moduleBuild("dev", "", replaced); gotVersion != "dev" {
		t.Errorf("a replaced main module reads as release %q", gotVersion)
	}
}

func TestInitAcceptsAReleaseModuleBuild(t *testing.T) {
	version, commit := moduleBuild("dev", "", goInstallBuild(t))
	var asked []string
	build := buildInfo{version: version, commit: commit, tagCommit: func(tag string) (string, error) {
		asked = append(asked, tag)
		return testCommit, nil
	}}
	pinned := "pinned to v" + goInstallRelease + " at " + testCommit + ", the commit its tag names on " + repositoryURL

	for host, want := range map[string][]string{
		githubHost: {costroidActionPath + "@" + testCommit + " # v" + goInstallRelease},
		gitlabHost: {"COSTROID_VERSION: " + goInstallRelease + "\n", "COSTROID_COMMIT: " + testCommit + "\n"},
	} {
		t.Run(host, func(t *testing.T) {
			asked = nil
			root := t.TempDir()
			code, stdout, stderr := runInit(t, root, build, costroid.Recipes, host, "--agent", "claude")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if !slices.Equal(asked, []string{"v" + goInstallRelease}) {
				t.Errorf("init asked for the tags %q", asked)
			}
			written := readFile(t, filepath.Join(root, filepath.FromSlash(initOut[host])))
			for _, line := range want {
				if !strings.Contains(written, line) {
					t.Errorf("%s holds no %q:\n%s", initOut[host], line, written)
				}
			}
			if !strings.Contains(stdout, "costroid: wrote "+initOut[host]+" from recipes/") || !strings.Contains(stdout, pinned+"\n") {
				t.Errorf("stdout does not say where the commit came from:\n%s", stdout)
			}
		})
	}

	refusals := []struct {
		name   string
		answer func(string) (string, error)
		want   string
	}{
		{"the tag cannot be read", func(string) (string, error) { return "", errors.New("could not read the tag") },
			"this build of " + goInstallRelease + " records no commit, and could not read the tag, so init pins nothing: run a released costroid, or name the commit to pin with --action-ref <40 hex>"},
		{"the tag names no commit", func(string) (string, error) { return "v1", nil }, `its tag names "v1", which is not a commit SHA`},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			root := t.TempDir()
			refused := buildInfo{version: version, tagCommit: refusal.answer}
			code, stdout, stderr := runInit(t, root, refused, costroid.Recipes, "github", "--agent", "claude")
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, refusal.want) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if files := tree(t, root); len(files) != 0 {
				t.Errorf("init wrote %v", files)
			}
		})
	}
}

// TestInitReadsTheCommitATagNames runs git ls-remote on a repository with an
// annotated and a lightweight tag, as github.com answers it.
func TestInitReadsTheCommitATagNames(t *testing.T) {
	root := gitRepository(t)
	runGit(t, root, "tag", "--annotate", "--message", "Release", "v1.0.0-alpha.1")
	first := runGit(t, root, "rev-parse", "HEAD")
	runGit(t, root, "commit", "--quiet", "--allow-empty", "--message", "Next")
	runGit(t, root, "tag", "v1.0.0-alpha.10")
	second := runGit(t, root, "rev-parse", "HEAD")
	if object := runGit(t, root, "rev-parse", "v1.0.0-alpha.1"); object == first {
		t.Fatal("the annotated tag names the commit itself, so this test proves nothing about peeling")
	}

	for tag, want := range map[string]string{"v1.0.0-alpha.1": first, "v1.0.0-alpha.10": second} {
		got, err := lsRemoteTagCommit(root, tag)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", tag, got, err, want)
		}
	}
	if got, err := lsRemoteTagCommit(root, "v1.0.0-alpha.2"); err == nil || !strings.Contains(err.Error(), root+" has no tag v1.0.0-alpha.2") {
		t.Errorf("a missing tag: %q, %v", got, err)
	}
	missing := filepath.Join(root, "missing")
	if _, err := lsRemoteTagCommit(missing, "v1.0.0-alpha.1"); err == nil || !strings.Contains(err.Error(), "could not read the tag v1.0.0-alpha.1 from "+missing+": fatal:") {
		t.Errorf("a repository that does not exist: %v", err)
	}
	if _, err := tagCommit(repositoryURL, "v1", "abc\trefs/tags/v1\n"); err == nil || !strings.Contains(err.Error(), "is not a commit SHA") {
		t.Errorf("a short object name: %v", err)
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// modulePath is the module the go command records as the main module of
	// every build of costroid.
	modulePath = "github.com/Costroid/costroid"
	// repositoryURL is where init reads the commit a release's tag names.
	repositoryURL = "https://" + modulePath
	// tagReadTimeout bounds git ls-remote, so init never waits on a network
	// that does not answer.
	tagReadTimeout = 30 * time.Second
)

// moduleRelease is a version the go command records for a build of a release
// tag: go install at that tag, or go build in a clean checkout of it. A
// pseudo-version always holds a hyphen inside its pre-release part, and a
// checkout with changes ends in +dirty, so neither matches.
var moduleRelease = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		version, commit = moduleBuild(version, commit, info)
	}
}

// moduleBuild returns this build's version and commit. A release build has
// both from the linker and keeps them. Any other build takes its version from
// the module's build information when that names a release tag, and its
// commit from the checkout it was built in. go install builds from the
// module's source, which records no commit, so init reads it from the tag.
func moduleBuild(linkedVersion, linkedCommit string, info *debug.BuildInfo) (string, string) {
	if linkedVersion != "dev" || info.Main.Path != modulePath || info.Main.Replace != nil || !moduleRelease.MatchString(info.Main.Version) {
		return linkedVersion, linkedCommit
	}
	revision := ""
	for _, setting := range info.Settings {
		switch {
		case setting.Key == "vcs.modified" && setting.Value != "false":
			return linkedVersion, linkedCommit
		case setting.Key == "vcs.revision":
			revision = setting.Value
		}
	}
	return strings.TrimPrefix(info.Main.Version, "v"), revision
}

// remoteTagCommit reads the commit tag names on github.com, with the same git
// ls-remote the action reads its release with.
func remoteTagCommit(tag string) (string, error) {
	return lsRemoteTagCommit(repositoryURL, tag)
}

// lsRemoteTagCommit reads the commit tag names in the repository at url.
func lsRemoteTagCommit(url, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tagReadTimeout)
	defer cancel()
	ref := "refs/tags/" + tag
	command := exec.CommandContext(ctx, "git", "ls-remote", url, ref, ref+"^{}")
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if said := firstLineOf(string(exit.Stderr)); said != "" {
			return "", fmt.Errorf("could not read the tag %s from %s: %s", tag, url, said)
		}
	}
	if err != nil {
		return "", fmt.Errorf("could not read the tag %s from %s: %w", tag, url, err)
	}
	return tagCommit(url, tag, string(output))
}

// tagCommit reads the commit tag names from git ls-remote's lines. An
// annotated tag's own line names the tag object, and the line for its peeled
// ref names the commit. A lightweight tag's line names the commit.
func tagCommit(url, tag, listing string) (string, error) {
	named := map[string]string{}
	for line := range strings.Lines(listing) {
		if object, ref, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			named[ref] = object
		}
	}
	ref := "refs/tags/" + tag
	object, ok := named[ref+"^{}"]
	if !ok {
		object, ok = named[ref]
	}
	if !ok {
		return "", fmt.Errorf("%s has no tag %s", url, tag)
	}
	if !fullSHA.MatchString(object) {
		return "", fmt.Errorf("the tag %s on %s names %q, which is not a commit SHA", tag, url, object)
	}
	return object, nil
}

package ghaw

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// uploadLists holds, for each gh-aw release named, the path list of the usage
// artifact's upload step, copied line for line from
// buildUsageArtifactUploadStep in pkg/workflow/notify_comment.go of
// github/gh-aw at that tag, read 2026-11-08:
// https://github.com/github/gh-aw/blob/v1.0.1/pkg/workflow/notify_comment.go
// and https://github.com/github/gh-aw/blob/v1.2.1/pkg/workflow/notify_comment.go.
// v1.2.0's list is v1.2.1's.
var uploadLists = filepath.Join("testdata", "upload-lists")

// uploadRoot is the directory each path of the upload step starts with, and
// the artifact's root.
const uploadRoot = "/tmp/gh-aw/usage/"

// recordedRun is every file the usage artifact of GitHub Actions run 42469416753
// held, gh-aw v1.0.1 running Codex 0.164.0, as gh run download wrote it on
// 2026-11-08.
var recordedRun = []string{
	"activity/summary.json",
	"agent/execution.json",
	"agent/harness_retry.json",
	"agent/token_usage.jsonl",
	"agent_usage.json",
	"aw_session.jsonl",
	"detection/token_usage.jsonl",
	"github_rate_limits.jsonl",
	"graders/grader_manifest.json",
	"graders/grader_results.json",
}

// uploaded is the artifact paths of one release's upload step.
func uploaded(t *testing.T, release string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(uploadLists, release+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		path, ok := strings.CutPrefix(line, uploadRoot)
		if !ok {
			t.Fatalf("%s lists %q outside %s", release, line, uploadRoot)
		}
		paths = append(paths, path)
	}
	return paths
}

// artifactOf is a usage artifact that holds every one of paths, with one
// agent record that reads and an aw_info.json that states no cap.
func artifactOf(paths []string) fstest.MapFS {
	artifact := fstest.MapFS{}
	for _, path := range paths {
		artifact[path] = &fstest.MapFile{Data: []byte("{}\n")}
	}
	artifact[agentUsage] = &fstest.MapFile{Data: []byte(`{"_schema":"token-usage/v0.29.0","timestamp":"2026-11-08T10:00:00Z","event":"token_usage",` +
		`"request_id":"r1","provider":"openai","model":"gpt-6.1-sol","status":200,"input_tokens":1,"output_tokens":1,` +
		`"cache_read_tokens":0,"cache_write_tokens":0,"duration_ms":1}` + "\n")}
	artifact[detectionUsage] = &fstest.MapFile{}
	return artifact
}

// TestGhAwAdmitsEveryFileGhAwWrites holds the list of what the usage
// artifact may hold to what gh-aw's upload step names, file for file, in the
// releases the refusal names. The artifact of each release with every file it
// can upload is read, and so is the recorded run's. A file the list does not
// name is refused, and the refusal names those releases.
func TestGhAwAdmitsEveryFileGhAwWrites(t *testing.T) {
	older, newer := uploaded(t, "v1.0.1"), uploaded(t, "v1.2.1")
	written := slices.Compact(slices.Sorted(slices.Values(slices.Concat(older, newer))))
	if listed := slices.Sorted(slices.Values(knownFiles)); !slices.Equal(listed, written) {
		t.Errorf("the list is not what gh-aw's upload step names:\n list %q\nupload %q", listed, written)
	}
	if knownReleases != "gh-aw v1.0.1 to v1.2.1" {
		t.Errorf("the refusal names %q, and the lists were read from v1.0.1 and v1.2.1", knownReleases)
	}
	for name, paths := range map[string][]string{"v1.0.1": older, "v1.2.1": newer, "the recorded run": recordedRun} {
		if _, err := ReadUsage(artifactOf(paths), nil); err != nil {
			t.Errorf("the usage artifact of %s was refused: %v", name, err)
		}
	}
	for _, path := range written {
		extra := artifactOf(written)
		extra[path+".sh"] = &fstest.MapFile{Data: []byte("#!/bin/sh\n")}
		_, err := ReadUsage(extra, nil)
		if want := "holds " + path + ".sh, which gh-aw v1.0.1 to v1.2.1 do not write there"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("an artifact beside %s: %v, want %q", path, err, want)
		}
	}
	info := fstest.MapFS{runInfo: &fstest.MapFile{Data: []byte(`{"model_config":{"max_ai_credits":10}}`)}}
	if _, err := ReadUsage(artifactOf(recordedRun), info); err != nil {
		t.Errorf("an info artifact of aw_info.json alone was refused: %v", err)
	}
	info["prompt.txt"] = &fstest.MapFile{Data: []byte("text")}
	if _, err := ReadUsage(artifactOf(recordedRun), info); err == nil || !strings.Contains(err.Error(), "the info artifact holds prompt.txt, which gh-aw v1.0.1 to v1.2.1 do not write there") {
		t.Errorf("an info artifact with a second file: %v", err)
	}
}

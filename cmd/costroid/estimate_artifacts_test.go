package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// gitLabArtifacts are the paths the jobs of a GitLab CI file keep, by job.
type gitLabArtifacts map[string]struct {
	Artifacts struct {
		Paths []string `yaml:"paths"`
	} `yaml:"artifacts"`
}

// TestGitLabTemplateKeepsTheEstimatesNote holds the GitLab template's agent
// jobs to the artifacts docs/infrastructure.md says they keep: the rows, the
// run's object and the note of an estimate that was not there, which the
// report reads to say so.
func TestGitLabTemplateKeepsTheEstimatesNote(t *testing.T) {
	var documented []string
	for _, block := range docsCodeBlock.FindAllStringSubmatch(readFile(t, filepath.Join(repositoryRoot, "docs", "infrastructure.md")), -1) {
		if !strings.Contains(block[1], "artifacts:") {
			continue
		}
		var jobs gitLabArtifacts
		if err := yaml.Unmarshal([]byte(block[1]), &jobs); err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			documented = append(documented, job.Artifacts.Paths...)
		}
	}
	if !slices.Contains(documented, "costroid-rows.infrastructure.json") {
		t.Fatalf("docs/infrastructure.md keeps no note as an artifact: %v", documented)
	}
	var template gitLabArtifacts
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(repositoryRoot, "recipes", "gitlab-ci.yml"))), &template); err != nil {
		t.Fatal(err)
	}
	kept := template[".costroid-agent"].Artifacts.Paths
	for _, path := range documented {
		if !slices.Contains(kept, path) {
			t.Errorf("the template's agent jobs keep %v, and docs/infrastructure.md says they keep %s", kept, path)
		}
	}
}

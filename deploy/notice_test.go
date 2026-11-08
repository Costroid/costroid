package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	dockerignore = "../.dockerignore"
	docDirectory = "/usr/share/doc/costroid/"
)

// shipped are the two files that travel with every build of costroid, read
// from the repository's root.
var shipped = []string{"NOTICE", "LICENSE"}

func TestImageCarriesNoticeAndLicense(t *testing.T) {
	for _, name := range shipped {
		if info, err := os.Stat(filepath.Join("..", name)); err != nil || info.Size() == 0 {
			t.Errorf("the repository's %s is missing or empty: %v", name, err)
		}
		if pattern := excludedBy(t, string(read(t, dockerignore)), name); pattern != "" {
			t.Errorf(".dockerignore leaves %s out of the build context by %q", name, pattern)
		}
	}
	copied := map[string]bool{}
	for _, sources := range finalStageCopies(string(read(t, dockerfile)), docDirectory) {
		for _, source := range sources {
			copied[source] = true
		}
	}
	for _, name := range shipped {
		if !copied[name] {
			t.Errorf("the image's final stage does not copy %s from the build context to %s", name, docDirectory)
		}
	}
}

// excludedBy returns the .dockerignore pattern that leaves name, a path in the
// build context, out of it, or "" when name is sent. The last pattern that
// matches decides, and a pattern that names a directory leaves out everything
// under it, as Docker reads the file.
func excludedBy(t *testing.T, ignore, name string) string {
	t.Helper()
	decided := ""
	for _, line := range strings.Split(ignore, "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		negated := strings.HasPrefix(pattern, "!")
		pattern = path.Clean(strings.TrimPrefix(strings.TrimPrefix(pattern, "!"), "/"))
		if strings.ContainsAny(pattern, `[\`) {
			t.Fatalf(".dockerignore pattern %q holds a character class or an escape, which this test does not read", line)
		}
		if dockerignorePattern(pattern).MatchString(name) {
			decided = line
			if negated {
				decided = ""
			}
		}
	}
	return decided
}

func dockerignorePattern(pattern string) *regexp.Regexp {
	var expression strings.Builder
	expression.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			expression.WriteString("(.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			expression.WriteString(".*")
			i++
		case pattern[i] == '*':
			expression.WriteString("[^/]*")
		case pattern[i] == '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	expression.WriteString("(/.*)?$")
	return regexp.MustCompile(expression.String())
}

// finalStageCopies returns the sources of each COPY in the Dockerfile's last
// stage that takes files from the build context into destination.
func finalStageCopies(dockerfileText, destination string) [][]string {
	var instructions []string
	for _, line := range strings.Split(strings.ReplaceAll(dockerfileText, "\\\n", " "), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "FROM ") {
			instructions = nil
		}
		instructions = append(instructions, line)
	}
	var copies [][]string
	for _, instruction := range instructions {
		fields := strings.Fields(instruction)
		if fields[0] != "COPY" {
			continue
		}
		var arguments []string
		fromStage := false
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "--from") {
				fromStage = true
			}
			if !strings.HasPrefix(field, "--") {
				arguments = append(arguments, field)
			}
		}
		if !fromStage && len(arguments) >= 2 && arguments[len(arguments)-1] == destination {
			copies = append(copies, arguments[:len(arguments)-1])
		}
	}
	return copies
}

type releaseStep struct {
	Name             string            `yaml:"name"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	WorkingDirectory string            `yaml:"working-directory"`
	With             map[string]string `yaml:"with"`
}

type releaseWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Steps []releaseStep `yaml:"steps"`
	} `yaml:"jobs"`
}

// TestReleaseUploadsNoticeAndLicense runs the release workflow's own shell
// steps on a stand-in build, with cosign and gh replaced by recorders, from the
// binaries the build writes to the files gh uploads.
func TestReleaseUploadsNoticeAndLicense(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(read(t, filepath.Join(workflows, "release.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	dist := filepath.Join(work, "dist")
	recorded := filepath.Join(work, "recorded")
	tools := filepath.Join(work, "tools")
	for _, directory := range []string{dist, tools} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range shipped {
		write(t, filepath.Join(work, name), read(t, filepath.Join("..", name)))
	}
	for _, name := range []string{"costroid-linux-amd64", "costroid-darwin-arm64", "costroid.spdx.json"} {
		write(t, filepath.Join(dist, name), []byte(name))
	}
	for _, tool := range []string{"cosign", "gh"} {
		write(t, filepath.Join(tools, tool), []byte("#!/usr/bin/env bash\necho \""+tool+" $*\" >>\""+recorded+"\"\n"))
		if err := os.Chmod(filepath.Join(tools, tool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(),
		"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"IMAGE="+workflow.Env["IMAGE"], "DIGEST=sha256:"+strings.Repeat("0", 64),
		"TAG=v1.0.0-alpha.99", "GH_REPO=Costroid/costroid", "GH_TOKEN=unused",
	)

	binaries := workflow.Jobs["binaries"].Steps
	built, uploaded := -1, -1
	for i, step := range binaries {
		if strings.Contains(step.Run, "go build") {
			built = i
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["name"] == "binaries" {
			uploaded = i
		}
	}
	if built < 0 || uploaded < built || binaries[uploaded].With["path"] != "dist" {
		t.Fatalf("the binaries job does not build and then upload dist: build step %d, upload step %d", built, uploaded)
	}
	for _, step := range binaries[built+1 : uploaded] {
		if step.Run != "" {
			runStep(t, step, work, env)
		}
	}
	for _, name := range shipped {
		if !bytes.Equal(read(t, filepath.Join(dist, name)), read(t, filepath.Join("..", name))) {
			t.Errorf("the binaries job hands over a %s that is not the repository's", name)
		}
	}

	var sum, sign, publish *releaseStep
	var subjects string
	for i, step := range workflow.Jobs["sign"].Steps {
		switch {
		case strings.Contains(step.Run, ">SHA256SUMS"):
			sum = &workflow.Jobs["sign"].Steps[i]
		case strings.Contains(step.Run, "cosign sign-blob"):
			sign = &workflow.Jobs["sign"].Steps[i]
		case strings.HasPrefix(step.Uses, "actions/attest-build-provenance@") && step.With["subject-path"] != "":
			subjects = step.With["subject-path"]
		}
	}
	for i, step := range workflow.Jobs["publish"].Steps {
		if strings.Contains(step.Run, "gh release create") {
			publish = &workflow.Jobs["publish"].Steps[i]
		}
	}
	if sum == nil || sign == nil || publish == nil || subjects == "" {
		t.Fatal("release.yml lost the step that sums, signs, attests or publishes the files")
	}
	runStep(t, *sum, work, env)
	sums := string(read(t, filepath.Join(dist, "SHA256SUMS")))
	for _, name := range shipped {
		if !strings.Contains(sums, sumOf(read(t, filepath.Join("..", name)))+"  "+name+"\n") {
			t.Errorf("SHA256SUMS holds no line for the repository's %s:\n%s", name, sums)
		}
	}

	attested := map[string]bool{}
	for _, subject := range strings.Fields(subjects) {
		matches, err := filepath.Glob(filepath.Join(work, subject))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			attested[filepath.Base(match)] = true
		}
	}

	runStep(t, *sign, work, env)
	runStep(t, *publish, work, env)
	calls := string(read(t, recorded))
	for _, name := range shipped {
		if !attested[name] {
			t.Errorf("the provenance attestation does not name %s", name)
		}
		if !strings.Contains(calls, "cosign sign-blob --yes --bundle "+name+".sigstore.json "+name+"\n") {
			t.Errorf("cosign did not sign %s:\n%s", name, calls)
		}
		if !regexp.MustCompile(`(?m)^gh release create .* dist/` + name + ` `).MatchString(calls) {
			t.Errorf("gh release create does not upload %s:\n%s", name, calls)
		}
	}
}

func runStep(t *testing.T, step releaseStep, work string, env []string) {
	t.Helper()
	command := exec.Command("bash", "-e", "-o", "pipefail", "-c", step.Run)
	command.Dir = filepath.Join(work, step.WorkingDirectory)
	command.Env = env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("step %q: %v\n%s", step.Name, err, output)
	}
}

func write(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

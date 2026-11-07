package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	costroid "github.com/Costroid/costroid"
	"github.com/Costroid/costroid/internal/recipelint"
	"go.yaml.in/yaml/v3"
)

// fakeKey stands for a provider key. No four characters of it in a row
// appear in anything doctor prints of its own.
const fakeKey = "sk-Zq8xVw7Pk2Lm9Rt4Yb6Nc1Hd3Jf5Gs0Xe"

// fakeHost answers doctor's reads of the environment and of programs. A
// program it does not list is not on PATH, and a command it does not list
// fails.
type fakeHost struct {
	env      map[string]string
	programs []string
	outputs  map[string]string
	ran      []string
}

func (f *fakeHost) host() doctorHost {
	return doctorHost{
		getenv: func(name string) string { return f.env[name] },
		lookPath: func(name string) (string, error) {
			for _, program := range f.programs {
				if program == name {
					return "/fake/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		},
		output: func(_ context.Context, _, name string, args ...string) ([]byte, error) {
			command := strings.Join(append([]string{strings.TrimPrefix(name, "/fake/")}, args...), " ")
			f.ran = append(f.ran, command)
			out, found := f.outputs[command]
			if !found {
				return nil, errors.New(command + " exited 1: not answered")
			}
			return []byte(out), nil
		},
	}
}

// ghHost has gh and claude on PATH, and a repository whose secrets and
// variables gh lists as given.
func ghHost(secrets, variables string) *fakeHost {
	return &fakeHost{
		env:      map[string]string{},
		programs: []string{"gh", "claude"},
		outputs: map[string]string{
			"claude --version": "2.1.316 (Claude Code)\n",
			"gh secret list":   secrets,
			"gh variable list": variables,
			"gh repo view --json isPrivate --jq .isPrivate": "false\n",
		},
	}
}

func pinnedRecipe(t *testing.T, name, agent string) string {
	t.Helper()
	recipe, err := pinRecipe(name, shippedRecipe(t, name), pin{commit: testCommit, version: testRelease}, agent)
	if err != nil {
		t.Fatal(err)
	}
	return string(recipe)
}

func doctorRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		writeFile(t, filepath.Join(root, name), text)
	}
	return root
}

func runDoctor(t *testing.T, root string, host *fakeHost, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := doctor(context.Background(), root, releaseBuild, host.host(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func lineWith(output, prefix string) string {
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// printsAnyOf reports the first run of four characters of secret that text
// holds.
func printsAnyOf(text, secret string) string {
	for start := 0; start+4 <= len(secret); start++ {
		for end := start + 4; end <= len(secret); end++ {
			if strings.Contains(text, secret[start:end]) {
				return secret[start:end]
			}
		}
	}
	return ""
}

func TestDoctorNeverPrintsSecret(t *testing.T) {
	host := ghHost("ANTHROPIC_API_KEY\t2026-11-01T09:00:00Z\n",
		"COSTROID_INGEST_URL\t"+fakeKey+"\t2026-11-01T09:00:00Z\nCOSTROID_INGEST_AUDIENCE\t"+fakeKey+"\t2026-11-01T09:00:00Z\n")
	host.programs = append(host.programs, "codex")
	host.outputs["codex --version"] = "codex-cli 0.164.0-alpha.11.1\n"
	event := filepath.Join(t.TempDir(), "event.json")
	writeFile(t, event, `{"pull_request":{"head":{"repo":{"full_name":"acme/widgets"}}}}`)
	for _, name := range []string{"ANTHROPIC_API_KEY", "CODEX_API_KEY", "OPENAI_API_KEY", credentialVariable, "GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		host.env[name] = fakeKey
	}
	for name, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event,
		"GITHUB_REPOSITORY": "acme/widgets", "GITHUB_WORKFLOW_REF": "acme/widgets/.github/workflows/costroid.yml@refs/pull/7/merge",
		"GITHUB_JOB": "report", "ACTIONS_ID_TOKEN_REQUEST_URL": "https://token.example/",
	} {
		host.env[name] = value
	}
	root := doctorRepository(t, map[string]string{
		".github/workflows/costroid.yml": pinnedRecipe(t, "github-claude-code.yml", "claude"),
		".github/workflows/codex.yml":    pinnedRecipe(t, "github-codex.yml", "codex"),
	})

	code, stdout, stderr := runDoctor(t, root, host, "--ci")
	output := stdout + stderr
	if leaked := printsAnyOf(output, fakeKey); leaked != "" {
		t.Fatalf("doctor printed %q of the key:\n%s", leaked, output)
	}
	if code != exitRejected {
		t.Errorf("exit %d, want %d for the credential that reads like a key", code, exitRejected)
	}
	for _, want := range []string{
		"ok: ANTHROPIC_API_KEY is set in this shell",
		"ok: CODEX_API_KEY and OPENAI_API_KEY are set in this shell",
		"fail: " + credentialVariable + " reads like a key",
		"ok: the repository sets COSTROID_INGEST_URL and COSTROID_INGEST_AUDIENCE",
		"ok: job report of .github/workflows/costroid.yml declares permissions",
	} {
		if lineWith(stdout, want) == "" {
			t.Errorf("doctor read no key here, so it proved nothing: no line starts %q:\n%s", want, stdout)
		}
	}
}

func TestPrintsAnyOfFindsFourCharactersOfTheKey(t *testing.T) {
	for _, text := range []string{"key " + fakeKey, "prefix " + fakeKey[:4], "middle Pk2L end"} {
		if printsAnyOf(text, fakeKey) == "" {
			t.Errorf("found nothing in %q", text)
		}
	}
	if found := printsAnyOf("ok: costroid 1.0.0-alpha.10", fakeKey); found != "" {
		t.Errorf("found %q in a line that holds none of the key", found)
	}
}

func TestDoctorNamesEachMissingSecretWithItsCommand(t *testing.T) {
	root := doctorRepository(t, map[string]string{
		".github/workflows/costroid.yml": pinnedRecipe(t, "github-claude-code.yml", "claude"),
		".github/workflows/codex.yml":    pinnedRecipe(t, "github-codex-action.yml", "codex-action"),
		".github/workflows/deploy.yml":   "on: push\npermissions: {}\njobs:\n  deploy:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: ./deploy\n        env:\n          TOKEN: ${{ secrets.DEPLOY_TOKEN }}\n",
	})
	host := ghHost("ANTHROPIC_API_KEY\t2026-11-01T09:00:00Z\nUNRELATED\t2026-11-01T09:00:00Z\n", "")

	code, stdout, stderr := runDoctor(t, root, host)
	if code != exitRejected {
		t.Fatalf("exit %d, want %d for a missing secret: %s", code, exitRejected, stderr)
	}
	if want := "fail: gh secret list names no repository secret OPENAI_API_KEY: gh secret set OPENAI_API_KEY"; lineWith(stdout, want) != want {
		t.Errorf("no line %q:\n%s", want, stdout)
	}
	if want := "ok: the repository has the secret ANTHROPIC_API_KEY"; lineWith(stdout, want) != want {
		t.Errorf("no line %q:\n%s", want, stdout)
	}
	if strings.Contains(stdout, "DEPLOY_TOKEN") {
		t.Errorf("doctor judged a secret of a workflow that runs neither Costroid nor an agent:\n%s", stdout)
	}

	host.programs, host.ran = []string{"claude"}, nil
	code, stdout, _ = runDoctor(t, root, host)
	want := "warn: gh is not on PATH, so doctor did not list the repository's secrets: install gh, or check that the repository's settings hold ANTHROPIC_API_KEY and OPENAI_API_KEY"
	if code != 0 || lineWith(stdout, "warn: gh is not on PATH") != want {
		t.Errorf("without gh, exit %d, want 0 and %q:\n%s", code, want, stdout)
	}
	for _, command := range host.ran {
		if strings.HasPrefix(command, "gh ") {
			t.Errorf("doctor ran %q with gh off PATH", command)
		}
	}
}

func TestDoctorWarnsOnTwoAgentWorkflows(t *testing.T) {
	recipe := pinnedRecipe(t, "github-claude-code.yml", "claude")
	nightly := strings.Replace(existingReview, "on:\n  pull_request:\n    types: [opened, synchronize]\n", "on:\n  schedule:\n    - cron: \"0 3 * * *\"\n", 1)
	if nightly == existingReview {
		t.Fatal("the review workflow's trigger was not replaced")
	}
	for name, check := range map[string]struct {
		files map[string]string
		want  string
	}{
		"two on pull_request": {
			files: map[string]string{".github/workflows/costroid.yml": recipe, ".github/workflows/claude.yml": existingReview},
			want: "warn: .github/workflows/claude.yml and .github/workflows/costroid.yml each run an agent on pull_request, so each pull request pays for 2 runs: " +
				"keep one of them. To keep .github/workflows/claude.yml, delete the others and run costroid init github --agent claude-code-action --existing .github/workflows/claude.yml, which prints the patch that reports from it",
		},
		"one on pull_request, one nightly": {
			files: map[string]string{".github/workflows/costroid.yml": recipe, ".github/workflows/claude.yml": nightly},
		},
		"one on pull_request": {
			files: map[string]string{".github/workflows/costroid.yml": recipe},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, stdout, stderr := runDoctor(t, doctorRepository(t, check.files), ghHost("ANTHROPIC_API_KEY\n", ""))
			got := lineWith(stdout, "warn: .github/workflows/claude.yml and")
			if got != check.want {
				t.Errorf("got %q, want %q:\n%s%s", got, check.want, stdout, stderr)
			}
		})
	}
}

func TestDoctorPassesWhatInitWrites(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := runInit(t, root, releaseBuild, costroid.Recipes, "github", "--agent", "claude"); code != 0 {
		t.Fatalf("init exited %d: %s", code, stderr)
	}
	code, stdout, stderr := runDoctor(t, root, ghHost("ANTHROPIC_API_KEY\n", ""))
	if code != 0 || strings.Contains(stdout, "warn: ") || strings.Contains(stdout, "fail: ") {
		t.Fatalf("exit %d on what init wrote:\n%s%s", code, stdout, stderr)
	}
	for _, want := range []string{
		"ok: costroid " + testRelease,
		"ok: costroid.yml: valid",
		"ok: .github/workflows/costroid.yml: costroid lint finds nothing high or medium",
		"ok: .github/workflows/costroid.yml:" + firstPinLine(t, readFile(t, filepath.Join(root, ".github/workflows/costroid.yml"))) + ": Costroid/costroid/action pinned to the commit of " + testRelease,
		"ok: claude 2.1.316 is on PATH",
		"ok: the repository is public",
		"costroid: doctor: 0 fail, 0 warn, ",
	} {
		if lineWith(stdout, want) == "" {
			t.Errorf("no line starts %q:\n%s", want, stdout)
		}
	}
}

// firstPinLine is the line number of the first call of Costroid's action.
func firstPinLine(t *testing.T, workflow string) string {
	t.Helper()
	for index, line := range strings.Split(workflow, "\n") {
		if strings.Contains(line, costroidActionPath+"@") {
			return strconv.Itoa(index + 1)
		}
	}
	t.Fatal("the workflow calls no Costroid action")
	return ""
}

func TestDoctorFailsAPinThatNamesNoRelease(t *testing.T) {
	recipe := pinnedRecipe(t, "github-claude-code.yml", "claude")
	at := ".github/workflows/costroid.yml:" + firstPinLine(t, recipe) + ": Costroid/costroid/action@"
	for name, check := range map[string]struct{ ref, want string }{
		"a tag":      {"v1.0.0-alpha.10", "fail: " + at + "v1.0.0-alpha.10 is not a full commit SHA: "},
		"a branch":   {"main", "fail: " + at + "main is not a full commit SHA: "},
		"zero":       {zeroCommit, "fail: " + at + zeroCommit + " is the zero SHA, which names no release: "},
		"no comment": {testCommit, "warn: " + at + testCommit + " records no release in its comment: "},
	} {
		t.Run(name, func(t *testing.T) {
			changed := strings.Replace(recipe, "@"+testCommit+" # v"+testRelease, "@"+check.ref, 1)
			if check.ref == testCommit {
				changed = strings.Replace(recipe, " # v"+testRelease, "", 1)
			}
			if changed == recipe {
				t.Fatal("the pin was not replaced")
			}
			_, stdout, _ := runDoctor(t, doctorRepository(t, map[string]string{".github/workflows/costroid.yml": changed}), ghHost("ANTHROPIC_API_KEY\n", ""))
			if lineWith(stdout, check.want) == "" {
				t.Errorf("no line starts %q:\n%s", check.want, stdout)
			}
		})
	}
}

const zeroCommit = "0000000000000000000000000000000000000000"

func TestDoctorStatesWhatAnEmptyRepositoryLacks(t *testing.T) {
	host := ghHost("", "")
	host.programs = nil
	code, stdout, stderr := runDoctor(t, t.TempDir(), host)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"ok: costroid.yml: no file, so the defaults apply",
		"warn: no workflow in .github/workflows calls Costroid's action: run costroid init github --agent claude",
		"warn: claude is not on PATH, so costroid run cannot start it on this machine, though CI installs its own: install it with npm install --global @anthropic-ai/claude-code@2.1.316",
		"ok: ANTHROPIC_API_KEY is not set in this shell",
	} {
		if lineWith(stdout, want) == "" {
			t.Errorf("no line starts %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorFailsACostroidYmlTheSchemaRefuses(t *testing.T) {
	root := doctorRepository(t, map[string]string{configFile: "comment:\n  show_cost: maybe\n"})
	code, stdout, _ := runDoctor(t, root, ghHost("", ""))
	if code != exitRejected || lineWith(stdout, "fail: costroid.yml: ") == "" {
		t.Fatalf("exit %d, want %d and a fail line for costroid.yml:\n%s", code, exitRejected, stdout)
	}
}

func TestDoctorWarnsOnHardenRunnerInAPrivateRepository(t *testing.T) {
	root := doctorRepository(t, map[string]string{".github/workflows/costroid.yml": pinnedRecipe(t, "github-claude-code.yml", "claude")})
	host := ghHost("ANTHROPIC_API_KEY\n", "COSTROID_INGEST_URL\thttps://ledger.example/v1/rows\t2026-11-01T09:00:00Z\n")
	host.outputs["gh repo view --json isPrivate --jq .isPrivate"] = "true\n"
	_, stdout, _ := runDoctor(t, root, host)
	for _, want := range []string{
		"ok: the repository is private, so the comment always shows amounts",
		"warn: .github/workflows/costroid.yml runs step-security/harden-runner, which needs a paid plan on a private repository: ",
		"warn: the repository sets COSTROID_INGEST_URL and not COSTROID_INGEST_AUDIENCE: ",
	} {
		if lineWith(stdout, want) == "" {
			t.Errorf("no line starts %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorInCIReadsTheEventAndTheJob(t *testing.T) {
	root := doctorRepository(t, map[string]string{".github/workflows/costroid.yml": pinnedRecipe(t, "github-claude-code.yml", "claude")})
	event := filepath.Join(t.TempDir(), "event.json")
	writeFile(t, event, `{"pull_request":{"head":{"repo":{"full_name":"someone/widgets"}}}}`)
	host := ghHost("ANTHROPIC_API_KEY\n", "")
	host.env = map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "issue_comment", "GITHUB_EVENT_PATH": event,
		"GITHUB_REPOSITORY": "acme/widgets", "GITHUB_WORKFLOW_REF": "acme/widgets/.github/workflows/costroid.yml@refs/pull/7/merge",
		"GITHUB_JOB": "agent",
	}
	_, stdout, _ := runDoctor(t, root, host, "--ci")
	for _, want := range []string{
		"warn: the event is issue_comment, and costroid report posts only on pull_request: ",
		"warn: the pull request's head is the fork someone/widgets, which receives no secrets and cannot post the check: add recipes/github-follow-up.yml, which posts it from acme/widgets",
		"ok: this job does not hold id-token: write",
		"ok: job agent of .github/workflows/costroid.yml declares permissions contents: read",
	} {
		if lineWith(stdout, want) == "" {
			t.Errorf("no line starts %q:\n%s", want, stdout)
		}
	}

	_, stdout, _ = runDoctor(t, root, host)
	if strings.Contains(stdout, "the event is") {
		t.Errorf("doctor without --ci read the job:\n%s", stdout)
	}
}

func TestDoctorInCIRefusesOutsideAJob(t *testing.T) {
	code, stdout, stderr := runDoctor(t, t.TempDir(), ghHost("", ""), "--ci")
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "costroid: doctor: --ci runs as a step of a GitHub Actions job") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestDoctorHelpRunsNoCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := doctorCommand(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 || stderr.String() != doctorHelp || stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

var (
	markdownYAML  = regexp.MustCompile("(?s)```yaml\n(.*?)```")
	markdownShell = regexp.MustCompile("(?s)```sh\n(.*?)```")
	placeholder   = regexp.MustCompile(`<[a-z][a-z -]*>|__RELEASE_[A-Z_]*`)
	listingPin    = regexp.MustCompile(`Costroid/costroid(?:/action|-action)@(\S+)(?: # (\S+))?`)
)

const repositoryRoot = "../.."

// listingFor writes the listing for tag the way a release does: through
// scripts/marketplace.sh, in a scratch checkout that holds the script and
// marketplace/ with the tag on its one commit. It returns the README the
// script wrote and the tag's commit.
func listingFor(t *testing.T, tag string) (string, string) {
	t.Helper()
	checkout, home := filepath.Join(t.TempDir(), "costroid"), t.TempDir()
	for _, name := range []string{"scripts/marketplace.sh", "marketplace/README.md", "marketplace/action.yml", "marketplace/LICENSE", "marketplace/NOTICE"} {
		writeFile(t, filepath.Join(checkout, name), readFile(t, filepath.Join(repositoryRoot, name)))
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", checkout}, args...)...)
		command.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("add", ".")
	git("commit", "--quiet", "--message", "Add the listing")
	git("tag", "--annotate", "--message", tag, tag)
	out := filepath.Join(t.TempDir(), "costroid-action")
	command := exec.Command("bash", filepath.Join(checkout, "scripts", "marketplace.sh"), tag, out)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("scripts/marketplace.sh %s: %v: %s", tag, err, output)
	}
	return readFile(t, filepath.Join(out, "README.md")), git("rev-parse", "HEAD")
}

func TestListingHasNoPlaceholder(t *testing.T) {
	if source := readFile(t, filepath.Join(repositoryRoot, "marketplace", "README.md")); strings.Contains(source, "<commit>") {
		t.Error("marketplace/README.md asks the reader to fill in <commit>")
	}
	tag := "v" + testRelease
	listing, tagCommit := listingFor(t, tag)
	if found := placeholder.FindAllString(listing, -1); len(found) > 0 {
		t.Errorf("the listing for %s keeps placeholders: %v", tag, found)
	}
	pins := listingPin.FindAllStringSubmatch(listing, -1)
	if len(pins) == 0 {
		t.Fatal("the listing pins no Costroid action")
	}
	for _, pin := range pins {
		if pin[1] != tagCommit || pin[2] != tag {
			t.Errorf("the listing pins %q, want %s # %s", pin[0], tagCommit, tag)
		}
	}
}

func TestListingRecipeLints(t *testing.T) {
	listing, tagCommit := listingFor(t, "v"+testRelease)
	release := pin{commit: tagCommit, version: testRelease}
	blocks := markdownYAML.FindAllStringSubmatch(listing, -1)
	if len(blocks) != 3 {
		t.Fatalf("the listing has %d YAML blocks, want the recipe and the two blocks", len(blocks))
	}
	recipe := blocks[0][1]
	if report := recipelint.LintBytes("listing.yml", []byte(recipe)); len(report.Findings) > 0 {
		t.Errorf("the listing's recipe breaks a rule of lint-recipes: %v", report.Findings)
	}
	if report := recipelint.LintCustomer("listing.yml", []byte(recipe)); len(report.Findings) > 0 {
		t.Errorf("costroid lint finds %v in the listing's recipe", report.Findings)
	}
	if want := pinnedRecipeAt(t, "github-claude-code.yml", release); recipe != want {
		t.Errorf("the listing's recipe is not recipes/github-claude-code.yml pinned to the tag:\n%s", recipe)
	}
	for index, want := range []string{
		strings.Join(readStep(0, 2, addedStepID, release), "\n") + "\n",
		strings.Join(reportJob(0, 2, "report", "review", release), "\n") + "\n",
	} {
		if got := blocks[index+1][1]; got != want {
			t.Errorf("block %d is not what costroid init --existing adds:\n%s\nwant:\n%s", index+1, got, want)
		}
	}
}

func pinnedRecipeAt(t *testing.T, name string, release pin) string {
	t.Helper()
	recipe, err := pinRecipe(name, shippedRecipe(t, name), release, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(recipe)
}

func TestListingNamesEveryOutputOfTheAction(t *testing.T) {
	var action struct {
		Outputs map[string]any `yaml:"outputs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(repositoryRoot, "action", "action.yml"))), &action); err != nil {
		t.Fatal(err)
	}
	_, outputs, _ := strings.Cut(readFile(t, filepath.Join(repositoryRoot, "marketplace", "README.md")), "\n## Outputs\n")
	outputs, _, _ = strings.Cut(outputs, "\n## ")
	if len(action.Outputs) == 0 {
		t.Fatal("action.yml declares no outputs")
	}
	for name := range action.Outputs {
		if !strings.Contains(outputs, "`"+name+"`") {
			t.Errorf("the listing's Outputs section does not name %s", name)
		}
	}
}

func TestReadmeAndListingQuoteTheDemoCommentsFirstLine(t *testing.T) {
	body, err := demoComment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := lineWith(body, "This run cost ")
	if first == "" {
		t.Fatalf("the demo's comment states no cost line:\n%s", body)
	}
	for _, page := range []string{"README.md", "marketplace/README.md"} {
		if !strings.Contains(readFile(t, filepath.Join(repositoryRoot, page)), "\n> "+first+"\n") {
			t.Errorf("%s does not quote the comment's first line %q", page, first)
		}
	}
}

// TestReadmeTryItBlockIsWhatInitPrints runs the README's first command and
// holds the rest of its block to the commands init prints after it.
func TestReadmeTryItBlockIsWhatInitPrints(t *testing.T) {
	readme := readFile(t, filepath.Join(repositoryRoot, "README.md"))
	_, section, _ := strings.Cut(readme, "\n## Try it on a pull request\n")
	section, _, _ = strings.Cut(section, "\n## ")
	block := markdownShell.FindStringSubmatch(section)
	if block == nil {
		t.Fatal("the README's Try it section has no shell block")
	}
	lines := strings.Split(strings.TrimSuffix(block[1], "\n"), "\n")
	args := strings.Fields(lines[0])
	if len(args) < 2 || args[0] != "costroid" || args[1] != "init" {
		t.Fatalf("the block opens with %q, not costroid init", lines[0])
	}
	code, stdout, stderr := runInit(t, t.TempDir(), releaseBuild, costroid.Recipes, args[2:]...)
	if code != 0 {
		t.Fatalf("%s exited %d: %s", lines[0], code, stderr)
	}
	_, printed, found := strings.Cut(stdout, "These commands are left, and init ran none of them:\n")
	if !found {
		t.Fatalf("init printed no commands:\n%s", stdout)
	}
	var want []string
	for line := range strings.SplitSeq(strings.TrimSuffix(printed, "\n"), "\n") {
		want = append(want, strings.TrimSpace(line))
	}
	if !slices.Equal(lines[1:], want) {
		t.Errorf("the README's block runs %q after init, and init prints %q", lines[1:], want)
	}
}

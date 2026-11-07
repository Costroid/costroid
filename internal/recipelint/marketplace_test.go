package recipelint

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	marketplaceDir    = "../../marketplace"
	marketplaceScript = "../../scripts/marketplace.sh"
	wrapperUses       = costroidAction + "@__RELEASE_COMMIT__"
)

// brandingColors and omittedIcons are from GitHub's metadata syntax page for
// branding, read on 2026-11-04.
var (
	brandingColors = []string{"white", "black", "yellow", "blue", "green", "orange", "red", "purple", "gray-dark"}
	omittedIcons   = []string{"coffee", "columns", "divide-circle", "divide-square", "divide", "frown", "hexagon", "key", "meh", "mouse-pointer", "smile", "tool", "x-octagon"}
)

func document(t *testing.T, source string) *yaml.Node {
	t.Helper()
	documents, err := parse([]byte(source))
	if err != nil || len(documents) != 1 {
		t.Fatalf("want one YAML document, got %d: %v", len(documents), err)
	}
	return documents[0]
}

// scalars reads a mapping of scalars, so two can be compared whole.
func scalars(node *yaml.Node) map[string]string {
	values := map[string]string{}
	for _, item := range entries(node) {
		values[item.key.Value] = item.value.Value
	}
	return values
}

// actionInput is what a caller sees of one input.
type actionInput struct {
	Description, Default, Required string
	HasDefault                     bool
}

func actionInputs(root *yaml.Node) (map[string]actionInput, []string) {
	inputs, names := map[string]actionInput{}, []string{}
	for _, item := range entries(field(root, "inputs")) {
		names = append(names, item.key.Value)
		inputs[item.key.Value] = actionInput{
			Description: scalarField(item.value, "description"),
			Default:     scalarField(item.value, "default"),
			Required:    scalarField(item.value, "required"),
			HasDefault:  field(item.value, "default") != nil,
		}
	}
	return inputs, names
}

// checkInputParity holds a wrapper to the action it calls: every input under
// the same name, description and default, none added, each passed through.
func checkInputParity(t *testing.T, action, wrapper string) []string {
	t.Helper()
	var problems []string
	wrapperRoot, actionRoot := document(t, wrapper), document(t, action)
	want, names := actionInputs(actionRoot)
	got, _ := actionInputs(wrapperRoot)
	for _, name := range names {
		input, found := got[name]
		switch {
		case !found:
			problems = append(problems, "the wrapper lacks the input "+name)
		case input.HasDefault != want[name].HasDefault || input.Default != want[name].Default:
			problems = append(problems, "the wrapper's default for "+name+" is "+input.Default+", not "+want[name].Default)
		case input != want[name]:
			problems = append(problems, "the wrapper describes "+name+" differently")
		}
	}
	for name := range got {
		if _, found := want[name]; !found {
			problems = append(problems, "the wrapper adds the input "+name)
		}
	}
	steps := sequence(field(field(wrapperRoot, "runs"), "steps"))
	if scalarField(field(wrapperRoot, "runs"), "using") != "composite" || len(steps) != 1 {
		return append(problems, "the wrapper is not a composite action of one step")
	}
	uses := field(steps[0], "uses")
	if uses == nil || uses.Value != wrapperUses || lineComment(steps[0], uses) != "# __RELEASE_TAG__" {
		problems = append(problems, "the wrapper's step does not call "+wrapperUses+" # __RELEASE_TAG__")
	}
	forwarded := map[string]string{}
	for _, name := range names {
		forwarded[name] = "${{ inputs." + name + " }}"
	}
	if passed := scalars(field(steps[0], "with")); !reflect.DeepEqual(passed, forwarded) {
		problems = append(problems, "the wrapper's step does not pass each input through as itself")
	}
	return problems
}

func TestTheMarketplaceWrapperTakesEveryInputOfTheAction(t *testing.T) {
	action := read(t, filepath.Join(actionDir, "action.yml"))
	wrapper := read(t, filepath.Join(marketplaceDir, "action.yml"))
	if problems := checkInputParity(t, action, wrapper); len(problems) != 0 {
		t.Fatalf("marketplace/action.yml:\n%s", strings.Join(problems, "\n"))
	}
}

func TestTheInputParityCheckRefusesADriftedWrapper(t *testing.T) {
	action := read(t, filepath.Join(actionDir, "action.yml"))
	wrapper := read(t, filepath.Join(marketplaceDir, "action.yml"))
	for name, change := range map[string][2]string{
		"a missing input":   {"  agent:\n    description: claude or codex.\n    default: claude\n", ""},
		"another default":   {"    description: claude or codex.\n    default: claude\n", "    description: claude or codex.\n    default: codex\n"},
		"an added input":    {"\noutputs:\n", "  extra:\n    description: More.\n    default: \"\"\n\noutputs:\n"},
		"a dropped forward": {"        agent: ${{ inputs.agent }}\n", ""},
		"another pin":       {"@__RELEASE_COMMIT__", "@main"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(wrapper, change[0]) {
				t.Fatalf("the wrapper has no %q to change", change[0])
			}
			if problems := checkInputParity(t, action, strings.Replace(wrapper, change[0], change[1], 1)); len(problems) == 0 {
				t.Fatal("the check passed a drifted wrapper")
			}
		})
	}
}

// actionOutput is what a caller sees of one output, and where it comes from.
type actionOutput struct {
	Description, Value string
}

func actionOutputs(root *yaml.Node) (map[string]actionOutput, []string) {
	outputs, names := map[string]actionOutput{}, []string{}
	for _, item := range entries(field(root, "outputs")) {
		names = append(names, item.key.Value)
		outputs[item.key.Value] = actionOutput{
			Description: scalarField(item.value, "description"),
			Value:       scalarField(item.value, "value"),
		}
	}
	return outputs, names
}

// checkOutputParity holds a wrapper's outputs to the action's: every output
// under the same name and description, none added, each read from the one
// step that calls the action.
func checkOutputParity(t *testing.T, action, wrapper string) []string {
	t.Helper()
	var problems []string
	wrapperRoot := document(t, wrapper)
	want, names := actionOutputs(document(t, action))
	got, _ := actionOutputs(wrapperRoot)
	if len(names) == 0 {
		return append(problems, "the action declares no outputs")
	}
	steps := sequence(field(field(wrapperRoot, "runs"), "steps"))
	if len(steps) != 1 || scalarField(steps[0], "id") == "" {
		return append(problems, "the wrapper's one step has no id to read outputs from")
	}
	id := scalarField(steps[0], "id")
	for _, name := range names {
		output, found := got[name]
		switch {
		case !found:
			problems = append(problems, "the wrapper lacks the output "+name)
		case output.Description != want[name].Description:
			problems = append(problems, "the wrapper describes the output "+name+" differently")
		case output.Value != "${{ steps."+id+".outputs."+name+" }}":
			problems = append(problems, "the wrapper's output "+name+" is not the action's "+name)
		}
	}
	for name := range got {
		if _, found := want[name]; !found {
			problems = append(problems, "the wrapper adds the output "+name)
		}
	}
	return problems
}

func TestTheMarketplaceWrapperGivesEveryOutputOfTheAction(t *testing.T) {
	action := read(t, filepath.Join(actionDir, "action.yml"))
	wrapper := read(t, filepath.Join(marketplaceDir, "action.yml"))
	if problems := checkOutputParity(t, action, wrapper); len(problems) != 0 {
		t.Fatalf("marketplace/action.yml:\n%s", strings.Join(problems, "\n"))
	}
}

func TestTheOutputParityCheckRefusesADriftedWrapper(t *testing.T) {
	action := read(t, filepath.Join(actionDir, "action.yml"))
	wrapper := read(t, filepath.Join(marketplaceDir, "action.yml"))
	for name, change := range map[string][2]string{
		"a missing output":      {"  tokens_output:\n    description: Output tokens. Empty when cost is.\n    value: ${{ steps.costroid.outputs.tokens_output }}\n", ""},
		"another description":   {"    description: Output tokens. Empty when cost is.\n", "    description: Output tokens.\n"},
		"an added output":       {"\nruns:\n", "  extra:\n    description: More.\n    value: ${{ steps.costroid.outputs.extra }}\n\nruns:\n"},
		"a crossed value":       {"value: ${{ steps.costroid.outputs.cost }}", "value: ${{ steps.costroid.outputs.currency }}"},
		"a step without its id": {"    - id: costroid\n      uses:", "    - uses:"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(wrapper, change[0]) {
				t.Fatalf("the wrapper has no %q to change", change[0])
			}
			if problems := checkOutputParity(t, action, strings.Replace(wrapper, change[0], change[1], 1)); len(problems) == 0 {
				t.Fatal("the check passed a drifted wrapper")
			}
		})
	}
}

func TestTheMarketplaceWrapperCarriesItsListing(t *testing.T) {
	root := document(t, read(t, filepath.Join(marketplaceDir, "action.yml")))
	if got := scalarField(root, "name"); got != "Costroid cost check" {
		t.Errorf("name: %q", got)
	}
	if got := scalarField(root, "author"); got != "Costroid" {
		t.Errorf("author: %q", got)
	}
	branding := field(root, "branding")
	if color := scalarField(branding, "color"); !slices.Contains(brandingColors, color) {
		t.Errorf("GitHub accepts no branding colour %q", color)
	}
	if icon := scalarField(branding, "icon"); icon == "" || slices.Contains(omittedIcons, icon) {
		t.Errorf("GitHub accepts no branding icon %q", icon)
	}
}

// A listed repository holds one action and the files it needs, so marketplace/
// carries no workflow, and its licence and notice are the root's own.
func TestTheMarketplaceFolderHoldsTheListingAndNothingElse(t *testing.T) {
	files, err := os.ReadDir(marketplaceDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name())
	}
	if want := []string{"LICENSE", "NOTICE", "README.md", "action.yml"}; !slices.Equal(names, want) {
		t.Fatalf("marketplace/ holds %v, want %v", names, want)
	}
	for _, name := range []string{"LICENSE", "NOTICE"} {
		if read(t, filepath.Join(marketplaceDir, name)) != read(t, filepath.Join(repositoryRoot, name)) {
			t.Errorf("marketplace/%s is not the root's %s", name, name)
		}
	}
}

// listingRepository is a scratch checkout holding the script and marketplace/,
// with one tagged commit, so the script reads a tag no real release has.
type listingRepository struct {
	t          *testing.T
	root, home string
	commit     string
}

func newListingRepository(t *testing.T) *listingRepository {
	t.Helper()
	r := &listingRepository{t: t, root: filepath.Join(t.TempDir(), "costroid"), home: t.TempDir()}
	for _, name := range []string{"scripts", "marketplace"} {
		if err := os.MkdirAll(filepath.Join(r.root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(marketplaceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		write(t, filepath.Join(r.root, "marketplace", file.Name()), read(t, filepath.Join(marketplaceDir, file.Name())))
	}
	write(t, filepath.Join(r.root, "scripts", "marketplace.sh"), read(t, marketplaceScript))
	r.git(r.root, "init", "--quiet")
	r.git(r.root, "add", ".")
	r.git(r.root, "commit", "--quiet", "--message", "Add the listing")
	r.git(r.root, "tag", "--annotate", "--message", "v"+testVersion, "v"+testVersion)
	r.commit = strings.TrimSpace(r.git(r.root, "rev-parse", "HEAD"))
	return r
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *listingRepository) env() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + r.home, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
}

func (r *listingRepository) git(dir string, args ...string) string {
	r.t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = r.env()
	output, err := command.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func (r *listingRepository) generate(tag, out string) (int, string) {
	r.t.Helper()
	command := exec.Command("bash", filepath.Join(r.root, "scripts", "marketplace.sh"), tag, out)
	command.Env = r.env()
	output, err := command.CombinedOutput()
	if err != nil && command.ProcessState == nil {
		r.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), string(output)
}

func TestTheMarketplaceScriptPinsTheTagsCommit(t *testing.T) {
	r := newListingRepository(t)
	out := filepath.Join(t.TempDir(), "out")
	code, output := r.generate("v"+testVersion, out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, output)
	}
	action := read(t, filepath.Join(out, "action.yml"))
	if want := "uses: " + costroidAction + "@" + r.commit + " # v" + testVersion + "\n"; !strings.Contains(action, want) {
		t.Fatalf("action.yml lacks %q:\n%s", want, action)
	}
	if report := LintBytes("action.yml", []byte(action)); len(report.Findings) != 0 {
		t.Errorf("findings: %v", report.Findings)
	}
	for _, name := range []string{"action.yml", "README.md", "LICENSE", "NOTICE"} {
		if body := read(t, filepath.Join(out, name)); strings.Contains(body, "__RELEASE_") {
			t.Errorf("%s keeps a placeholder", name)
		}
	}
	if !strings.Contains(read(t, filepath.Join(out, "README.md")), "/blob/v"+testVersion+"/recipes/") {
		t.Error("the README does not link the recipe at the tag")
	}
}

func TestTheMarketplaceScriptWritesIntoTheListingsCheckoutWithoutCommitting(t *testing.T) {
	for _, origin := range []string{
		"https://github.com/Costroid/costroid-action.git",
		"https://github.com/Costroid/costroid-action",
		"git@github.com:Costroid/costroid-action.git",
	} {
		t.Run(origin, func(t *testing.T) {
			r := newListingRepository(t)
			out := filepath.Join(t.TempDir(), "costroid-action")
			r.git(filepath.Dir(out), "init", "--quiet", out)
			r.git(out, "remote", "add", "origin", origin)
			write(t, filepath.Join(out, "action.yml"), "an older release\n")
			if code, output := r.generate("v"+testVersion, out); code != 0 {
				t.Fatalf("exit %d: %s", code, output)
			}
			if !strings.Contains(read(t, filepath.Join(out, "action.yml")), "@"+r.commit+" # v") {
				t.Error("action.yml was not replaced")
			}
			if status := r.git(out, "status", "--porcelain"); !strings.Contains(status, "?? ") {
				t.Errorf("the script committed or staged:\n%s", status)
			}
			command := exec.Command("git", "-C", out, "rev-parse", "--verify", "--quiet", "HEAD")
			command.Env = r.env()
			if err := command.Run(); err == nil {
				t.Error("the listing's checkout has a commit")
			}
		})
	}
}

func TestTheMarketplaceScriptRefusesWhatIsNoReleaseOrNoListing(t *testing.T) {
	r := newListingRepository(t)
	r.git(r.root, "tag", "nightly")
	r.git(r.root, "tag", "v1.2")
	for name, check := range map[string]struct {
		tag   string
		setup func(out string)
		want  string
	}{
		"a tag that is no semver": {"nightly", nil, "is not a release tag of the form v<semver>"},
		"a short version":         {"v1.2", nil, "is not a release tag of the form v<semver>"},
		"no leading v":            {testVersion, nil, "is not a release tag of the form v<semver>"},
		"a missing tag":           {"v9.9.9", nil, "v9.9.9 is not a tag in"},
		"a non-empty directory": {"v" + testVersion, func(out string) {
			write(t, filepath.Join(out, "notes.txt"), "mine\n")
		}, "is not empty and not a git repository"},
		"another origin": {"v" + testVersion, func(out string) {
			r.git(out, "init", "--quiet")
			r.git(out, "remote", "add", "origin", "https://github.com/Costroid/costroid.git")
		}, "not Costroid/costroid-action"},
		"no origin": {"v" + testVersion, func(out string) {
			r.git(out, "init", "--quiet")
		}, "has no origin"},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			if check.setup != nil {
				check.setup(out)
			}
			code, output := r.generate(check.tag, out)
			if code == 0 || !strings.Contains(output, check.want) {
				t.Fatalf("exit %d: %s", code, output)
			}
			if _, err := os.Stat(filepath.Join(out, "action.yml")); !os.IsNotExist(err) {
				t.Error("it wrote action.yml")
			}
		})
	}
}

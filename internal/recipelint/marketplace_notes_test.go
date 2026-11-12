package recipelint

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const listingAction = "Costroid/costroid-action"

// releasedListing writes the listing for the scratch tag into a checkout of
// the listing, then commits and tags it there, as a release of the listing
// does. It returns the checkout and the listing's own commit.
func (r *listingRepository) releasedListing(annotated bool) (string, string) {
	r.t.Helper()
	out := filepath.Join(r.t.TempDir(), "costroid-action")
	r.git(filepath.Dir(out), "init", "--quiet", out)
	r.git(out, "remote", "add", "origin", "https://github.com/"+listingAction+".git")
	if code, output := r.generate("v"+testVersion, out); code != 0 {
		r.t.Fatalf("exit %d: %s", code, output)
	}
	r.git(out, "add", ".")
	r.git(out, "commit", "--quiet", "--message", "Call Costroid at "+testVersion)
	if annotated {
		r.git(out, "tag", "--annotate", "--message", "v"+testVersion, "v"+testVersion)
	} else {
		r.git(out, "tag", "v"+testVersion)
	}
	return out, strings.TrimSpace(r.git(out, "rev-parse", "HEAD"))
}

// notes runs the script's notes mode and returns its exit code, what it
// printed and what it said.
func (r *listingRepository) notes(out string) (int, string, string) {
	r.t.Helper()
	command := exec.Command("bash", filepath.Join(r.root, "scripts", "marketplace.sh"), "notes", "v"+testVersion, out)
	command.Env = r.env()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil && command.ProcessState == nil {
		r.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

// TestMarketplaceNotesNameTheListingsOwnCommit holds the listing to name its
// own commit wherever it tells a reader what to pin: the release notes name
// the commit of the listing's tag and call the listing at it, the README
// gives the command that resolves the tag to that commit, and both keep
// naming Costroid's commit as the one the action calls.
func TestMarketplaceNotesNameTheListingsOwnCommit(t *testing.T) {
	r := newListingRepository(t)
	out, own := r.releasedListing(true)
	if own == r.commit {
		t.Fatal("the listing's commit is Costroid's, so this test proves nothing")
	}
	code, notes, stderr := r.notes(out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := "Pin this action to `" + own + "`, the commit of this tag. It calls `" + costroidAction + "` at commit `" + r.commit + "`"; !strings.HasPrefix(notes, want) {
		t.Errorf("the notes do not open with %q:\n%s", want, notes)
	}
	readme := read(t, filepath.Join(out, "README.md"))
	readmeBlocks := yamlBlock.FindAllStringSubmatch(readme, -1)
	noteBlocks := yamlBlock.FindAllStringSubmatch(notes, -1)
	if len(noteBlocks) != 1 || len(readmeBlocks) == 0 {
		t.Fatalf("the notes hold %d YAML blocks, want one:\n%s", len(noteBlocks), notes)
	}
	report := readmeBlocks[len(readmeBlocks)-1][1]
	want := strings.ReplaceAll(report, costroidAction+"@"+r.commit, listingAction+"@"+own)
	if want == report || noteBlocks[0][1] != want {
		t.Errorf("the notes' example is not the README's report job calling %s@%s:\n%s", listingAction, own, noteBlocks[0][1])
	}

	command := shellBlock.FindStringSubmatch(readme[strings.Index(readme, "\n## The commit to pin\n"):])
	if command == nil {
		t.Fatal("the README's pin section gives no command")
	}
	args := strings.Fields(strings.TrimSpace(command[1]))
	if len(args) != 4 || args[0] != "git" || args[1] != "ls-remote" || args[2] != "https://github.com/"+listingAction {
		t.Fatalf("the README's command is %q", command[1])
	}
	resolved := r.git(r.root, "ls-remote", out, strings.Trim(args[3], "'"))
	if fields := strings.Fields(resolved); len(fields) != 2 || fields[0] != own {
		t.Errorf("the README's command prints %q, want the listing's commit %s", resolved, own)
	}

	security := read(t, filepath.Join(out, "SECURITY.md"))
	for _, said := range []string{"https://github.com/Costroid/costroid/security", `"Report a vulnerability"`, "/blob/v" + testVersion + "/SECURITY.md"} {
		if !strings.Contains(security, said) {
			t.Errorf("SECURITY.md does not say %s:\n%s", said, security)
		}
	}

	t.Run("a tag that is not annotated", func(t *testing.T) {
		r := newListingRepository(t)
		out, _ := r.releasedListing(false)
		if code, notes, stderr := r.notes(out); code == 0 || notes != "" || !strings.Contains(stderr, "holds no annotated tag") {
			t.Errorf("exit %d, notes %q: %s", code, notes, stderr)
		}
	})
	t.Run("a listing tag that calls another release", func(t *testing.T) {
		r := newListingRepository(t)
		out, _ := r.releasedListing(true)
		r.git(r.root, "commit", "--quiet", "--allow-empty", "--message", "Move on")
		r.git(r.root, "tag", "--force", "--annotate", "--message", "v"+testVersion, "v"+testVersion)
		if code, notes, stderr := r.notes(out); code == 0 || notes != "" || !strings.Contains(stderr, "does not call Costroid/costroid/action at") {
			t.Errorf("exit %d, notes %q: %s", code, notes, stderr)
		}
	})
}

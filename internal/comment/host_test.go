package comment

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focusfile"
)

// asGitLab is a state's report as GitLab's note carries it: no check run, and
// the project's namespace for an installation.
func asGitLab(input Input) Input {
	input.AccountHost, input.Installation, input.CheckRunID = AccountGitLab, gitlabNamespace, nil
	return input
}

// hostSurfaces are the texts a person reads of one report: the verdict
// sentence, the comment without its machine block, and the job summary.
func hostSurfaces(t *testing.T, input Input) map[string]string {
	t.Helper()
	r := renderState(t, input)
	return map[string]string{"sentence": VerdictSentence(input), "comment": readable(r.comment), "job summary": r.summary}
}

// gitHubsOwn are the two sentences that say "pull request" on every host. The
// first states a limit of GitHub's API in GitHub's words, and only GitHub
// keeps the check runs it is about. The second is the focusfile package's
// reason for a missing estimate, which the job log prints as well.
var gitHubsOwn = []string{commitCap, focusfile.NotAvailable(focusfile.DefaultSource).Sentence()}

// nounCount counts a noun in a text a person reads, in any case, outside the
// sentences that keep GitHub's word on every host.
func nounCount(text, noun string) int {
	for _, sentence := range gitHubsOwn {
		text = strings.ReplaceAll(text, sentence, "")
	}
	return strings.Count(strings.ToLower(text), noun)
}

// TestTheNounFollowsTheHost holds every word a person reads to the host's own
// noun for a change. Each golden state is rendered as GitLab's note and as the
// same report on GitHub: wherever GitHub's sentence, comment and job summary
// read "pull request", GitLab's read "merge request", as many times, and never
// "pull request". Nothing a machine reads follows the host: the result object
// and the outputs are the same bytes on both, so keys, rule ids and tags keep
// pr and pull_request.
func TestTheNounFollowsTheHost(t *testing.T) {
	for host, want := range map[string]string{AccountGitLab: "merge request", AccountGitHub: "pull request", "": "pull request"} {
		if got := requestNoun(host); got != want {
			t.Errorf("requestNoun(%q) = %q, want %q", host, got, want)
		}
	}
	said := 0
	states := goldenStates(t)
	for name, input := range states {
		onGitLab := asGitLab(input)
		onGitHub := onGitLab
		onGitHub.AccountHost = AccountGitHub
		gitHub, gitLab := hostSurfaces(t, onGitHub), hostSurfaces(t, onGitLab)
		for surface := range gitHub {
			pulls := nounCount(gitHub[surface], "pull request")
			said += pulls
			if merges := nounCount(gitLab[surface], "merge request"); merges != pulls || nounCount(gitLab[surface], "pull request") != 0 {
				t.Errorf("%s: GitHub's %s says \"pull request\" %d times, and GitLab's says \"merge request\" %d times and \"pull request\" %d times:\n%s",
					name, surface, pulls, merges, nounCount(gitLab[surface], "pull request"), gitLab[surface])
			}
		}
		gitHubBlock, err := onGitHub.block()
		if err != nil {
			t.Fatal(err)
		}
		gitLabBlock, err := onGitLab.block()
		if err != nil {
			t.Fatal(err)
		}
		if gitHubBlock != gitLabBlock || !slices.Equal(renderState(t, onGitHub).outputs, renderState(t, onGitLab).outputs) {
			t.Errorf("%s: the result object or the outputs follow the host", name)
		}
	}
	if said < 10 {
		t.Fatalf("the golden states say \"pull request\" %d times, too few to hold the noun", said)
	}

	skipped := asGitLab(states["no-figures-agent_skipped"])
	for surface, want := range map[string]string{
		"sentence": "No agent run: the workflow skipped the agent's job on this merge request, so there is no cost to report.",
		"comment":  "The recipes skip the agent's job for a first-time contributor without the `costroid-agent` label and for a fork's merge request.",
	} {
		if got := hostSurfaces(t, skipped)[surface]; !strings.Contains(got, want) {
			t.Errorf("GitLab's %s of a skipped agent does not say %q:\n%s", surface, want, got)
		}
	}
	total := hostSurfaces(t, states["gitlab-note-request-budget"])
	for surface, want := range map[string]string{
		"sentence": "Not decided: the merge request budget could not be checked.",
		"comment":  "Merge request budget: this check read no total for the merge request's earlier runs, so the rule did not run.",
	} {
		if !strings.Contains(total[surface], want) {
			t.Errorf("GitLab's %s under a budget on the change's total does not say %q:\n%s", surface, want, total[surface])
		}
	}
}

// TestGitHubSurfacesDoNotMoveForGitLabsNoun holds GitHub's surfaces to
// GitHub's word. No file of a golden state on GitHub says "merge request", in
// what a person reads or in what a machine reads, and a report that names no
// host renders byte for byte as one that names GitHub. TestGolden holds each
// of those files to its golden, so GitLab's noun moves none of them.
func TestGitHubSurfacesDoNotMoveForGitLabsNoun(t *testing.T) {
	checked := 0
	for name, input := range goldenStates(t) {
		if input.AccountHost == AccountGitLab {
			continue
		}
		checked++
		files := renderState(t, input).files()
		for file, text := range files {
			if strings.Contains(strings.ToLower(text), "merge request") {
				t.Errorf("%s: %s says \"merge request\" on GitHub:\n%s", name, file, text)
			}
		}
		named, unnamed := input, input
		named.AccountHost, unnamed.AccountHost = AccountGitHub, ""
		if !maps.Equal(renderState(t, named).files(), renderState(t, unnamed).files()) {
			t.Errorf("%s: a report that names no host does not render as GitHub's", name)
		}
	}
	if checked < 60 {
		t.Fatalf("%d golden states are GitHub's, too few to hold its surfaces", checked)
	}
}

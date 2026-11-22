package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/cli"
	receipts "github.com/Costroid/costroid/internal/receipt"
)

// claudeConfigVariable names Claude Code's configuration directory, ~/.claude
// when it is not set.
const claudeConfigVariable = "CLAUDE_CONFIG_DIR"

var receiptHelp = cli.Page{
	Command: "receipt",
	Summary: `receipt prints the token counts of your own Claude Code sessions on this
branch, as a comment to post on its pull request.`,
	Usage:    []string{"costroid receipt"},
	Examples: []string{"costroid receipt", "costroid receipt > receipt.md"},
	Sections: []cli.Section{
		{Heading: "What it reads", Body: []cli.Block{cli.Text(`It reads the session files Claude Code wrote on this machine, under
CLAUDE_CONFIG_DIR or ~/.claude, for this repository's working trees. Of each
line it reads the token counts, the model with its service tier and region,
the agent's version, the session's id, the time, the working directory and
the git branch, and never a prompt, an answer, a tool's input or output, or a
file's contents. A request counts when
its line recorded this branch and a working directory inside the repository,
from the oldest entry of the branch's reflog on. This release reads Claude Code
alone.`)}},
		{Heading: "What it prints", Body: []cli.Block{cli.Text(`Standard output is the comment's body alone: a marker line and the receipt, a
JSON object of counts by session, model and UTC day, with no amount and no
working directory. Standard error says what it counted and what it left out.
It opens no connection and posts nothing. Post the body yourself on the pull
request, as a comment of its own; once posted, everyone who can read the pull
request can read it. A report whose workflow asks for receipts prices the
newest one you posted and states it on one line, apart from the run's cost.
No budget reads it.`)}},
		{Heading: "Where it runs", Body: []cli.Block{cli.Text(`On your machine, on a branch. It refuses to run in a CI job, whose sessions
the run's own rows already hold, and on a detached head.`)}},
	},
	Environment: []cli.Block{cli.Rows{
		{claudeConfigVariable, "Claude Code's configuration directory, ~/.claude when unset"},
		{"CI, GITHUB_ACTIONS, GITLAB_CI", "a CI job, where receipt refuses to run"},
	}},
	Exits: []cli.Row{
		{"0", "the receipt is printed"},
		cli.UsageExit,
		{"125", "a CI job, no repository, a detached head, or no session on the branch"},
	},
	Docs: "receipt",
}.String()

var (
	errReceiptInCI   = cli.Stopped("receipt refused: this is a CI job", "A receipt states the sessions of a developer's own machine, and a CI run's rows already hold its sessions.", "Run costroid receipt on your machine, on the pull request's branch.")
	errReceiptNoRepo = cli.Stopped("this directory is in no git repository", "A receipt counts the sessions of one branch of one repository.", "Run costroid receipt inside the repository, on the pull request's branch.")
	errReceiptNoGit  = cli.Stopped("git is not on PATH", "receipt reads the branch, its worktrees and its reflog with git.", "Install git, then run costroid receipt again.")
	errReceiptNoHome = cli.Stopped("neither CLAUDE_CONFIG_DIR nor HOME is set", "Claude Code writes its session files under one of them.", "Set CLAUDE_CONFIG_DIR to Claude Code's configuration directory.")
)

// receiptCommand prints the receipt of the branch checked out in the
// working directory.
func receiptCommand(args []string, stdout, stderr io.Writer) int {
	return printReceipt(args, os.Getenv, time.Now, locateRepository, stdout, stderr)
}

// printReceipt reads the sessions under the configuration directory getenv
// names for the repository find locates, and prints the receipt of its
// branch as of now.
func printReceipt(args []string, getenv func(string) string, now func() time.Time, find locate, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("receipt", flag.ContinueOnError)
	err := parseFlags(set, args, receiptHelp, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil {
		err = cli.NoArgument(set)
	}
	if err != nil {
		return cli.Fail(stderr, cli.Mistaken("receipt", err))
	}
	if inCI(getenv) {
		return cli.Fail(stderr, errReceiptInCI)
	}
	scope, err := receiptScope(find, now())
	if err != nil {
		return cli.Fail(stderr, err)
	}
	config, err := claudeConfig(getenv)
	if err != nil {
		return cli.Fail(stderr, err)
	}
	found, err := claudecode.ReadLocalSessions(config, scope.Roots)
	if err != nil {
		return cli.Fail(stderr, fmt.Errorf("read Claude Code's session files under %s: %w", config, err))
	}
	stated, tally, err := receipts.Collect(found.Requests, scope)
	describeTally(stderr, scope, found, tally)
	if errors.Is(err, receipts.ErrNoSessions) {
		return cli.Fail(stderr, cli.Stopped(fmt.Sprintf("no Claude Code session on branch %s", scope.Branch),
			"A request counts when its line recorded this branch and a working directory inside this repository.",
			"Check out the pull request's branch where you worked with Claude Code, then run costroid receipt again."))
	}
	if err != nil {
		return cli.Fail(stderr, err)
	}
	body, err := stated.Body()
	if err != nil {
		return cli.Fail(stderr, err)
	}
	if _, err := io.WriteString(stdout, body); err != nil {
		return cli.Fail(stderr, err)
	}
	return 0
}

// inCI reports a CI job: one cicontext finds, or any that sets CI.
func inCI(getenv func(string) string) bool {
	ci := strings.ToLower(getenv("CI"))
	return cicontext.Detect(getenv) != cicontext.NoSystem || ci == "true" || ci == "1"
}

// receiptScope reads, with git, the branch checked out, the repository's
// worktrees and the oldest entry of the branch's reflog.
func receiptScope(find locate, now time.Time) (receipts.Scope, error) {
	repo, err := find()
	switch {
	case errors.Is(err, errNoRepository):
		return receipts.Scope{}, errReceiptNoRepo
	case errors.Is(err, errNoGit):
		return receipts.Scope{}, errReceiptNoGit
	case err != nil:
		return receipts.Scope{}, err
	}
	branch, err := gitOutput(repo.dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch = strings.TrimSpace(branch)
	if err != nil || branch == "" {
		return receipts.Scope{}, cli.Stopped("the working tree is on a detached head", "A receipt is for one branch.", "Check out the pull request's branch, then run costroid receipt again.")
	}
	if !receipts.ValidBranch(branch) {
		return receipts.Scope{}, cli.Stopped(fmt.Sprintf("the branch %q is not one a receipt can name", branch), "A receipt names a branch of letters, digits and . _ + - in parts joined by /.", "Run costroid receipt on a branch with such a name.")
	}
	scope := receipts.Scope{Branch: branch, Roots: worktrees(repo.root), Now: now}
	scope.Since = reflogStart(repo.root, branch)
	return scope, nil
}

// worktrees are the repository's working trees, each as git lists it and with
// its links followed, the root first.
func worktrees(root string) []string {
	roots := []string{root}
	listed, err := gitOutput(root, "worktree", "list", "--porcelain")
	if err != nil {
		return roots
	}
	for line := range strings.Lines(listed) {
		path, found := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "worktree ")
		if !found || !filepath.IsAbs(path) {
			continue
		}
		roots = append(roots, filepath.Clean(path))
		if real, err := filepath.EvalSymlinks(path); err == nil {
			roots = append(roots, real)
		}
	}
	slices.Sort(roots[1:])
	return slices.Compact(roots)
}

// reflogStart is when the oldest entry of the branch's reflog was written,
// zero when the reflog holds none. git names each entry as
// <branch>@{<seconds>} under --date=unix.
func reflogStart(root, branch string) time.Time {
	listed, err := gitOutput(root, "log", "--walk-reflogs", "--date=unix", "--format=%gd", "refs/heads/"+branch, "--")
	if err != nil {
		return time.Time{}
	}
	entries := strings.Fields(listed)
	if len(entries) == 0 {
		return time.Time{}
	}
	oldest := entries[len(entries)-1]
	at := strings.LastIndex(oldest, "@{")
	if at < 0 || !strings.HasSuffix(oldest, "}") {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(oldest[at+2:len(oldest)-1], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// claudeConfig is the directory Claude Code writes its session files under.
func claudeConfig(getenv func(string) string) (string, error) {
	if dir := getenv(claudeConfigVariable); dir != "" {
		return dir, nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errReceiptNoHome
	}
	return filepath.Join(home, ".claude"), nil
}

// describeTally says on standard error what the receipt counted and what it
// left out, lines --quiet drops. It names counts, days and the branch, and
// nothing a session said.
func describeTally(stderr io.Writer, scope receipts.Scope, found claudecode.LocalSessions, tally receipts.Tally) {
	since := "with no lower bound, because the branch's reflog holds no entry on this machine"
	if !scope.Since.IsZero() {
		since = "from " + scope.Since.Format("2006-01-02 15:04 UTC") + ", the oldest entry of the branch's reflog"
	}
	cli.Say(stderr, "costroid: counted %d %s of %d Claude Code %s on branch %s, %s\n",
		tally.Counted, plural("request", tally.Counted), found.Read, plural("session", found.Read), scope.Branch, since)
	for _, left := range []struct {
		count     int
		noun, why string
	}{
		{found.Unread, "session", "whose files hold a line this release cannot read"},
		{tally.Elsewhere, "request", "made on another branch"},
		{tally.NoBranch, "request", "whose lines recorded no branch"},
		{tally.Outside, "request", "made in a working directory outside this repository"},
		{tally.Earlier, "request", "made before the oldest entry of the branch's reflog"},
		{tally.Untimed, "request", "with no time, or a time after today"},
		{tally.Unnamed, "request", "of sessions whose id or version a receipt cannot state"},
		{tally.SessionsOmitted, "session", "the oldest, so the receipt fits in one comment"},
	} {
		if left.count > 0 {
			cli.Say(stderr, "costroid: left out %d %s %s\n", left.count, plural(left.noun, left.count), left.why)
		}
	}
}

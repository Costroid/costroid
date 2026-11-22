package receipt

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/receipt/receipttest"
)

const branch = "fix/retry-backoff"

// Session ids of the synthetic copies, one per session.
const (
	sessionA = "aaaaaaaa-0000-4000-8000-00000000000a"
	sessionB = "bbbbbbbb-0000-4000-8000-00000000000b"
	sessionC = "cccccccc-0000-4000-8000-00000000000c"
	sessionD = "dddddddd-0000-4000-8000-00000000000d"
	sessionE = "eeeeeeee-0000-4000-8000-00000000000e"
	sessionF = "ffffffff-0000-4000-8000-00000000000f"
	sessionG = "99999999-0000-4000-8000-000000000009"
)

var today = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

// at plants one working directory and one branch on every line.
func at(dir, onBranch string) receipttest.Place {
	return func(receipttest.Line) (string, string) { return dir, onBranch }
}

// machine is a configuration directory of synthetic sessions, and the
// repository's root and second worktree they ran in.
type machine struct {
	config, root, worktree string
}

// newMachine writes seven synthetic sessions: A, the recorded subagent run, on
// the branch in a directory under the root; B, one request, on main; C, one
// request, on the branch in the second worktree; D, the subagent run, which
// moves to main for its last request; E, one request whose lines record no
// branch; F, one request on the branch whose lines record a directory outside
// the repository; G, one request on the branch in another project.
func newMachine(t *testing.T) machine {
	t.Helper()
	m := machine{config: t.TempDir(), root: filepath.Join(t.TempDir(), "widgets"), worktree: filepath.Join(t.TempDir(), "widgets-review")}
	elsewhere := filepath.Join(t.TempDir(), "other")
	src := filepath.Join(m.root, "src")
	receipttest.Write(t, receipttest.Subagent, m.config, src, sessionA, at(src, branch), nil)
	receipttest.Write(t, receipttest.OneRequest, m.config, m.root, sessionB, at(m.root, "main"), nil)
	receipttest.Write(t, receipttest.OneRequest, m.config, m.worktree, sessionC, at(m.worktree, branch), nil)
	moves := func(line receipttest.Line) (string, string) {
		if line.Fields["requestId"] == "req_fixture_0005" {
			return m.root, "main"
		}
		return m.root, branch
	}
	receipttest.Write(t, receipttest.Subagent, m.config, m.root, sessionD, moves, nil)
	receipttest.Write(t, receipttest.OneRequest, m.config, m.root, sessionE, at(m.root, ""), nil)
	receipttest.Write(t, receipttest.OneRequest, m.config, m.root, sessionF, at(elsewhere, branch), nil)
	receipttest.Write(t, receipttest.OneRequest, m.config, elsewhere, sessionG, at(elsewhere, branch), nil)
	return m
}

func (m machine) read(t *testing.T) claudecode.LocalSessions {
	t.Helper()
	found, err := claudecode.ReadLocalSessions(m.config, []string{m.root, m.worktree})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TestReceiptCountsOnlyTheBranchsSessions reads synthetic copies of recorded
// Claude Code sessions and counts a request only when its line recorded the
// branch and a working directory inside one of the repository's worktrees:
// a subagent's request with its session, a session that moved to main up to
// the line where it moved, and nothing from main, from a line with no
// branch, from a directory outside the repository, or from another project,
// whose files are not opened. The reflog's oldest entry bounds the count from
// below.
func TestReceiptCountsOnlyTheBranchsSessions(t *testing.T) {
	m := newMachine(t)
	found := m.read(t)
	if found.Read != 6 || found.Unread != 0 {
		t.Fatalf("read %d sessions and left %d unread, want 6 read: another project's file was opened", found.Read, found.Unread)
	}
	scope := Scope{Branch: branch, Roots: []string{m.root, m.worktree}, Now: today}
	stated, tally, err := Collect(found.Requests, scope)
	if err != nil {
		t.Fatal(err)
	}
	want := Tally{Counted: 8, Elsewhere: 2, NoBranch: 1, Outside: 1}
	if tally != want {
		t.Errorf("tally %+v, want %+v", tally, want)
	}
	requests := map[string]int64{}
	for _, session := range stated.Sessions {
		requests[session.SessionID] += session.Requests
	}
	if len(stated.Sessions) != 3 || requests[sessionA] != 4 || requests[sessionC] != 1 || requests[sessionD] != 3 {
		t.Errorf("the receipt's entries: %+v", stated.Sessions)
	}
	recorded, input, output := receipttest.Requests(t, receipttest.Subagent)
	for _, session := range stated.Sessions {
		if session.SessionID == sessionA && (int64(recorded) != session.Requests || session.Tokens.InputUncached != strconv.FormatInt(input, 10) || session.Tokens.Output != strconv.FormatInt(output, 10)) {
			t.Errorf("session A states %d requests, %s input and %s output tokens; its files hold %d, %d and %d", session.Requests, session.Tokens.InputUncached, session.Tokens.Output, recorded, input, output)
		}
	}
	if stated.Since != nil || stated.Written != "2026-11-22" || stated.Branch != branch || stated.Sessions[0].FirstDay != "2026-11-01" {
		t.Errorf("the receipt: since %v, written %s, branch %s, first day %s", stated.Since, stated.Written, stated.Branch, stated.Sessions[0].FirstDay)
	}

	scope.Since = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	bounded, tally, err := Collect(found.Requests, scope)
	if err != nil {
		t.Fatal(err)
	}
	if tally.Counted != 1 || tally.Earlier != 7 || len(bounded.Sessions) != 1 || bounded.Sessions[0].SessionID != sessionC || *bounded.Since != "2026-11-05" {
		t.Errorf("bounded from 2026-11-05: tally %+v, entries %+v", tally, bounded.Sessions)
	}
	if _, _, err := Collect(found.Requests, Scope{Branch: "feature/none", Roots: scope.Roots, Now: today}); err != ErrNoSessions {
		t.Errorf("a branch no line recorded: %v", err)
	}
}

// TestReceiptHoldsNothingTheSessionSaid plants a canary in every field of a
// synthetic session that a person or a model wrote, in a tool's input and
// output, in a file's contents and in the working directory, and finds it
// nowhere in the receipt. The request is still counted.
func TestReceiptHoldsNothingTheSessionSaid(t *testing.T) {
	const canary = "CANARY-7f3e"
	config, root := t.TempDir(), filepath.Join(t.TempDir(), "widgets")
	dir := filepath.Join(root, canary)
	receipttest.Write(t, receipttest.OneRequest, config, dir, sessionA, at(dir, branch), receipttest.Plant(canary))
	found, err := claudecode.ReadLocalSessions(config, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	stated, tally, err := Collect(found.Requests, Scope{Branch: branch, Roots: []string{root}, Now: today})
	if err != nil || tally.Counted != 1 {
		t.Fatalf("the planted session gave %+v: %v", tally, err)
	}
	printed, err := stated.Body()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(printed), strings.ToLower(canary)) || strings.Contains(printed, root) {
		t.Errorf("the receipt holds what the session said:\n%s", printed)
	}
}

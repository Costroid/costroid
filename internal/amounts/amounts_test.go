package amounts

import (
	"context"
	"errors"
	"testing"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

const settingsCommit = "1111111111111111111111111111111111111111"

// repository is a host with one costroid.yml and one visibility, each of
// which can fail to answer. It counts what it was asked.
type repository struct {
	file       string
	missing    bool
	fileErr    error
	visibility scm.Visibility
	unseen     error
	files      int
	asked      int
}

func (r *repository) ReadFile(_ context.Context, path, ref string) ([]byte, error) {
	r.files++
	switch {
	case path != policy.ConfigPath || ref != settingsCommit:
		return nil, errors.New("read " + path + " at " + ref)
	case r.fileErr != nil:
		return nil, r.fileErr
	case r.missing:
		return nil, scm.ErrNotFound
	}
	return []byte(r.file), nil
}

func (r *repository) Visibility(context.Context) (scm.Visibility, error) {
	r.asked++
	return r.visibility, r.unseen
}

// TestReadAppliesTheShowCostRule holds the rule for each file and each
// visibility: a file that leaves the setting on shows amounts and asks no
// visibility, a private repository shows them whatever the file says, a
// public one withholds them when the file turns them off, and a read that
// fails leaves a repository not known to be private unsettled, with the
// reason it gives.
func TestReadAppliesTheShowCostRule(t *testing.T) {
	const off, invalid = "comment:\n  show_cost: false\n", "comment:\n  show_cost: sometimes\n"
	refused, hidden := errors.New("the file's read was refused"), errors.New("the visibility was not answered")
	for name, tc := range map[string]struct {
		host           repository
		ref            string
		want           comment.Amounts
		config         bool
		unread, unseen bool
		asked          int
	}{
		"no costroid.yml":                           {host: repository{missing: true}, want: comment.AmountsShown, config: true},
		"a file with no comment section":            {host: repository{file: "policy:\n  run_budget_usd: \"5\"\n"}, want: comment.AmountsShown, config: true},
		"show_cost on, visibility not answered":     {host: repository{file: "comment:\n  show_cost: true\n", unseen: hidden}, want: comment.AmountsShown, config: true},
		"show_cost off, private":                    {host: repository{file: off, visibility: scm.Private}, want: comment.AmountsShown, config: true, asked: 1},
		"show_cost off, public":                     {host: repository{file: off, visibility: scm.Public}, want: comment.AmountsOff, config: true, asked: 1},
		"show_cost off, visibility not answered":    {host: repository{file: off, unseen: hidden}, want: comment.AmountsUnsettled, config: true, unseen: true, asked: 1},
		"a value that is no switch, private":        {host: repository{file: invalid, visibility: scm.Private}, want: comment.AmountsShown, unread: true, asked: 1},
		"a value that is no switch, public":         {host: repository{file: invalid, visibility: scm.Public}, want: comment.AmountsUnsettled, unread: true, asked: 1},
		"a file the host refuses, public":           {host: repository{fileErr: refused, visibility: scm.Public}, want: comment.AmountsUnsettled, unread: true, asked: 1},
		"a file the host refuses, nothing answered": {host: repository{fileErr: refused, unseen: hidden}, want: comment.AmountsUnsettled, unread: true, unseen: true, asked: 1},
		"no commit to read at, public":              {host: repository{visibility: scm.Public}, ref: "main", want: comment.AmountsUnsettled, unread: true, asked: 1},
	} {
		ref := settingsCommit
		if tc.ref != "" {
			ref = tc.ref
		}
		host := tc.host
		got := Read(context.Background(), &host, ref)
		if got.Amounts != tc.want || (got.Config != nil) != tc.config || (got.Unread != nil) != tc.unread || (got.Unseen != nil) != tc.unseen {
			t.Errorf("%s: amounts %d, config %v, unread %v, unseen %v, want amounts %d", name, got.Amounts, got.Config, got.Unread, got.Unseen, tc.want)
		}
		if host.asked != tc.asked {
			t.Errorf("%s: the visibility was asked %d times, want %d", name, host.asked, tc.asked)
		}
		if tc.ref != "" && host.files != 0 {
			t.Errorf("%s: a file was read at %q, which is no commit", name, tc.ref)
		}
	}
}

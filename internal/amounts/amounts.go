// Package amounts decides whether what Costroid posts on a repository shows
// amounts. It is the comment.show_cost rule, read the same way for the
// report's comment and for a check posted again on an approval, by costroid
// approve and by the approve button of costroid serve.
package amounts

import (
	"context"
	"errors"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/vcs"
)

// Host is the repository the rule reads.
type Host interface {
	// ReadFile reads a file at a ref. A missing file is scm.ErrNotFound.
	ReadFile(ctx context.Context, path, ref string) ([]byte, error)
	Visibility(ctx context.Context) (scm.Visibility, error)
}

// Setting is what the rule found on one repository.
type Setting struct {
	Amounts comment.Amounts
	// Config is the comment's settings as costroid.yml states them at the
	// commit, nil when the file could not be read.
	Config *comment.Config
	// Unread says why costroid.yml could not be read, and Unseen why the
	// repository's visibility could not. Each is nil when the read succeeded
	// or the rule did not need it.
	Unread, Unseen error
	// Warnings are what costroid.yml holds that the comment does not read as
	// written, each the line comment.ParseConfig gives for it. Read prints
	// none of them: a command prints each, and a service logs it.
	Warnings []string
}

// Read applies the rule to costroid.yml at the commit ref and to the
// repository's visibility. A file that leaves comment.show_cost on shows
// amounts, and so does a private repository whatever the file says. A public
// repository whose file turns the setting off withholds them. When the file
// or the visibility cannot be read, a repository not known to be private
// withholds them, as unsettled.
func Read(ctx context.Context, host Host, ref string) Setting {
	config, warnings, err := readConfig(ctx, host, ref)
	if err == nil && config.ShowCost {
		return Setting{Amounts: comment.AmountsShown, Config: &config, Warnings: warnings}
	}
	setting := Setting{Config: &config, Unread: err, Warnings: warnings}
	if err != nil {
		setting.Config = nil
	}
	visibility, err := host.Visibility(ctx)
	switch {
	case err != nil:
		setting.Unseen = err
		setting.Amounts = comment.AmountsUnsettled
	case visibility == scm.Private:
		setting.Amounts = comment.AmountsShown
	case setting.Unread != nil:
		setting.Amounts = comment.AmountsUnsettled
	default:
		setting.Amounts = comment.AmountsOff
	}
	return setting
}

// readConfig reads the comment's settings at a commit. A repository with no
// costroid.yml there has the defaults.
func readConfig(ctx context.Context, host Host, ref string) (comment.Config, []string, error) {
	if !vcs.IsCommit(ref) {
		return comment.Config{}, nil, errors.New("no commit to read it from")
	}
	data, err := host.ReadFile(ctx, policy.ConfigPath, ref)
	if errors.Is(err, scm.ErrNotFound) {
		return comment.ParseConfig(nil)
	}
	if err != nil {
		return comment.Config{}, nil, err
	}
	return comment.ParseConfig(data)
}

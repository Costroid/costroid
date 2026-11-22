package claudecode

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
)

// LocalRequest is one model request a session file on a developer's machine
// recorded, with the session it belongs to and where it was made: the
// working directory and the git branch its line recorded, each empty when
// the line recorded none.
type LocalRequest struct {
	adapter.Request
	Session string
	Version string
	Dir     string
	Branch  string
}

// LocalSessions is what ReadLocalSessions found.
type LocalSessions struct {
	Requests []LocalRequest
	// Read counts the session files read, and Unread the sessions whose
	// files could not be read whole, so none of their requests is counted.
	Read, Unread int
}

// projectName is what Claude Code replaces in a working directory to name
// its project directory.
var projectName = regexp.MustCompile(`[^A-Za-z0-9]`)

// ReadLocalSessions reads the sessions Claude Code wrote under configDir for
// the working directories under roots: each session's main file and its
// subagents' files. A project directory is read when its name is a root's,
// or a directory's under it, as Claude Code names it; the working directory
// each line recorded decides the rest. Of each line it reads what a run's
// reader reads, and the line's working directory and git branch: never a
// prompt, a response, a tool's input or output, or a file's contents. A
// session whose files hold a line it cannot read is left out whole, and
// counted.
//
// A request the files hold no time for takes the first or last time its
// session states. Writes whose lifetime a line does not state are taken as
// one hour's, the rule for a session that states no API key.
func ReadLocalSessions(configDir string, roots []string) (LocalSessions, error) {
	projects := filepath.Join(configDir, "projects")
	entries, err := os.ReadDir(projects)
	if os.IsNotExist(err) {
		return LocalSessions{}, nil
	}
	if err != nil {
		return LocalSessions{}, err
	}
	var found LocalSessions
	for _, entry := range entries {
		if !entry.IsDir() || !projectOf(entry.Name(), roots) {
			continue
		}
		files, err := filepath.Glob(filepath.Join(projects, entry.Name(), "*.jsonl"))
		if err != nil {
			return LocalSessions{}, err
		}
		for _, file := range files {
			id := strings.TrimSuffix(filepath.Base(file), ".jsonl")
			if !sessionIDPattern.MatchString(id) {
				continue
			}
			requests, ok := readLocalSession(file, id)
			if !ok {
				found.Unread++
				continue
			}
			found.Read++
			found.Requests = append(found.Requests, requests...)
		}
	}
	return found, nil
}

// projectOf reports a project directory Claude Code named for a root or a
// directory under it.
func projectOf(name string, roots []string) bool {
	return slices.ContainsFunc(roots, func(root string) bool {
		prefix := projectName.ReplaceAllString(root, "-")
		return name == prefix || strings.HasPrefix(name, prefix+"-")
	})
}

// readLocalSession reads one session's main file and its subagents' files,
// and is false when a line of them cannot be read.
func readLocalSession(file, id string) ([]LocalRequest, bool) {
	builder := newRequestBuilder(focus.CacheWrite1h)
	facts, err := builder.read(file)
	if err != nil {
		return nil, false
	}
	last, err := readSubagents(builder, file, facts.last)
	if err != nil {
		return nil, false
	}
	var requests []LocalRequest
	for _, request := range builder.requests(facts.first, last) {
		where := builder.places[request.ID]
		requests = append(requests, LocalRequest{Request: request, Session: id, Version: facts.version, Dir: where.dir, Branch: where.branch})
	}
	return requests, true
}

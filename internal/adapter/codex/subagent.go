package codex

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"
)

// subagentSource is the session_meta thread_source of a thread another thread
// started.
const subagentSource = `"subagent"`

// rolloutPattern matches every rollout file under a Codex home.
var rolloutPattern = path.Join("sessions", "*", "*", "*", "rollout-*.jsonl")

// subagents follows the rollout files of the threads the run's thread started,
// and of the threads those started. Each writes its own file, and its
// session_meta names the thread that started it in session_id. Only the
// Tail goroutine changes it.
type subagents struct {
	// scanned holds the files already judged, ours or not.
	scanned map[string]bool
	// waiting holds subagent files whose parent is not yet known to be ours.
	waiting map[string]sessionMeta
	// threads holds the run's own thread and every subagent thread taken.
	threads map[string]bool
	files   []*subagentFile
}

type subagentFile struct {
	file rolloutFile
	log  rolloutLog
}

// subagentRead is what one subagent's file gained.
type subagentRead struct {
	sub   *subagentFile
	lines [][]byte
	err   error
}

// read finds new subagent files of the thread and returns what each file
// gained. A file last written before the thread started is skipped unread.
func (a *subagents) read(home, thread string, since time.Time) []subagentRead {
	if home == "" || !threadPattern.MatchString(thread) {
		return nil
	}
	a.discover(home, thread, since)
	reads := make([]subagentRead, 0, len(a.files))
	for _, sub := range a.files {
		lines, err := sub.file.lines()
		reads = append(reads, subagentRead{sub: sub, lines: lines, err: err})
	}
	return reads
}

func (a *subagents) discover(home, thread string, since time.Time) {
	if a.scanned == nil {
		a.scanned, a.waiting, a.threads = map[string]bool{}, map[string]sessionMeta{}, map[string]bool{}
	}
	a.threads[thread] = true
	found, err := fs.Glob(os.DirFS(home), rolloutPattern)
	if err != nil {
		return
	}
	for _, name := range found {
		if a.scanned[name] {
			continue
		}
		full := filepath.Join(home, filepath.FromSlash(name))
		meta, complete := firstMeta(full, since)
		if !complete {
			continue
		}
		a.scanned[name] = true
		if string(meta.ThreadSource) == subagentSource && meta.ID != thread {
			a.waiting[full] = meta
		}
	}
	a.take()
}

// take opens every waiting file whose parent is one of the run's threads,
// until no more qualify, in path order.
func (a *subagents) take() {
	for taken := true; taken; {
		taken = false
		paths := make([]string, 0, len(a.waiting))
		for name := range a.waiting {
			paths = append(paths, name)
		}
		slices.Sort(paths)
		for _, name := range paths {
			meta := a.waiting[name]
			if !a.threads[meta.SessionID] {
				continue
			}
			file, err := os.Open(name)
			if err != nil {
				continue
			}
			delete(a.waiting, name)
			a.threads[meta.ID] = true
			a.files = append(a.files, &subagentFile{file: rolloutFile{file: file}, log: rolloutLog{thread: meta.ID, opened: true}})
			taken = true
		}
	}
}

// firstMeta reads a file's first line as session_meta. It reports false while
// that line is not yet whole, so the file is judged again later. A file last
// written before since, or whose first line is anything else, is judged as no
// subagent of the run.
func firstMeta(name string, since time.Time) (sessionMeta, bool) {
	info, err := os.Stat(name)
	if err != nil {
		return sessionMeta{}, false
	}
	if info.ModTime().Before(since.Add(-time.Minute)) {
		return sessionMeta{}, true
	}
	file, err := os.Open(name)
	if err != nil {
		return sessionMeta{}, false
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadBytes('\n')
	if err != nil {
		return sessionMeta{}, false
	}
	var entry rolloutEntry
	var meta sessionMeta
	if json.Unmarshal(line, &entry) != nil || entry.Type != "session_meta" || json.Unmarshal(entry.Payload, &meta) != nil {
		return sessionMeta{}, true
	}
	return meta, true
}

// settled adds every subagent's running total.
func (a *subagents) settled() usage {
	var total usage
	for _, sub := range a.files {
		total = total.plus(sub.log.settled)
	}
	return total
}

// inFlight counts the requests the subagents' files show sent and unsettled,
// and returns the latest time one was sent.
func (a *subagents) inFlight() (int, time.Time) {
	var count int
	var last time.Time
	for _, sub := range a.files {
		if !sub.log.sent.IsZero() {
			count++
			last = later(last, sub.log.sent)
		}
	}
	return count, last
}

func (a *subagents) close() {
	for _, sub := range a.files {
		sub.file.close()
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

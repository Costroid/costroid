package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
)

// homeNote is the grade's note on a run read from its Codex home after it
// ended, as openai/codex-action leaves one.
const homeNote = "none: Costroid did not start this run, so no budget held it"

// homeSlack is how far before the job's start a rollout file's times may read,
// for a clock or a file system that rounds, and for a start stated in whole
// seconds.
const homeSlack = time.Minute

// ReadHome reads a Codex run that already ended from the Codex home it wrote
// to: every rollout file under sessions/, the run's own threads and their
// subagents alike, each read as the wrapper reads a rollout file. The home must
// be one made for the job, as codex-action's codex-home input makes one. A file
// last written before since belongs to an earlier job and is not read; a file
// started before since and written after it holds an earlier job's spend, so
// the reading is refused. A zero since reads every file.
//
// Costroid did not start this run, so no budget held it: the grade is none. The
// files do not say how the run billed, so the rows read list_equivalent, the
// weaker claim.
func (Agent) ReadHome(home string, since time.Time) (adapter.Report, error) {
	info, err := os.Stat(home)
	if err != nil {
		return adapter.Report{}, fmt.Errorf("codex: read the Codex home: %w", err)
	}
	if !info.IsDir() {
		return adapter.Report{}, fmt.Errorf("codex: the Codex home %s is not a directory", home)
	}
	names, err := fs.Glob(os.DirFS(home), rolloutPattern)
	if err != nil {
		return adapter.Report{}, fmt.Errorf("codex: list the rollout files under %s: %w", home, err)
	}
	model := modelFrom(nil, filepath.Join(home, "config.toml"))
	reading := homeReading{since: since}
	for _, name := range names {
		if err := reading.read(filepath.Join(home, filepath.FromSlash(name)), name, model); err != nil {
			return adapter.Report{}, err
		}
	}
	if len(reading.files) == 0 {
		return adapter.Report{}, reading.nothingRead(home)
	}
	return reading.report(), nil
}

// homeReading is what the files under one Codex home said.
type homeReading struct {
	since time.Time
	files []homeFile
	// earlier counts the files last written before the job started.
	earlier int
}

// homeFile is one thread's rollout file, read whole.
type homeFile struct {
	name     string
	log      rolloutLog
	subagent bool
}

// read reads one rollout file. A file the reader refuses refuses the whole
// reading, because it holds some of the run's figures.
func (r *homeReading) read(path, name, model string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("codex: read rollout file %s: %w", name, err)
	}
	if !r.since.IsZero() && info.ModTime().Before(r.since.Add(-homeSlack)) {
		r.earlier++
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("codex: read rollout file %s: %w", name, err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	var first rolloutEntry
	var meta sessionMeta
	if json.Unmarshal(lines[0], &first) != nil || first.Type != "session_meta" || json.Unmarshal(first.Payload, &meta) != nil {
		return fmt.Errorf("codex: rollout file %s does not open with a session_meta line", name)
	}
	if !r.since.IsZero() && first.Timestamp.Before(r.since.Add(-homeSlack)) {
		return fmt.Errorf("codex: rollout file %s was started at %s, before the job started, so it holds an earlier run's spend; give codex-home a directory made for the job", name, first.Timestamp.UTC().Format(time.RFC3339))
	}
	file := homeFile{name: name, log: rolloutLog{thread: meta.ID, opened: true}, subagent: string(meta.ThreadSource) == subagentSource}
	for _, line := range lines {
		file.log.apply(line, model)
	}
	if file.log.problem != "" {
		return fmt.Errorf("codex: rollout file %s: %s", name, file.log.problem)
	}
	r.files = append(r.files, file)
	return nil
}

// nothingRead is the error of a home that held no rollout file of the job.
func (r *homeReading) nothingRead(home string) error {
	why := fmt.Sprintf("codex: no rollout file under %s was written during this job, so codex-action did not start Codex or gave it another codex-home", filepath.Join(home, "sessions"))
	if r.earlier > 0 {
		why += fmt.Sprintf("; %s there %s last written before the job started", counted(r.earlier, "rollout file"), plural(r.earlier, "was", "were"))
	}
	return errors.New(why)
}

// report puts the threads together: the first thread no other started names
// the session, and every thread's requests are rows.
func (r *homeReading) report() adapter.Report {
	report := adapter.Report{
		CostBasis:   focus.ListEquivalent,
		CostSource:  costSourceRollout,
		Enforcement: adapter.Enforcement{Grade: adapter.GradeNone, Note: homeNote},
	}
	var versions []string
	var lastSent time.Time
	for _, file := range r.files {
		if report.SessionID == "" && !file.subagent {
			report.SessionID, report.AgentVersion = file.log.thread, file.log.version
		}
		report.Requests = append(report.Requests, file.log.requests...)
		if version := file.log.version; !slices.Contains(recordedVersions, version) && !slices.Contains(versions, version) {
			versions = append(versions, version)
		}
		if !file.log.sent.IsZero() {
			report.Unrecorded++
			lastSent = later(lastSent, file.log.sent)
		}
	}
	if report.SessionID == "" {
		report.SessionID, report.AgentVersion = r.files[0].log.thread, r.files[0].log.version
	}
	if r.earlier > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s under the Codex home %s last written before the job started, so %s not read",
			counted(r.earlier, "rollout file"), plural(r.earlier, "was", "were"), plural(r.earlier, "it was", "they were")))
	}
	for _, version := range versions {
		report.Warnings = append(report.Warnings, fmt.Sprintf("Codex %s is not a recorded version; its rollout files passed every check", version))
	}
	if report.Unrecorded > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("the rollout files show %s sent with no usage settled, the last at %s: Codex writes usage only when a request completes, so %s not counted",
			counted(report.Unrecorded, "model request"), lastSent.UTC().Format(time.RFC3339Nano), plural(report.Unrecorded, "that request is", "those requests are")))
	}
	return report
}

// counted is a count and its noun, plural past one.
func counted(count int, noun string) string {
	return fmt.Sprintf("%d %s", count, plural(count, noun, noun+"s"))
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

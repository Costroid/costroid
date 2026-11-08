package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	// ghawSchemaPrefix starts the _schema of every token-usage record.
	ghawSchemaPrefix = "token-usage/v"
	// ghawUsageFile is the file gh-aw's API proxy writes, one record per
	// model request, and the name it takes in a recording of the agent
	// artifact.
	ghawUsageFile = "token-usage.jsonl"
	// ghawUsageDir and ghawInfoDir are where gh run download writes a run's
	// usage and info artifacts when it is given both names.
	ghawUsageDir = "usage"
	ghawInfoDir  = "info"
	// ghawRunInfo is the file of the info artifact that states the run's cap.
	ghawRunInfo = "aw_info.json"
)

// ghawUsageFiles are the files of the usage artifact a recording keeps: the
// agent job's and the threat detection job's records. Every other file of the
// artifact holds the run's content or its repository's, and is left out.
var ghawUsageFiles = []string{"agent/token_usage.jsonl", "detection/token_usage.jsonl"}

// collectGhAw copies a gh-aw run's records from what gh run download wrote to
// src.home. Given the usage and info artifacts, as gh run download RUN -n
// usage -n info writes them, it copies the records of both jobs and the info
// artifact's aw_info.json under the same names. Given the agent artifact, it
// copies the proxy's token-usage.jsonl: the artifact keeps it under
// sandbox/firewall/logs/api-proxy-logs/ and older runs under
// firewall-audit-logs/api-proxy-logs/, so every file of that name is found.
// Copies that differ are refused, since the run's records cannot be told apart.
func collectGhAw(e *agentEntry, c *collected, src source, _, _ []byte) error {
	if src.stream != "" || src.executionFile != "" {
		return fmt.Errorf("%s leaves no stream or execution file: give --home the downloaded artifact's directory alone", e.name)
	}
	if _, err := fs.Stat(src.home, path.Join(ghawUsageDir, ghawUsageFiles[0])); err == nil {
		return collectGhAwUsage(c, src.home)
	}
	var found []string
	err := fs.WalkDir(src.home, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() && path.Base(name) == ghawUsageFile {
			found = append(found, name)
		}
		return err
	})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return errors.New("no token-usage.jsonl under --home: download the run's usage and info artifacts with gh run download RUN -n usage -n info")
	}
	data, err := fs.ReadFile(src.home, found[0])
	if err != nil {
		return err
	}
	for _, other := range found[1:] {
		copied, err := fs.ReadFile(src.home, other)
		if err != nil {
			return err
		}
		if !bytes.Equal(copied, data) {
			return fmt.Errorf("%s and %s differ: download one artifact", found[0], other)
		}
	}
	return c.add(ghawUsageFile, data)
}

// collectGhAwUsage copies the records of the usage artifact under usage/ and
// the info artifact's aw_info.json under info/, each where it is found.
func collectGhAwUsage(c *collected, home fs.FS) error {
	names := []string{path.Join(ghawInfoDir, ghawRunInfo)}
	for _, name := range ghawUsageFiles {
		names = append(names, path.Join(ghawUsageDir, name))
	}
	for _, name := range names {
		data, err := fs.ReadFile(home, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := c.add(name, data); err != nil {
			return err
		}
	}
	return nil
}

// ghawVersion is the version the records' _schema states: 0.29.8 for
// token-usage/v0.29.8.
func ghawVersion(files []*recordingFile) string {
	for _, file := range files {
		for _, line := range file.lines {
			if version, ok := strings.CutPrefix(line.fieldText("_schema"), ghawSchemaPrefix); ok && version != "" {
				return version
			}
		}
	}
	return ""
}

// ghawTotal is gh-aw's own figure for the run: the AI credits it counted, in
// its own unit, which Costroid never converts.
func ghawTotal(files []*recordingFile) string {
	for _, name := range []string{ghawUsageFile, path.Join(ghawUsageDir, ghawUsageFiles[0])} {
		last := lastLine(files, name, func(v *value) bool {
			total := v.field("ai_credits_total")
			return total != nil && total.isNumber()
		})
		if last != nil {
			return fmt.Sprintf("gh-aw's own figure for the run: %s AI credits, the `ai_credits_total` of the last line in `%s` that states it. Costroid does not convert credits.", last.field("ai_credits_total").raw, name)
		}
	}
	return "No line states gh-aw's AI credits for the run."
}

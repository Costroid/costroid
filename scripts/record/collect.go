package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// source is where one run's files are read from.
type source struct {
	agent string
	// stream is the agent's stdout: claude -p --output-format stream-json, or
	// codex exec --json. Empty when the run left none.
	stream string
	// executionFile is claude-code-action's execution_file output.
	executionFile string
	// home is the agent's own directory, CLAUDE_CONFIG_DIR or CODEX_HOME,
	// read through home alone so a test can see every file opened.
	home fs.FS
	// freshHome says home was made for this run, as on a CI runner, so every
	// session in it is the run's: a nested session or a thread with no stream.
	freshHome bool
	// since excludes Codex rollout files last written before the run began.
	since time.Time
}

// collected is what one run left, by the name each file takes in a recording.
type collected struct {
	files map[string][]byte
}

func (c *collected) add(name string, data []byte) error {
	if _, taken := c.files[name]; taken {
		return fmt.Errorf("two files would both be %s", name)
	}
	c.files[name] = data
	return nil
}

// write puts every file under dir, readable by its owner alone.
func (c *collected) write(dir string, m manifest) error {
	for name, data := range c.files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
	}
	return writeManifest(dir, m)
}

// prompts returns the text of every user message the run's files hold, so
// verify can refuse a scrubbed file that still carries one.
func (c *collected) prompts() []string {
	var texts []string
	for name, data := range c.files {
		file, err := parseFile(name, data)
		if err != nil {
			continue
		}
		for _, line := range file.lines {
			texts = append(texts, userTexts(line)...)
		}
	}
	return texts
}

// userTexts finds a user message's text: Claude Code's message.content, as a
// string or as text blocks, and Codex's input_text items.
func userTexts(line *value) []string {
	var texts []string
	add := func(content *value) {
		if content == nil {
			return
		}
		if content.kind == kindString {
			texts = append(texts, content.text)
			return
		}
		for _, block := range content.items {
			if text := block.fieldText("text"); text != "" {
				texts = append(texts, text)
			}
		}
	}
	if line.fieldText("type") == "user" {
		add(line.field("message").field("content"))
	}
	if payload := line.field("payload"); payload.fieldText("role") == "user" {
		add(payload.field("content"))
	}
	var long []string
	for _, text := range texts {
		if len(strings.TrimSpace(text)) >= minimumPromptLine {
			long = append(long, text)
		}
	}
	return long
}

func collect(src source) (*collected, error) {
	c := &collected{files: map[string][]byte{}}
	var stream, execution []byte
	var err error
	if src.stream != "" {
		if stream, err = os.ReadFile(src.stream); err != nil {
			return nil, fmt.Errorf("read the stream: %w", err)
		}
		if err := c.add("stream.jsonl", stream); err != nil {
			return nil, err
		}
	}
	if src.executionFile != "" {
		if execution, err = os.ReadFile(src.executionFile); err != nil {
			return nil, fmt.Errorf("read the execution file: %w", err)
		}
		if err := c.add("claude-execution-output.json", execution); err != nil {
			return nil, err
		}
	}
	entry, err := agentFor(src.agent)
	if err != nil {
		return nil, err
	}
	return c, entry.collect(entry, c, src, stream, execution)
}

// collectClaudeRun copies the session the stream or the execution file names.
func collectClaudeRun(e *agentEntry, c *collected, src source, stream, execution []byte) error {
	session := firstField(stream, isInit, "session_id")
	if session == "" {
		session = firstField(executionLines(execution), isInit, "session_id")
	}
	if session == "" {
		return fmt.Errorf("no system/init line names the session: run %s, or name the execution file", e.stream)
	}
	return collectClaude(c, src, session)
}

// collectCodexRun copies the thread the stream names, or with a home made for
// the run, the thread no stream named.
func collectCodexRun(e *agentEntry, c *collected, src source, stream, _ []byte) error {
	thread := firstField(stream, func(line *value) bool { return line.fieldText("type") == "thread.started" }, "thread_id")
	if thread == "" && !src.freshHome {
		return fmt.Errorf("no thread.started line names the thread: run %s, or collect from a home made for this run", e.stream)
	}
	return collectCodex(c, src, thread)
}

func isInit(line *value) bool {
	return line.fieldText("type") == "system" && line.fieldText("subtype") == "init"
}

// firstField is key's text on the first JSON line that match accepts.
func firstField(data []byte, match func(*value) bool, key string) string {
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line, err := parseValue(raw)
		if err != nil || line.kind != kindObject || !match(line) {
			continue
		}
		if text := line.fieldText(key); text != "" {
			return text
		}
	}
	return ""
}

// executionLines puts an execution file's messages one per line.
func executionLines(data []byte) []byte {
	document, err := parseValue(data)
	if err != nil || document.kind != kindArray {
		return nil
	}
	var out bytes.Buffer
	for _, item := range document.items {
		item.compact(&out)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// collectClaude copies the session's own file and its subagents' files. Only
// a home made for the run gives up its other sessions, as nested sessions.
func collectClaude(c *collected, src source, session string) error {
	if !safeWord.MatchString(session) {
		return fmt.Errorf("the session id %q is not a plain word", session)
	}
	mains, err := fs.Glob(src.home, path.Join("projects", "*", session+".jsonl"))
	if err != nil {
		return err
	}
	if len(mains) == 0 {
		return fmt.Errorf("no session file for the run's session under projects/ in the agent's directory")
	}
	if err := copySession(c, src.home, mains[0], ""); err != nil {
		return err
	}
	if !src.freshHome {
		return nil
	}
	others, err := fs.Glob(src.home, path.Join("projects", "*", "*.jsonl"))
	if err != nil {
		return err
	}
	nested := 0
	for _, other := range others {
		if path.Base(other) == session+".jsonl" {
			continue
		}
		nested++
		if err := copySession(c, src.home, other, fmt.Sprintf("nested/%d/", nested)); err != nil {
			return err
		}
	}
	return nil
}

// copySession copies one session file as prefix+session.jsonl and its
// subagents' files as prefix+subagents/agent-N.jsonl, in name order.
func copySession(c *collected, home fs.FS, file, prefix string) error {
	data, err := fs.ReadFile(home, file)
	if err != nil {
		return err
	}
	if err := c.add(prefix+"session.jsonl", data); err != nil {
		return err
	}
	subagents, err := fs.Glob(home, path.Join(strings.TrimSuffix(file, ".jsonl"), "subagents", "*.jsonl"))
	if err != nil {
		return err
	}
	slices.Sort(subagents)
	for i, subagent := range subagents {
		data, err := fs.ReadFile(home, subagent)
		if err != nil {
			return err
		}
		if err := c.add(fmt.Sprintf("%ssubagents/agent-%d.jsonl", prefix, i+1), data); err != nil {
			return err
		}
	}
	return nil
}

// collectCodex copies the thread's rollout file and its subagents' rollout
// files. A subagent's file is found by its session_meta line, which is read
// only in files written since the run began.
func collectCodex(c *collected, src source, thread string) error {
	files, err := fs.Glob(src.home, path.Join("sessions", "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return err
	}
	slices.Sort(files)
	var main string
	var candidates []string
	for _, file := range files {
		if thread != "" && strings.HasSuffix(file, "-"+thread+".jsonl") {
			main = file
			continue
		}
		info, err := fs.Stat(src.home, file)
		if err != nil {
			return err
		}
		if src.freshHome || !info.ModTime().Before(src.since) {
			candidates = append(candidates, file)
		}
	}
	subagents := 0
	nested := 0
	for _, file := range candidates {
		meta, err := sessionMeta(src.home, file)
		if err != nil {
			return err
		}
		switch {
		case meta.fieldText("thread_source") == "subagent" && thread != "" && meta.fieldText("session_id") == thread:
			subagents++
			err = copyFile(c, src.home, file, fmt.Sprintf("subagents/rollout-%d.jsonl", subagents))
		case thread == "" && main == "" && meta.fieldText("thread_source") != "subagent":
			main, thread = file, meta.fieldText("id")
		case src.freshHome:
			nested++
			err = copyFile(c, src.home, file, fmt.Sprintf("nested/%d/rollout.jsonl", nested))
		}
		if err != nil {
			return err
		}
	}
	if main == "" {
		return errors.New("no rollout file for the run's thread under sessions/ in the agent's directory")
	}
	return copyFile(c, src.home, main, "rollout.jsonl")
}

func copyFile(c *collected, home fs.FS, file, name string) error {
	data, err := fs.ReadFile(home, file)
	if err != nil {
		return err
	}
	return c.add(name, data)
}

// sessionMeta is the payload of a rollout file's first line.
func sessionMeta(home fs.FS, file string) (*value, error) {
	f, err := home.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	first, err := bufio.NewReaderSize(f, 1<<20).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	line, err := parseValue(first)
	if err != nil || line.fieldText("type") != "session_meta" {
		return &value{kind: kindObject}, nil
	}
	return line.field("payload"), nil
}

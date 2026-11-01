// Package cicontext reads what the CI system says about a run.
//
// Everything here is a claim. The CI environment is writable by whatever ran
// earlier in the job, so a row that carries these values stays unverified and
// unattributed until a server has checked them against the job's OIDC token.
package cicontext

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/focus"
)

// ResourceAttributesVariable is the variable OpenTelemetry SDKs read.
const ResourceAttributesVariable = "OTEL_RESOURCE_ATTRIBUTES"

const (
	attributeChange     = "vcs.change.id"
	attributeHead       = "vcs.ref.head.revision"
	attributeBase       = "vcs.ref.base.revision"
	attributeRepository = "vcs.repository.url.full"
	attributeRun        = "cicd.pipeline.run.id"
	attributeTask       = "cicd.pipeline.task.run.id"
)

// The tag convention's value patterns. A value that misses one is dropped, so a
// tag with no valid value is never written.
var (
	repositoryPattern  = regexp.MustCompile(`^[a-z0-9.-]+(/[a-z0-9._~%-]+){1,8}$`)
	pullRequestPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	commitPattern      = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	runPattern         = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// maxRepositoryLength is the tag convention's limit on costroid/repo.
const maxRepositoryLength = 256

// Context is one CI run's stated facts. A zero Context means no CI system was
// found, and adds nothing to a row or to the agent's environment.
type Context struct {
	// Repository is host and path in lower case, the form of costroid/repo.
	Repository string
	// RepositoryURL is the repository's address, for vcs.repository.url.full.
	RepositoryURL string
	PullRequest   string
	Head          string
	Base          string
	Run           string
	Task          string
}

// Read finds the CI system from its own variables. An error means the system
// was found and its event could not be read: the Context still holds what the
// variables said, without a head that might name a merge commit.
func Read(getenv func(string) string) (Context, error) {
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		return readGitHub(getenv)
	case getenv("GITLAB_CI") == "true":
		return readGitLab(getenv), nil
	}
	return Context{}, nil
}

func readGitHub(getenv func(string) string) (Context, error) {
	server := strings.TrimRight(cmp.Or(getenv("GITHUB_SERVER_URL"), "https://github.com"), "/")
	context := Context{
		Repository:    repositoryOf(strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://"), getenv("GITHUB_REPOSITORY")),
		RepositoryURL: server + "/" + getenv("GITHUB_REPOSITORY"),
		Run:           getenv("GITHUB_RUN_ID"),
		Task:          getenv("GITHUB_JOB"),
	}
	event, err := ReadEvent(getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		if !strings.HasPrefix(getenv("GITHUB_EVENT_NAME"), "pull_request") {
			context.Head = getenv("GITHUB_SHA")
		}
		return context.checked(), err
	}
	context.Head = getenv("GITHUB_SHA")
	if event.PullRequest != nil {
		context.PullRequest = fmt.Sprint(event.PullRequest.Number)
		context.Head = event.PullRequest.Head.SHA
		context.Base = event.PullRequest.Base.SHA
	}
	return context.checked(), nil
}

// readGitLab prefers the source branch's head. GitLab sets that variable only
// in merged results pipelines, where CI_COMMIT_SHA is a temporary merge.
func readGitLab(getenv func(string) string) Context {
	context := Context{
		Repository:    repositoryOf(getenv("CI_SERVER_HOST"), getenv("CI_PROJECT_PATH")),
		RepositoryURL: getenv("CI_PROJECT_URL"),
		PullRequest:   getenv("CI_MERGE_REQUEST_IID"),
		Head:          cmp.Or(getenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"), getenv("CI_COMMIT_SHA")),
		Base:          getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA"),
		Run:           getenv("CI_PIPELINE_ID"),
		Task:          getenv("CI_JOB_ID"),
	}
	return context.checked()
}

func repositoryOf(host, path string) string {
	return strings.ToLower(host + "/" + path)
}

// checked keeps the values the tag convention allows. A repository that fails
// takes its address with it, because both name the same thing.
func (c Context) checked() Context {
	if len(c.Repository) > maxRepositoryLength || !repositoryPattern.MatchString(c.Repository) {
		c.Repository, c.RepositoryURL = "", ""
	}
	c.PullRequest = keep(pullRequestPattern, c.PullRequest)
	c.Head = keep(commitPattern, c.Head)
	c.Base = keep(commitPattern, c.Base)
	c.Run = keep(runPattern, c.Run)
	c.Task = keep(runPattern, c.Task)
	return c
}

func keep(pattern *regexp.Regexp, value string) string {
	if pattern.MatchString(value) {
		return value
	}
	return ""
}

// Tags returns the join keys a row states.
func (c Context) Tags() map[string]string {
	tags := map[string]string{}
	for key, value := range map[string]string{
		focus.TagRepo:          c.Repository,
		focus.TagPR:            c.PullRequest,
		focus.TagCommitSHA:     c.Head,
		focus.TagWorkflowRunID: c.Run,
	} {
		if value != "" {
			tags[key] = value
		}
	}
	return tags
}

// ResourceAttributes merges the run's vcs.* and cicd.* attributes into the
// value of OTEL_RESOURCE_ATTRIBUTES the job already set, and returns the result.
// Costroid's value replaces one the job set under the same key. Empty means
// there is nothing to set.
func (c Context) ResourceAttributes(existing string) string {
	attributes := map[string]string{}
	for key, value := range map[string]string{
		attributeChange:     c.PullRequest,
		attributeHead:       c.Head,
		attributeBase:       c.Base,
		attributeRepository: c.RepositoryURL,
		attributeRun:        c.Run,
		attributeTask:       c.Task,
	} {
		if value != "" {
			attributes[key] = escapeAttribute(value)
		}
	}
	if len(attributes) == 0 {
		return ""
	}
	var merged []string
	for _, pair := range strings.Split(existing, ",") {
		key, _, _ := strings.Cut(pair, "=")
		if _, replaced := attributes[strings.TrimSpace(key)]; pair != "" && !replaced {
			merged = append(merged, pair)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(attributes)) {
		merged = append(merged, key+"="+attributes[key])
	}
	return strings.Join(merged, ",")
}

// escapeAttribute percent-encodes what the variable's W3C Baggage value syntax
// does not allow, and the percent sign itself.
func escapeAttribute(value string) string {
	var escaped strings.Builder
	for _, b := range []byte(value) {
		if b <= ' ' || b >= 0x7f || strings.IndexByte(`",;\%`, b) >= 0 {
			fmt.Fprintf(&escaped, "%%%02X", b)
			continue
		}
		escaped.WriteByte(b)
	}
	return escaped.String()
}

// ErrNoEventFile is why a run on GitHub Actions has no event to read.
var ErrNoEventFile = errors.New("GITHUB_EVENT_PATH is not set")

// Event is the part of a GitHub event file Costroid reads.
type Event struct {
	PullRequest *struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
}

// ReadEvent reads the event file at path. On a pull request GITHUB_SHA names
// the test merge commit, so this file is the only place the head appears.
func ReadEvent(path string) (Event, error) {
	if path == "" {
		return Event{}, ErrNoEventFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Event{}, fmt.Errorf("read the event file: %w", err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return Event{}, fmt.Errorf("parse the event file: %w", err)
	}
	return event, nil
}

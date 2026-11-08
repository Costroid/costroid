package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/cicontext"
	reviewcomment "github.com/Costroid/costroid/internal/review"
	"github.com/Costroid/costroid/internal/scm"
)

const reviewHelp = `Usage:
  costroid review --keep --from KIND=PATH --out FILE [--since T]
  costroid review --post --file FILE

review posts the agent's own answer as a second comment on the pull request,
apart from the report's. Nothing runs it by default: the action runs it only
with post-review: "true". docs/review.md says how to turn it on.

--keep runs in the agent's job, after the run. It reads the answer the run
ended with from PATH and writes that text, and nothing else, to --out. KIND is
one of:

  claude-stream   the stream costroid run kept from claude -p
  execution-file  the execution_file output of anthropics/claude-code-action
  codex-stream    the stream costroid run kept from codex exec
  codex-home      the codex-home given to openai/codex-action

Claude Code's answer is the result field of its last result message, and
Codex's is the last agent message of its last completed turn. Neither reads
what the agent wrote on the way. --since, with codex-home, is when the job
started, in RFC 3339: a rollout file last written before it is not read. A run
that ended without an answer, at a cap or on an error, leaves no file at
--out, and keep says why and exits 0.

--post runs in the report's job. It reads FILE as data, up to 1 MiB, and posts
one comment of its own, or edits the one an earlier run posted. The comment
fences the answer as plain text, so no link, mention, image or HTML in it
renders, cuts it to 30000 bytes and says the words are the agent's. With no
answer in FILE, it creates no comment, and edits an earlier run's to say this
run kept none. Like report, it exits 0 whatever the host answers, and outside
GitHub Actions and GitLab CI it refuses and exits 125.

Exit codes of review:
  0                 the answer was kept or posted, or there was none to keep,
                    or the host refused it
  125               costroid could not read the flags or write --out, or
                    --post found neither GitHub Actions nor GitLab CI
`

// answerSource is one kind of file an answer is read from.
type answerSource struct {
	name string
	read func(path string, since time.Time) (string, error)
	// since says the source takes --since.
	since bool
}

var answerSources = []answerSource{
	{name: "claude-stream", read: fromFile(claudecode.ReadStreamAnswer)},
	{name: "execution-file", read: fromFile(claudecode.ReadExecutionAnswer)},
	{name: "codex-stream", read: fromFile(codex.ReadStreamAnswer)},
	{name: "codex-home", read: codex.ReadHomeAnswer, since: true},
}

func fromFile(read func(io.Reader) (string, error)) func(string, time.Time) (string, error) {
	return func(path string, _ time.Time) (string, error) {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		return read(file)
	}
}

type reviewFlags struct {
	keep, post bool
	source     answerSource
	path       string
	since      time.Time
	out, file  string
}

func parseReview(args []string, stdout, stderr io.Writer) (reviewFlags, error) {
	set := flag.NewFlagSet("review", flag.ContinueOnError)
	set.SetOutput(stderr)
	keep := set.Bool("keep", false, "read the run's answer and write it to --out")
	post := set.Bool("post", false, "post the answer in --file as the review comment")
	from := set.String("from", "", "KIND=PATH, where the run left its answer")
	since := set.String("since", "", "when the job started, in RFC 3339")
	out := set.String("out", "", "the file the answer goes to")
	file := set.String("file", "", "the file the agent's job kept the answer in")
	if err := parseFlags(set, args, reviewHelp, stdout, stderr); err != nil {
		return reviewFlags{}, err
	}
	flags := reviewFlags{keep: *keep, post: *post, out: *out, file: *file}
	switch {
	case *keep == *post || set.NArg() != 0:
		return reviewFlags{}, errors.New("review takes one of --keep and --post, and no other argument")
	case *post && (*file == "" || *from != "" || *out != "" || *since != ""):
		return reviewFlags{}, errors.New("review --post takes --file alone")
	case *post:
		return flags, nil
	case *file != "" || *out == "":
		return reviewFlags{}, errors.New("review --keep needs --from KIND=PATH and --out")
	}
	kind, path, _ := strings.Cut(*from, "=")
	for _, source := range answerSources {
		if source.name == kind && path != "" {
			flags.source, flags.path = source, path
		}
	}
	if flags.path == "" {
		return reviewFlags{}, fmt.Errorf("review --keep needs --from KIND=PATH, with KIND one of %s", answerKinds())
	}
	if *since == "" {
		return flags, nil
	}
	if !flags.source.since {
		return reviewFlags{}, errors.New("review --keep takes --since only with codex-home")
	}
	at, err := time.Parse(time.RFC3339Nano, *since)
	if err != nil {
		return reviewFlags{}, fmt.Errorf("review --keep takes --since as an RFC 3339 time, not %q", *since)
	}
	flags.since = at
	return flags, nil
}

func answerKinds() string {
	names := make([]string, len(answerSources))
	for i, source := range answerSources {
		names[i] = source.name
	}
	return strings.Join(names, ", ")
}

// reviewCommand keeps the agent's answer in the agent's job, or posts it in
// the report's job.
func reviewCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parseReview(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if flags.keep {
		return keepAnswer(flags, stderr)
	}
	return postAnswer(ctx, flags.file, stderr)
}

// keepAnswer writes the run's answer to the file the action uploads. It first
// removes any file already there, so a run with no answer leaves none.
func keepAnswer(flags reviewFlags, stderr io.Writer) int {
	if err := os.Remove(flags.out); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	text, err := flags.source.read(flags.path, flags.since)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: no answer was kept: %v\n", err)
		return 0
	}
	if len(text) > reviewcomment.MaxFileBytes {
		end := reviewcomment.MaxFileBytes
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		fmt.Fprintf(stderr, "costroid: warning: the answer is %d bytes, so its first %d were kept\n", len(text), end)
		text = text[:end]
	}
	if err := os.WriteFile(flags.out, []byte(text), 0o600); err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stderr, "costroid: kept the agent's answer, %d bytes, for the review comment\n", len(text))
	return 0
}

// postAnswer posts the answer the agent's job kept as the review comment. The
// file came from that job, so it is read as data and never past MaxFileBytes.
func postAnswer(ctx context.Context, path string, stderr io.Writer) int {
	system := cicontext.Detect(os.Getenv)
	if system == cicontext.NoSystem {
		fmt.Fprintln(stderr, "costroid: review refused: neither GitHub Actions nor GitLab CI was detected")
		return exitFailure
	}
	if event := os.Getenv("GITHUB_EVENT_NAME"); system == cicontext.GitHubActions && event != reportEvent {
		fmt.Fprintf(stderr, "costroid: not posted: review posts on the pull_request event, and this run's event is %q\n", event)
		return 0
	}
	target, err := targetFromEnvironment(system, reportFlags{}, stderr)
	if err == nil && target.pull.pullRequest == 0 {
		err = errors.New("the event names no pull request")
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	answer := reviewcomment.Answer{Text: readKeptAnswer(path, stderr), Head: target.pull.head}
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	if text, _ := reviewcomment.Clean(answer.Text); text == "" {
		return replaceEarlierAnswer(ctx, target, answer, stderr)
	}
	if err := target.host.UpsertComment(ctx, target.pull.pullRequest, reviewcomment.Marker, reviewcomment.Render(answer)); err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the agent's answer was not posted: %v\n", err)
		return 0
	}
	fmt.Fprintln(stderr, "costroid: posted the agent's answer in a comment of its own")
	return 0
}

// replaceEarlierAnswer handles a run that kept no answer. It creates no
// comment, and edits the one an earlier run posted to say this run kept none,
// so an earlier answer never stands for a later commit.
func replaceEarlierAnswer(ctx context.Context, target reportTarget, answer reviewcomment.Answer, stderr io.Writer) int {
	finder, ok := target.host.(scm.CommentFinder)
	if !ok {
		fmt.Fprintln(stderr, "costroid: no answer to post, and this host cannot look for an earlier one")
		return 0
	}
	found, err := finder.HasComment(ctx, target.pull.pullRequest, reviewcomment.Marker)
	if err == nil && !found {
		fmt.Fprintln(stderr, "costroid: no answer to post, and the pull request holds no earlier answer")
		return 0
	}
	if err == nil {
		err = target.host.UpsertComment(ctx, target.pull.pullRequest, reviewcomment.Marker, reviewcomment.Render(answer))
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the earlier answer was not replaced: %v\n", err)
		return 0
	}
	fmt.Fprintln(stderr, "costroid: the earlier answer now says this run kept none")
	return 0
}

// readKeptAnswer reads the kept answer, empty when there is none to read.
func readKeptAnswer(path string, stderr io.Writer) string {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(stderr, "costroid: the agent's job kept no answer")
		return ""
	}
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("it is not a plain file")
	}
	var data []byte
	if err == nil {
		data, err = readAtMost(path, reviewcomment.MaxFileBytes)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the kept answer was not read: %v\n", err)
		return ""
	}
	return string(data)
}

func readAtMost(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

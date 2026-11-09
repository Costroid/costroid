//go:build gitlab

package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// GitLab CE 19.5.1, pushed to Docker Hub on 2026-10-21, and GitLab
	// Runner 19.5.1, pushed on 2026-10-22, each pinned by its digest.
	gitLabImage = "gitlab/gitlab-ce:19.5.1-ce.0@sha256:bb79dc86267c557fd11e7feb0cd9dd506dd02e991ecdd6cae11ca7d7e5d36fc3"
	runnerImage = "gitlab/gitlab-runner:ubuntu-v19.5.1@sha256:8e049fb719162488893a9eccc48f3a05259d02f2386dfbb20f3e379a50575afd"

	// label marks every container the test starts.
	label = "costroid-gitlab-e2e"

	rootTokenFile = "/tmp/costroid-e2e-root-token"
	rootTokenLog  = "/var/log/gitlab/costroid-e2e-root-token.log"
	// rootTokenScript runs testdata/root-token.rb once GitLab's database is
	// up, beside the web server's start, and ends its log with the exit code.
	rootTokenScript = "{ gitlab-rails runner /costroid-e2e/root-token.rb; echo \"exit $?\"; } >" + rootTokenLog + " 2>&1 &"

	// cleanupReserve is the time the test keeps, before go test's deadline,
	// to remove its containers.
	cleanupReserve = 90 * time.Second
	// startLimit bounds GitLab's first start, which took about 4 minutes in
	// each run on 2026-11-09.
	startLimit = 20 * time.Minute
	pollEvery  = 2 * time.Second
)

// omnibusConfig keeps one Puma process and leaves out the services the run
// does not use, to shorten the start.
const omnibusConfig = `external_url 'http://127.0.0.1'
puma['worker_processes'] = 0
sidekiq['concurrency'] = 5
prometheus_monitoring['enable'] = false
gitlab_kas['enable'] = false
registry['enable'] = false
`

// instance is GitLab CE and one runner, two containers with no network but
// loopback. The runner shares GitLab's network namespace, so each reaches
// the other at 127.0.0.1, and the test reaches GitLab through docker exec.
// Every directory either writes to is a tmpfs: the test makes no volume and
// no network.
type instance struct {
	t      *testing.T
	gitlab string
	runner string
	until  time.Time
}

// start runs both containers and waits for GitLab's API. Their removal is
// registered first, so it runs on every way out of the test.
func start(t *testing.T, binary string) *instance {
	t.Helper()
	sweep(t)
	name := fmt.Sprintf("%s-%d", label, os.Getpid())
	in := &instance{t: t, gitlab: name + "-ce", runner: name + "-runner", until: deadline(t)}
	t.Cleanup(in.remove)
	script, err := filepath.Abs("testdata/root-token.rb")
	if err != nil {
		t.Fatal(err)
	}
	in.run(in.gitlab, "--network", "none", "--shm-size", "256m",
		"--tmpfs", "/etc/gitlab:rw,size=64m",
		"--tmpfs", "/var/log/gitlab:rw,size=1g",
		"--tmpfs", "/var/opt/gitlab:rw,exec,size=8g",
		"--volume", script+":/costroid-e2e/root-token.rb:ro",
		"--env", "GITLAB_OMNIBUS_CONFIG="+omnibusConfig,
		"--env", "GITLAB_POST_RECONFIGURE_SCRIPT="+rootTokenScript,
		"--env", "GITLAB_SKIP_TAIL_LOGS=true",
		"--env", "GITLAB_DISABLE_OPENSSH=true",
		gitLabImage)
	in.run(in.runner, "--network", "container:"+in.gitlab,
		"--tmpfs", "/etc/gitlab-runner:rw,size=16m",
		"--tmpfs", "/home/gitlab-runner:rw,exec,size=1g,mode=0700,uid=999,gid=999",
		"--volume", binary+":/usr/local/bin/costroid:ro",
		runnerImage)
	return in
}

// deadline is go test's, less the time the removal needs.
func deadline(t *testing.T) time.Time {
	until, ok := t.Deadline()
	if !ok {
		return time.Now().Add(startLimit + 10*time.Minute)
	}
	return until.Add(-cleanupReserve)
}

func (in *instance) run(name string, args ...string) {
	in.t.Helper()
	run := append([]string{"run", "--detach", "--name", name, "--label", label + "=" + name}, args...)
	if out, err := docker(run...); err != nil {
		in.t.Fatalf("start %s: %v\n%s", name, err, out)
	}
	in.noVolume(name)
}

// noVolume fails the test when a container holds a Docker volume, which
// would outlive it on disk unless removed by name.
func (in *instance) noVolume(name string) {
	in.t.Helper()
	out, err := docker("inspect", "--format", `{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}`, name)
	if err != nil {
		in.t.Fatalf("inspect %s: %v\n%s", name, err, out)
	}
	if volumes := strings.TrimSpace(string(out)); volumes != "" {
		in.t.Fatalf("%s holds the volumes %s", name, volumes)
	}
}

// sweep removes the containers of a run that go test killed before its
// cleanup ran. The e2e lock keeps two runs from overlapping.
func sweep(t *testing.T) {
	t.Helper()
	for _, name := range leftovers(t, label) {
		t.Logf("removing %s, left by an earlier run", name)
		if out, err := docker("rm", "--force", "--volumes", name); err != nil {
			t.Fatalf("remove %s: %v\n%s", name, err, out)
		}
	}
}

func leftovers(t *testing.T, filter string) []string {
	t.Helper()
	out, err := docker("ps", "--all", "--filter", "label="+filter, "--format", "{{.Names}}")
	if err != nil {
		t.Fatalf("list containers: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

// remove deletes both containers, and any anonymous volume with them, then
// checks that Docker holds no container of this run.
func (in *instance) remove() {
	if in.t.Failed() {
		in.logTail(in.runner, "logs", "--tail", "40", in.runner)
		in.logTail(in.gitlab, "exec", in.gitlab, "tail", "-n", "20", rootTokenLog)
	}
	for _, name := range []string{in.runner, in.gitlab} {
		out, err := docker("rm", "--force", "--volumes", name)
		if err != nil && !strings.Contains(string(out), "No such container") {
			in.t.Errorf("remove %s: %v\n%s", name, err, out)
		}
	}
	for _, name := range []string{in.runner, in.gitlab} {
		if left := leftovers(in.t, label+"="+name); len(left) > 0 {
			in.t.Errorf("still in Docker after the removal: %v", left)
		}
	}
}

func (in *instance) logTail(name string, args ...string) {
	if out, err := docker(args...); err == nil {
		in.t.Logf("%s:\n%s", name, out)
	}
}

// docker runs the docker command and returns its combined output.
func docker(args ...string) ([]byte, error) {
	return exec.Command("docker", args...).CombinedOutput()
}

// exec runs a command inside a container and returns its standard output.
// Each variable in env is passed by name, so its value travels in the docker
// command's environment and never in an argument a process list shows.
func (in *instance) exec(container string, env []string, args ...string) (string, error) {
	full := []string{"exec"}
	for _, variable := range env {
		name, _, _ := strings.Cut(variable, "=")
		full = append(full, "--env", name)
	}
	command := exec.Command("docker", append(append(full, container), args...)...)
	command.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("docker exec %s %s: %w: %s", container, args[0], err, stderr.String())
	}
	return stdout.String(), nil
}

// await calls ready every pollEvery until it reports true, and fails the test
// at until with what it last said.
func await(t *testing.T, what string, until time.Time, ready func() (bool, string)) {
	t.Helper()
	for {
		done, last := ready()
		if done {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("%s: gave up at %s: %s", what, until.Format(time.RFC3339), last)
		}
		time.Sleep(pollEvery)
	}
}

// rootToken waits for the token the start script writes and for the API to
// take it.
func (in *instance) rootToken() *client {
	in.t.Helper()
	until := in.until
	if limit := time.Now().Add(startLimit); limit.Before(until) {
		until = limit
	}
	began := time.Now()
	var token string
	await(in.t, "GitLab's first token", until, func() (bool, string) {
		out, err := in.exec(in.gitlab, nil, "cat", rootTokenFile)
		if err == nil {
			token = strings.TrimSpace(out)
			return token != "", "the file is empty"
		}
		if log, _ := in.exec(in.gitlab, nil, "cat", rootTokenLog); failed(log) {
			in.t.Fatalf("testdata/root-token.rb made no token:\n%s", log)
		}
		return false, err.Error()
	})
	root := in.client(token)
	await(in.t, "GitLab's API", until, func() (bool, string) {
		status, body := root.call(http.MethodGet, "/version", nil, nil)
		return status == http.StatusOK, fmt.Sprintf("status %d: %s", status, body)
	})
	in.t.Logf("GitLab answered %s after it was started", time.Since(began).Round(time.Second))
	return root
}

// failed reports a log whose last line is a nonzero exit code.
func failed(log string) bool {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	last := lines[len(lines)-1]
	return strings.HasPrefix(last, "exit ") && last != "exit 0"
}

// dial opens a connection to GitLab's port 80 through docker exec and nc,
// because the container has no network the host can reach.
func (in *instance) dial(context.Context, string, string) (net.Conn, error) {
	command := exec.Command("docker", "exec", "--interactive", in.gitlab, "nc", "127.0.0.1", "80")
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("dial GitLab: %w", err)
	}
	return &execConn{command: command, stdin: stdin, stdout: stdout}, nil
}

// execConn is one connection carried by a docker exec process's pipes.
type execConn struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
}

func (c *execConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *execConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *execConn) Close() error {
	_ = c.stdin.Close()
	_ = c.command.Process.Kill()
	_ = c.command.Wait()
	return nil
}

func (c *execConn) LocalAddr() net.Addr              { return execAddr{} }
func (c *execConn) RemoteAddr() net.Addr             { return execAddr{} }
func (c *execConn) SetDeadline(time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(time.Time) error { return nil }

type execAddr struct{}

func (execAddr) Network() string { return "docker-exec" }
func (execAddr) String() string  { return "gitlab:80" }

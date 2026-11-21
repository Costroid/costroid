package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/agenttest"
)

// TestMain runs the test binary as a pack's child when a runner starts it as
// one. The child first refuses, with exit code 3, if anything of its
// parent's reached it: a variable of the environment, a file in its working
// directory, or a descriptor beyond its pipes. Started under a name in
// fakeChildren, it then answers as that child does. Otherwise it records, as
// costroid's main does, that this executable hands a child to the pack, and
// runs the tests with a stub first on PATH for every agent, since the package
// starts processes.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == PackChildArgument {
		if problem := inheritedProblem(); problem != "" {
			fmt.Fprintln(os.Stderr, problem)
			os.Exit(3)
		}
		if child, fake := fakeChildren[filepath.Base(os.Args[0])]; fake {
			child()
			os.Exit(0)
		}
		code, _ := PackChild()
		os.Exit(code)
	}
	PackChild()
	os.Exit(agenttest.Main(m.Run))
}

// fakeChildren are children that answer what the engine never would.
var fakeChildren = map[string]func(){
	"pass-child": func() {
		os.Stdout.WriteString(`{"steps":1,"results":[{"rule":"team_rule","outcome":"pass","reason":"within_team_limit"}]}` + "\n")
	},
	"garbage-child": func() {
		os.Stdout.WriteString("success\n")
	},
	"flood-child": func() {
		os.Stdout.WriteString(`{"steps":1,"results":[]}`)
		os.Stdout.Write(bytes.Repeat([]byte(" "), 1<<20))
	},
	"crash-child": func() {
		panic("the child crashed")
	},
	"hang-child": func() {
		time.Sleep(time.Hour)
	},
	// lying-invalid-child says the pack is not valid when it is handed rows,
	// and answers no result when it is handed none.
	"lying-invalid-child": func() {
		var request childRequest
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			panic(err)
		}
		if string(request.Input) == "{}" {
			os.Stdout.WriteString(`{"steps":1,"results":[]}` + "\n")
			return
		}
		os.Stdout.WriteString(`{"steps":0,"fault":{"reason":"pack_invalid","line":1,"error":"forged"}}` + "\n")
	},
}

// inheritedProblem says what of the parent's the child can see, or nothing.
// The child's own runtime holds epoll and event descriptors, and keeps its
// cgroup's cpu.max open to set GOMAXPROCS; any other descriptor beyond the
// pipes came from the parent.
func inheritedProblem() string {
	if environment := os.Environ(); len(environment) > 0 {
		return fmt.Sprintf("the child's environment holds %d variables", len(environment))
	}
	directory, err := os.Getwd()
	if err != nil {
		return err.Error()
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) > 0 || directory == "/" {
		return fmt.Sprintf("the child's working directory %s is not empty: %v", directory, err)
	}
	descriptors, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return ""
	}
	for _, entry := range descriptors {
		descriptor, _ := strconv.Atoi(entry.Name())
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			// The listing's own descriptor, closed once it was read.
			continue
		}
		runtime := strings.HasPrefix(target, "anon_inode:") || (strings.HasPrefix(target, "/sys/fs/cgroup/") && strings.HasSuffix(target, "/cpu.max"))
		if descriptor > 2 && !runtime {
			return fmt.Sprintf("the child holds descriptor %d, %s", descriptor, target)
		}
	}
	return ""
}

// livingChildren are the processes whose parent is this one, from /proc.
func livingChildren(t *testing.T) []string {
	t.Helper()
	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil || len(stats) == 0 {
		t.Skip("this system has no /proc to list processes from")
	}
	var children []string
	for _, path := range stats {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		end := bytes.LastIndex(data, []byte(") "))
		fields := strings.Fields(string(data[end+1:]))
		if end > 0 && len(fields) > 1 && fields[1] == strconv.Itoa(os.Getpid()) {
			children = append(children, string(data))
		}
	}
	return children
}

// fakeRunner is a runner whose child is the test binary under name.
func fakeRunner(t *testing.T, name string) packRunner {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	return packRunner{executable: link, steps: maxPackSteps, time: maxPackTime}
}

// childRunner is the runner RunCustomerPack builds.
func childRunner(t testing.TB) packRunner {
	t.Helper()
	executable, err := selfExecutable()
	if err != nil {
		t.Fatal(err)
	}
	return packRunner{executable: executable, steps: maxPackSteps, time: maxPackTime}
}

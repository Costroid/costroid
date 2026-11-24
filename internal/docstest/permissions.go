package docstest

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Manifest is permissions.json: every call Costroid makes to a host under a
// token, the steps of other publishers that need a permission, and what each
// recipe's job and the GitHub App ask of their tokens.
type Manifest struct {
	Calls  []Call  `json:"calls"`
	Steps  []Step  `json:"steps"`
	Local  []Local `json:"local_commands"`
	Held   []Held  `json:"held"`
	Grants []Grant `json:"grants"`
}

// Held is what a token holds without asking for it, and what that rests on.
type Held struct {
	Token       string            `json:"token"`
	Permissions map[string]string `json:"permissions"`
	Why         string            `json:"why"`
	Source      *Source           `json:"source"`
}

// HeldBy answers the permissions token holds without asking for them.
func (m Manifest) HeldBy(token string) map[string]string {
	held := map[string]string{}
	for _, entry := range m.Held {
		if entry.Token == token {
			maps.Copy(held, entry.Permissions)
		}
	}
	return held
}

// Source is what a call's permissions rest on, and the day it was read: the
// publisher's own page, with its words, or a recorded GitHub Actions run, with
// what it answered.
type Source struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
	Run  string `json:"run,omitempty"`
	Read string `json:"read"`
	Says string `json:"says"`
}

// Local is a gh or git command Costroid runs on the machine it runs on, under
// the person's own sign-in or, for a public read, under none.
type Local struct {
	ID       string   `json:"id"`
	Command  string   `json:"command"`
	SignedIn bool     `json:"signed_in"`
	Access   string   `json:"access"`
	What     string   `json:"what"`
	Why      string   `json:"why"`
	By       []string `json:"by"`
	Commands []string `json:"commands"`
}

// Call is one method and path sent to a host.
type Call struct {
	ID     string `json:"id"`
	Host   string `json:"host"`
	Base   string `json:"base"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Access string `json:"access"`
	// Permissions maps each permission the call needs to its level.
	Permissions map[string]string `json:"permissions"`
	What        string            `json:"what"`
	Why         string            `json:"why"`
	// By names the code that sends the request, and Commands what a recipe
	// or a person runs that makes it.
	By       []string `json:"by"`
	Commands []string `json:"commands"`
	// Source is what the permissions rest on, or Unverified says why nothing
	// this file quotes states them.
	Source     *Source `json:"source,omitempty"`
	Unverified string  `json:"unverified,omitempty"`
}

// Step is a step from another publisher that needs a permission, when its
// Input is set or, with no Input, always.
type Step struct {
	Uses        string            `json:"uses"`
	Input       string            `json:"input,omitempty"`
	Permissions map[string]string `json:"permissions"`
	What        string            `json:"what"`
	Why         string            `json:"why"`
	Source      *Source           `json:"source,omitempty"`
	Unverified  string            `json:"unverified,omitempty"`
}

// Grant is what one recipe's job, or the GitHub App, asks of its token.
type Grant struct {
	Name  string `json:"name"`
	File  string `json:"file,omitempty"`
	Job   string `json:"job,omitempty"`
	Token string `json:"token"`
	Role  string `json:"role,omitempty"`
	// Runs names every command, script and step of the grant that makes a
	// call or needs a permission.
	Runs        []string           `json:"runs"`
	Permissions map[string]Granted `json:"permissions"`
}

// Granted is one permission a grant asks for, and the calls and steps that
// need it. Excess says why the level is more than any of them needs.
type Granted struct {
	Level  string   `json:"level"`
	Calls  []string `json:"calls"`
	Steps  []string `json:"steps"`
	Excess string   `json:"excess,omitempty"`
}

// ManifestFile and ManifestSchema are the manifest and its JSON Schema, from
// the repository's root.
const (
	ManifestFile   = "permissions.json"
	ManifestSchema = "permissions.schema.json"
)

// Permissions reads permissions.json in the repository at root, and fails
// the test unless the file holds to its schema and names each call once.
func Permissions(t testing.TB, root string) Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: %v", ManifestFile, err)
	}
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(filepath.Join(root, ManifestSchema))
	if err != nil {
		t.Fatalf("%s: %v", ManifestSchema, err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s does not hold to %s: %v", ManifestFile, ManifestSchema, err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("%s: %v", ManifestFile, err)
	}
	ids := map[string]bool{}
	for _, call := range manifest.Calls {
		if ids[call.ID] {
			t.Errorf("%s names the call %s twice", ManifestFile, call.ID)
		}
		ids[call.ID] = true
	}
	return manifest
}

// Call answers the call whose id is id, and false when there is none.
func (m Manifest) Call(id string) (Call, bool) {
	for _, call := range m.Calls {
		if call.ID == id {
			return call, true
		}
	}
	return Call{}, false
}

// placeholder is {name} for one path segment or {+name} for one or more.
var placeholder = regexp.MustCompile(`\{(\+?)[A-Za-z_]+\}`)

// Matches reports whether a request of method to path, after the call's
// base and without its query, is this call.
func (c Call) Matches(method, path string) bool {
	if method != c.Method {
		return false
	}
	var pattern strings.Builder
	last := 0
	for _, found := range placeholder.FindAllStringSubmatchIndex(c.Path, -1) {
		pattern.WriteString(regexp.QuoteMeta(c.Path[last:found[0]]))
		if found[3] > found[2] {
			pattern.WriteString(`[^/]+(/[^/]+)*`)
		} else {
			pattern.WriteString(`[^/]+`)
		}
		last = found[1]
	}
	pattern.WriteString(regexp.QuoteMeta(c.Path[last:]))
	return regexp.MustCompile("^" + pattern.String() + "$").MatchString(path)
}

// Under answers the calls on host and the steps that what a grant runs
// makes, in the manifest's order.
func (m Manifest) Under(grant Grant, host string) ([]Call, []Step) {
	var calls []Call
	for _, call := range m.Calls {
		if call.Host == host && slices.ContainsFunc(call.Commands, func(command string) bool { return slices.Contains(grant.Runs, command) }) {
			calls = append(calls, call)
		}
	}
	var steps []Step
	for _, step := range m.Steps {
		if slices.Contains(grant.Runs, step.Uses) {
			steps = append(steps, step)
		}
	}
	return calls, steps
}

// Problems holds a grant to asked, what its recipe or the App asks of the
// token, and to the calls and steps under it, and answers each way they
// differ. held is what the token holds without asking, such as the metadata
// every workflow token reads.
func (m Manifest) Problems(grant Grant, host string, asked, held map[string]string) []string {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(asked)) {
		if stated := grant.Permissions[name]; stated.Level != asked[name] {
			problems = append(problems, fmt.Sprintf("%s asks %s: %s, and %s states it asks %s", grant.Name, name, asked[name], ManifestFile, cmp.Or(stated.Level, "nothing")))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(grant.Permissions)) {
		if _, found := asked[name]; !found {
			problems = append(problems, fmt.Sprintf("%s states %s for %s, which does not ask it", ManifestFile, name, grant.Name))
		}
	}
	calls, steps := m.Under(grant, host)
	needed := map[string]int{}
	for _, permissions := range slices.Concat(callPermissions(calls), stepPermissions(steps)) {
		for name, level := range permissions {
			needed[name] = max(needed[name], Rank(level))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(asked)) {
		excess := grant.Permissions[name].Excess
		switch over := Rank(asked[name]) > needed[name]; {
		case over && excess == "":
			problems = append(problems, fmt.Sprintf("%s asks %s: %s, and no call or step under it needs more than %s", grant.Name, name, asked[name], levelName(needed[name])))
		case !over && excess != "":
			problems = append(problems, fmt.Sprintf("%s states an excess for %s on %s, and a call under it needs %s: %s", ManifestFile, name, grant.Name, name, asked[name]))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(needed)) {
		if have := max(Rank(asked[name]), Rank(held[name])); have < needed[name] {
			problems = append(problems, fmt.Sprintf("a call or step under %s needs %s: %s, and it holds %s", grant.Name, name, levelName(needed[name]), levelName(have)))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(grant.Permissions)) {
		stated := grant.Permissions[name]
		var wantCalls, wantSteps []string
		for _, call := range calls {
			if _, found := call.Permissions[name]; found {
				wantCalls = append(wantCalls, call.ID)
			}
		}
		for _, step := range steps {
			if _, found := step.Permissions[name]; found {
				wantSteps = append(wantSteps, step.Uses)
			}
		}
		if !slices.Equal(stated.Calls, wantCalls) || !slices.Equal(stated.Steps, wantSteps) {
			problems = append(problems, fmt.Sprintf("%s lists calls %v and steps %v under %s for %s, and the calls and steps that need it are %v and %v", ManifestFile, stated.Calls, stated.Steps, name, grant.Name, wantCalls, wantSteps))
		}
	}
	return problems
}

func callPermissions(calls []Call) []map[string]string {
	permissions := make([]map[string]string, 0, len(calls))
	for _, call := range calls {
		permissions = append(permissions, call.Permissions)
	}
	return permissions
}

func stepPermissions(steps []Step) []map[string]string {
	permissions := make([]map[string]string, 0, len(steps))
	for _, step := range steps {
		permissions = append(permissions, step.Permissions)
	}
	return permissions
}

// levelName is the level Rank gives rank.
func levelName(rank int) string {
	return [...]string{"nothing", "read", "write"}[rank]
}

// Rank orders the levels: none, read, then write.
func Rank(level string) int {
	switch level {
	case "read":
		return 1
	case "write":
		return 2
	}
	return 0
}

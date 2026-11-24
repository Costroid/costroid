package docstest

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/permissions.md from permissions.json")

const (
	root         = "../.."
	page         = "docs/permissions.md"
	firstHeading = "## Recipes\n"
	lastHeading  = "## Sources\n"
)

// TestTheScopePageIsTheManifest holds docs/permissions.md, from its
// Recipes heading to its Sources heading, to the tables permissions.json
// gives: for each recipe's job and for the GitHub App, what each permission
// lets it read and change, then every call. -update rewrites that part.
func TestTheScopePageIsTheManifest(t *testing.T) {
	manifest := Permissions(t, root)
	text := Page(t, root, page)
	start, end := strings.Index(text, firstHeading), strings.Index(text, lastHeading)
	if start < 0 || end < start {
		t.Fatalf("%s holds no %q heading followed by a %q heading", page, strings.TrimSpace(firstHeading), strings.TrimSpace(lastHeading))
	}
	want := renderPermissions(manifest)
	if *update {
		text = text[:start] + want + text[end:]
		end = start + len(want)
		if err := os.WriteFile(filepath.Join(root, page), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if text[start:end] != want {
		t.Errorf("%s is not what %s gives, rerun with -update. It gives:\n%s", page, ManifestFile, want)
	}
	holdTokenSetup(t, manifest)
	holdAppSetup(t, manifest)
}

// appSetup is the page that registers the GitHub App, and appRow one row of
// its table of permissions: the name as an App writes it, and the level.
const appSetup = "docs/github-app.md"

var appRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` +\\| (read|write) +\\|")

// holdAppSetup holds the permissions the App's setup page names to what the
// manifest states the App asks.
func holdAppSetup(t *testing.T, m Manifest) {
	t.Helper()
	stated := map[string]string{}
	for _, row := range appRow.FindAllStringSubmatch(Page(t, root, appSetup), -1) {
		stated[strings.ReplaceAll(row[1], "_", "-")] = row[2]
	}
	for _, grant := range m.Grants {
		if grant.File != "" {
			continue
		}
		asked := map[string]string{}
		for name, granted := range grant.Permissions {
			asked[name] = granted.Level
		}
		if !maps.Equal(stated, asked) {
			t.Errorf("%s names the permissions %v, and %s states the App asks %v", appSetup, stated, ManifestFile, asked)
		}
	}
}

// tokenSetup is the page that tells a person to create a GitLab token.
const tokenSetup = "docs/recipes.md"

// holdTokenSetup holds the role and the scope the setup page tells a person to
// give a GitLab token to what the manifest grants it.
func holdTokenSetup(t *testing.T, m Manifest) {
	t.Helper()
	setup := Page(t, root, tokenSetup)
	for _, grant := range m.Grants {
		if grant.Role == "" {
			continue
		}
		levels := map[string]string{}
		for name, granted := range grant.Permissions {
			levels[name] = granted.Level
		}
		if told := fmt.Sprintf("with the %s role and the %s", grant.Role, needs(levels)); !strings.Contains(setup, told) {
			t.Errorf("%s does not tell a person to create %s %s, as %s grants it", tokenSetup, grant.Token, told, ManifestFile)
		}
	}
}

func renderPermissions(m Manifest) string {
	var out strings.Builder
	out.WriteString(firstHeading + "\n")
	for _, held := range m.Held {
		fmt.Fprintf(&out, "%s Basis: %s.\n\n", held.Why, basis(held.Source, ""))
	}
	var files []string
	for _, grant := range m.Grants {
		if grant.File != "" && !slices.Contains(files, grant.File) {
			files = append(files, grant.File)
		}
	}
	for _, file := range files {
		fmt.Fprintf(&out, "### `%s`\n\n", filepath.Base(file))
		rows := [][]string{{"Job", "Permission", "Can read", "Can change"}}
		var notes []string
		for _, grant := range m.Grants {
			if grant.File != file {
				continue
			}
			for _, row := range grantRows(m, grant) {
				rows = append(rows, append([]string{"`" + grant.Job + "`"}, row...))
			}
			notes = append(notes, grantNotes(grant)...)
		}
		writeTable(&out, rows)
		writeNotes(&out, notes)
	}
	out.WriteString("## The GitHub App\n\n")
	for _, grant := range m.Grants {
		if grant.File == "" {
			writeTable(&out, slices.Concat([][]string{{"Permission", "Can read", "Can change"}}, grantRows(m, grant)))
			writeNotes(&out, grantNotes(grant))
		}
	}
	out.WriteString("## Every call\n\n")
	rows := [][]string{{"Call", "Host", "Needs", "Made by", "Why", "Basis"}}
	for _, call := range m.Calls {
		rows = append(rows, []string{callName(call), hostName(call.Host), needs(call.Permissions), codeList(call.Commands), call.Why, basis(call.Source, call.Unverified)})
	}
	writeTable(&out, rows)
	out.WriteString("## Steps of other publishers\n\n")
	rows = [][]string{{"Step", "Needs", "Why", "Basis"}}
	for _, step := range m.Steps {
		name := "`" + step.Uses + "`"
		if step.Input != "" {
			name += fmt.Sprintf(" with `%s`", step.Input)
		}
		rows = append(rows, []string{name, needs(step.Permissions), step.Why, basis(step.Source, step.Unverified)})
	}
	writeTable(&out, rows)
	out.WriteString("## Through gh and git\n\n")
	rows = [][]string{{"Command", "Signed in", "Can read", "Can change", "Made by", "Why"}}
	for _, local := range m.Local {
		signedIn, read, changed := "as you", local.What, "nothing"
		if !local.SignedIn {
			signedIn = "no"
		}
		if local.Access == "write" {
			read, changed = "nothing", local.What
		}
		rows = append(rows, []string{"`" + local.Command + "`", signedIn, read, changed, codeList(local.Commands), local.Why})
	}
	writeTable(&out, rows)
	return out.String()
}

// grantRows are a grant's permissions, each with what the calls and steps
// under it read and change through it. A recipe's job also shows the
// metadata every workflow token reads, where a call reads it.
func grantRows(m Manifest, grant Grant) [][]string {
	host := "github"
	if grant.Role != "" {
		host = "gitlab"
	}
	calls, steps := m.Under(grant, host)
	levels := map[string]string{}
	for name, granted := range grant.Permissions {
		levels[name] = granted.Level
	}
	var rows [][]string
	for _, name := range slices.Sorted(maps.Keys(withHeld(m, levels, grant, calls))) {
		var read, changed []string
		for _, call := range calls {
			if _, needed := call.Permissions[name]; !needed {
				continue
			}
			if call.Access == "write" {
				changed = appendNew(changed, call.What)
			} else {
				read = appendNew(read, call.What)
			}
		}
		for _, step := range steps {
			if _, needed := step.Permissions[name]; needed {
				read = appendNew(read, step.What)
			}
		}
		permission := needs(map[string]string{name: levels[name]})
		if _, asked := grant.Permissions[name]; !asked {
			permission += ", held unasked"
		}
		rows = append(rows, []string{permission, orNothing(read), orNothing(changed)})
	}
	return rows
}

// withHeld adds what a recipe job's token holds without asking, where a call
// under it needs it.
func withHeld(m Manifest, levels map[string]string, grant Grant, calls []Call) map[string]string {
	if grant.File == "" {
		return levels
	}
	for name, level := range m.HeldBy(grant.Token) {
		for _, call := range calls {
			if _, needed := call.Permissions[name]; needed {
				levels[name] = level
			}
		}
	}
	return levels
}

func grantNotes(grant Grant) []string {
	var notes []string
	if grant.Role != "" {
		notes = append(notes, fmt.Sprintf("The `%s` job's token is `%s`, and its user holds the %s role.", grant.Job, grant.Token, grant.Role))
	}
	for _, name := range slices.Sorted(maps.Keys(grant.Permissions)) {
		if excess := grant.Permissions[name].Excess; excess != "" {
			notes = append(notes, fmt.Sprintf("%s asks for %s. %s", grantSubject(grant), needs(map[string]string{name: grant.Permissions[name].Level}), excess))
		}
	}
	return notes
}

// grantSubject names a grant at the start of a sentence: a recipe's job, or
// the App.
func grantSubject(grant Grant) string {
	if grant.Job != "" {
		return fmt.Sprintf("The `%s` job", grant.Job)
	}
	return strings.ToUpper(grant.Name[:1]) + grant.Name[1:]
}

// basis says what a call's or a step's permissions rest on, or that nothing
// states them.
func basis(source *Source, unverified string) string {
	switch {
	case source == nil:
		return "unverified. " + unverified
	case source.Kind == "run":
		return fmt.Sprintf("a recorded GitHub Actions run %s, read %s", source.Run, source.Read)
	}
	return fmt.Sprintf("[%s](%s), read %s", sourceName(source.URL), source.URL, source.Read)
}

func sourceName(url string) string {
	if strings.Contains(url, "gitlab.com") {
		return "GitLab"
	}
	return "GitHub"
}

func writeNotes(out *strings.Builder, notes []string) {
	for _, note := range notes {
		out.WriteString(note + "\n\n")
	}
}

func appendNew(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}

func orNothing(items []string) string {
	if len(items) == 0 {
		return "nothing"
	}
	return strings.Join(items, "; ")
}

// needs names permissions as a reader sets them: a workflow's permission and
// level, or GitLab's token scope.
func needs(permissions map[string]string) string {
	if len(permissions) == 0 {
		return "the App's own signed token"
	}
	var names []string
	for _, name := range slices.Sorted(maps.Keys(permissions)) {
		switch {
		case name == "api" && permissions[name] == "read":
			names = append(names, "`read_api` scope")
		case name == "api":
			names = append(names, "`api` scope")
		default:
			names = append(names, fmt.Sprintf("`%s: %s`", name, permissions[name]))
		}
	}
	return strings.Join(names, ", ")
}

func callName(call Call) string {
	if call.Base != "api" {
		return fmt.Sprintf("`%s $%s%s`", call.Method, call.Base, call.Path)
	}
	return fmt.Sprintf("`%s %s`", call.Method, call.Path)
}

func hostName(host string) string {
	return map[string]string{"github": "GitHub", "gitlab": "GitLab"}[host]
}

func codeList(items []string) string {
	quoted := make([]string, len(items))
	for index, item := range items {
		quoted[index] = "`" + item + "`"
	}
	return strings.Join(quoted, ", ")
}

// writeTable writes rows as a Markdown table, the first row its header, each
// column padded to its widest cell.
func writeTable(out *strings.Builder, rows [][]string) {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for index, cell := range row {
			widths[index] = max(widths[index], len([]rune(cell)))
		}
	}
	line := func(cells []string) {
		for index, cell := range cells {
			fmt.Fprintf(out, "| %s%s ", cell, strings.Repeat(" ", widths[index]-len([]rune(cell))))
		}
		out.WriteString("|\n")
	}
	line(rows[0])
	rule := make([]string, len(widths))
	for index, width := range widths {
		rule[index] = strings.Repeat("-", width)
	}
	line(rule)
	for _, row := range rows[1:] {
		line(row)
	}
	out.WriteString("\n")
}

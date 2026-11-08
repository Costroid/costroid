package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// standardLibrary names the Go standard library where a module path stands.
const standardLibrary = "Go standard library"

// A module is one module the binary links: the standard library or a
// dependency, with its directory and the directories of its linked packages.
type module struct {
	path, version string
	dir           string
	packageDirs   []string
	// boringCrypto is set for the standard library of a binary built with
	// GOEXPERIMENT=boringcrypto, which links a BoringSSL object.
	boringCrypto bool
}

// boringLicence is the SHA-256 of src/crypto/internal/boring/LICENSE as Go
// 1.27 ships it, tidied. It puts the directory's Go source under Go's own
// licence and gives BoringSSL's licences for the object that only a
// boringcrypto build links.
const boringLicence = "f9a80abe24446ec19cdf96bfba69f1dca4eecc8c9059699500ec1401940e17a8"

func (m module) String() string {
	if m.version == "" {
		return m.path
	}
	return m.path + " " + m.version
}

// listedPackage is the part of go list's output the generator reads.
type listedPackage struct {
	Dir      string
	Standard bool
	Module   *struct {
		Path, Version, Dir string
		Main               bool
	}
}

// linkedModules reads the modules a binary links from its build information,
// and asks go list, for the binary's platform, where each module and each of
// its linked packages lies. The two lists must agree.
func linkedModules(ctx context.Context, binary string) ([]module, error) {
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return nil, fmt.Errorf("read the build information of %s: %w", binary, err)
	}
	env := os.Environ()
	listArgs := []string{"list", "-deps", "-json=Dir,Standard,Module"}
	std := &module{path: standardLibrary}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT":
			env = append(env, setting.Key+"="+setting.Value)
		case "-tags":
			listArgs = append(listArgs, "-tags="+setting.Value)
		}
		if setting.Key == "GOEXPERIMENT" {
			std.boringCrypto = slices.Contains(strings.Split(setting.Value, ","), "boringcrypto")
		}
	}
	packages, err := listPackages(ctx, env, append(listArgs, info.Path)...)
	if err != nil {
		return nil, err
	}
	if std.dir, err = goEnv(ctx, "GOROOT", env); err != nil {
		return nil, err
	}
	byPath := map[string]*module{standardLibrary: std}
	for _, p := range packages {
		switch {
		case p.Standard:
			std.packageDirs = append(std.packageDirs, p.Dir)
		case p.Module == nil:
			return nil, fmt.Errorf("go list gives the package in %s no module", p.Dir)
		case p.Module.Main:
		default:
			m, ok := byPath[p.Module.Path]
			if !ok {
				m = &module{path: p.Module.Path, version: p.Module.Version, dir: p.Module.Dir}
				byPath[p.Module.Path] = m
			}
			m.packageDirs = append(m.packageDirs, p.Dir)
		}
	}
	modules := []module{*std}
	for _, dep := range info.Deps {
		m, ok := byPath[dep.Path]
		if !ok {
			return nil, fmt.Errorf("the binary links %s %s, and go list finds none of its packages; build the binary from this tree", dep.Path, dep.Version)
		}
		if m.version != dep.Version {
			return nil, fmt.Errorf("the binary links %s %s, and go list finds %s; build the binary from this tree", dep.Path, dep.Version, m.version)
		}
		if m.dir == "" {
			return nil, fmt.Errorf("%s is not in the module cache; run go mod download", m)
		}
		modules = append(modules, *m)
		delete(byPath, dep.Path)
	}
	delete(byPath, standardLibrary)
	if len(byPath) > 0 {
		extra := slices.Sorted(maps.Keys(byPath))
		return nil, fmt.Errorf("go list finds packages of %s, which the binary does not link; build the binary from this tree", strings.Join(extra, ", "))
	}
	slices.SortFunc(modules[1:], func(a, b module) int { return strings.Compare(a.path, b.path) })
	return modules, nil
}

func listPackages(ctx context.Context, env []string, args ...string) ([]listedPackage, error) {
	output, err := goCommand(ctx, env, args...)
	if err != nil {
		return nil, err
	}
	var packages []listedPackage
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var p listedPackage
		err := decoder.Decode(&p)
		if errors.Is(err, io.EOF) {
			return packages, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read go list's output: %w", err)
		}
		packages = append(packages, p)
	}
}

func goEnv(ctx context.Context, name string, env []string) (string, error) {
	output, err := goCommand(ctx, env, "env", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func goCommand(ctx context.Context, env []string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "go", args...)
	command.Env = env
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

// A licenceFile is one licence or notice file of a module, by its path inside
// the module, with the paths of any other files there that hold the same text.
type licenceFile struct {
	paths   []string
	text    string
	notice  bool
	licence licence
}

// licenceFiles reads a module's licence and notice files: those in its root,
// and those in a directory that holds one of its linked packages or a parent
// of one. A licence file the generator cannot identify stops it.
func licenceFiles(m module) ([]licenceFile, error) {
	dirs := map[string]bool{m.dir: true}
	for _, dir := range m.packageDirs {
		for dir != m.dir && strings.HasPrefix(dir, m.dir+string(filepath.Separator)) {
			dirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}
	var paths []string
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && stem(entry.Name()) != "" {
				paths = append(paths, filepath.Join(dir, entry.Name()))
			}
		}
	}
	slices.SortFunc(paths, func(a, b string) int { return compareByDepth(m.dir, a, b) })
	var files []licenceFile
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		relative, err := filepath.Rel(m.dir, path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		relative = filepath.ToSlash(relative)
		text := tidy(string(raw))
		if i := slices.IndexFunc(files, func(f licenceFile) bool { return f.text == text }); i >= 0 {
			files[i].paths = append(files[i].paths, relative)
			continue
		}
		if m.path == standardLibrary && !m.boringCrypto && fmt.Sprintf("%x", sha256.Sum256([]byte(text))) == boringLicence {
			continue
		}
		file := licenceFile{paths: []string{relative}, text: text, notice: stem(filepath.Base(path)) == "notice"}
		if !file.notice {
			l, ok := identify(text)
			if !ok {
				return nil, fmt.Errorf("%s: the licence in %s is not one this generator knows; add its text to scripts/notice once its terms are read", m, relative)
			}
			file.licence = l
		}
		files = append(files, file)
	}
	if !slices.ContainsFunc(files, func(f licenceFile) bool { return !f.notice }) {
		return nil, fmt.Errorf("%s holds no licence file", m)
	}
	return files, nil
}

// compareByDepth orders a module's root files first, then the rest by path.
func compareByDepth(root, a, b string) int {
	aRoot, bRoot := filepath.Dir(a) == root, filepath.Dir(b) == root
	if aRoot != bRoot {
		if aRoot {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// stem names the kind of a licence or notice file: license, licence, copying
// or notice, for a file named so in any case, alone, with a suffix after a
// hyphen, or with a .txt or .md extension. Any other name gives "".
func stem(name string) string {
	base := strings.ToLower(name)
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".txt"), ".md")
	for _, s := range []string{"license", "licence", "copying", "notice"} {
		if base == s || strings.HasPrefix(base, s+"-") {
			return s
		}
	}
	return ""
}

// tidy gives a file's text Unix line ends, no trailing spaces and no blank
// lines at either end.
func tidy(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

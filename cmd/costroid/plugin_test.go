package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Costroid/costroid/internal/recipelint"
)

// pluginRoot is the Claude Code plugin's directory, by path from this package.
var pluginRoot = filepath.Join(repositoryRoot, "plugin", "costroid")

func TestPluginSkillMatchesInitSkill(t *testing.T) {
	if got := readFile(t, filepath.Join(pluginRoot, "skills", "costroid", "SKILL.md")); got != costroidSkill {
		t.Errorf("the plugin's skill is not the one init --skills writes:\n%s", got)
	}
}

// TestPluginToolsReadOnly starts each server the plugin names, as Claude Code
// would, from the binary this tree builds. Each tool it lists must say it only
// reads and destroys nothing, and every tool a command allows must be one of
// them.
func TestPluginToolsReadOnly(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "costroid")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, output)
	}
	var config struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(pluginRoot, ".mcp.json"))), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.MCPServers) == 0 {
		t.Fatal("the plugin names no server")
	}
	readTools := recipelint.ReadTools{}
	for name, server := range config.MCPServers {
		if server.Command != "costroid" {
			t.Fatalf("server %s runs %q, not the costroid binary on PATH", name, server.Command)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
			Connect(ctx, &mcp.CommandTransport{Command: exec.Command(binary, server.Args...)}, nil)
		if err != nil {
			t.Fatalf("start server %s: %v", name, err)
		}
		defer session.Close()
		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Tools) == 0 {
			t.Errorf("server %s lists no tool", name)
		}
		for _, tool := range listed.Tools {
			annotations := tool.Annotations
			if annotations == nil || !annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint {
				t.Errorf("server %s's tool %s does not state that it only reads and destroys nothing", name, tool.Name)
				continue
			}
			readTools[name] = append(readTools[name], tool.Name)
		}
		slices.Sort(readTools[name])
	}
	report, err := recipelint.LintMarketplace(repositoryRoot, readTools)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		t.Error(finding)
	}
}

// TestPluginPassesTheClaudeValidator runs Claude Code's own validator, with
// warnings as errors, on the plugin and on the marketplace. Its settings
// directory is one of its own. It needs claude on PATH.
func TestPluginPassesTheClaudeValidator(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not on PATH")
	}
	for _, target := range []string{pluginRoot, repositoryRoot} {
		command := exec.Command(claude, "plugin", "validate", "--strict", target)
		command.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+t.TempDir(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("claude plugin validate --strict %s: %v\n%s", target, err, output)
		}
	}
}

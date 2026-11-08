package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Costroid/costroid/internal/mcpserver"
	"github.com/Costroid/costroid/internal/recipelint"
)

const exitRejected = 1

func lintRecipes(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		fmt.Fprint(stderr, "costroid: lint-recipes needs at least one path\n")
		return exitFailure
	}
	report, err := recipelint.Lint(paths)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	plugins, err := lintMarketplace(".")
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	report.Files += plugins.Files
	report.Findings = append(report.Findings, plugins.Findings...)
	for _, note := range report.Notes {
		fmt.Fprintln(stdout, note)
	}
	for _, finding := range report.Findings {
		fmt.Fprintln(stderr, finding)
	}
	fmt.Fprintf(stdout, "costroid: %d files, %d rejected lines\n", report.Files, len(report.Findings))
	if len(report.Findings) > 0 {
		return exitRejected
	}
	return 0
}

// lintMarketplace holds the plugin marketplace at root, and the plugins it
// lists, to the plugin rules, when root holds one. The tools known to only
// read are the ones this binary's own MCP server lists that way.
func lintMarketplace(root string) (recipelint.Report, error) {
	switch _, err := os.Stat(filepath.Join(root, filepath.FromSlash(recipelint.MarketplaceFile))); {
	case errors.Is(err, fs.ErrNotExist):
		return recipelint.Report{}, nil
	case err != nil:
		return recipelint.Report{}, fmt.Errorf("read the marketplace: %w", err)
	}
	tools, err := ownReadTools(context.Background())
	if err != nil {
		return recipelint.Report{}, err
	}
	return recipelint.LintMarketplace(root, recipelint.ReadTools{mcpserver.Name: tools})
}

// ownReadTools lists the tools of costroid mcp, in process, and keeps those
// that state they only read and destroy nothing.
func ownReadTools(ctx context.Context) ([]string, error) {
	serverSide, clientSide := mcp.NewInMemoryTransports()
	server, err := mcpserver.New(mcpserver.Options{}).Connect(ctx, serverSide, nil)
	if err != nil {
		return nil, fmt.Errorf("start costroid mcp to list its tools: %w", err)
	}
	defer server.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "lint-recipes", Version: version}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		return nil, fmt.Errorf("list the tools of costroid mcp: %w", err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list the tools of costroid mcp: %w", err)
	}
	var tools []string
	for _, tool := range listed.Tools {
		hints := tool.Annotations
		if hints != nil && hints.ReadOnlyHint && hints.DestructiveHint != nil && !*hints.DestructiveHint {
			tools = append(tools, tool.Name)
		}
	}
	slices.Sort(tools)
	return tools, nil
}

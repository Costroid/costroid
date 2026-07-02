// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package webdist embeds the built web dashboard. `make build` copies
// web/dist into the dist/ directory next to this file before compiling
// (go:embed cannot reference paths outside the package directory). A
// committed dist/.gitkeep keeps the embed pattern valid on a clean checkout
// where the dashboard has never been built, so plain `go build ./...` works.
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the built dashboard assets rooted at the dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: "dist" is embedded above and always present.
		panic(err)
	}
	return sub
}

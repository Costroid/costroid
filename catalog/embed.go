// Package catalogdata embeds the price sheets of each release the binary
// carries.
package catalogdata

import "embed"

// Version is the release a run prices with.
const Version = "0.2.0"

// Sheets holds one directory per release, named for its version, with one
// dated file per provider. From 0.2.0, a release directory also holds
// SHA256SUMS, the catalog's manifest of the release's sheets.
//
//go:embed 0.1.0/*.json 0.2.0/*.json 0.2.0/SHA256SUMS
var Sheets embed.FS

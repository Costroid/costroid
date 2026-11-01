// Package catalogdata embeds the pinned price sheets.
package catalogdata

import "embed"

// Version is the pinned catalog release.
const Version = "0.1.0"

// Sheets holds one dated file per provider under Version.
//
//go:embed 0.1.0/*.json
var Sheets embed.FS

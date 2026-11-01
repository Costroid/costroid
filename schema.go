// Package costroid holds the published schema of costroid.yml.
package costroid

import _ "embed"

// ConfigSchema is the JSON Schema of costroid.yml.
//
//go:embed costroid.schema.json
var ConfigSchema []byte

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import "testing"

func TestResolveAddr(t *testing.T) {
	tests := []struct {
		name     string
		flagAddr string
		envAddr  string
		want     string
	}{
		{name: "default", flagAddr: "", envAddr: "", want: ":8080"},
		{name: "env only", flagAddr: "", envAddr: ":9090", want: ":9090"},
		{name: "flag only", flagAddr: ":7070", envAddr: "", want: ":7070"},
		{name: "flag wins over env", flagAddr: ":7070", envAddr: ":9090", want: ":7070"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAddr(tt.flagAddr, tt.envAddr); got != tt.want {
				t.Errorf("resolveAddr(%q, %q) = %q, want %q", tt.flagAddr, tt.envAddr, got, tt.want)
			}
		})
	}
}

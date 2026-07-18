// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/storage"
)

// TestBuildAlertChannels pins the serve --sync secret-resolution wiring: slots
// resolve from the D32 vault, a webhook without an authSlot never touches the
// vault, and a missing slot is a startup error naming the channel and slot.
func TestBuildAlertChannels(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "credentials.key")
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", keyPath)
	if err := credentials.InitKeyFile(keyPath); err != nil {
		t.Fatalf("InitKeyFile: %v", err)
	}
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	vault, err := credentials.Open(keyPath, store)
	if err != nil {
		t.Fatalf("credentials.Open: %v", err)
	}
	if err := vault.Set(ctx, "slack-url", "https://hooks.slack.com/services/T000/B000/token"); err != nil {
		t.Fatalf("vault.Set slack: %v", err)
	}
	if err := vault.Set(ctx, "ops-token", "bearer-secret-value"); err != nil {
		t.Fatalf("vault.Set token: %v", err)
	}

	t.Run("slack and webhook resolve", func(t *testing.T) {
		channels, err := buildAlertChannels(ctx, store, []alertChannelConfig{
			{name: "team-slack", kind: "slack", urlSlot: "slack-url"},
			{name: "ops-webhook", kind: "webhook", endpoint: "https://ops.example.com/hook", authSlot: "ops-token"},
		})
		if err != nil {
			t.Fatalf("buildAlertChannels: %v", err)
		}
		if len(channels) != 2 {
			t.Fatalf("channels = %d, want 2", len(channels))
		}
		if channels[0].Name() != "team-slack" || channels[1].Name() != "ops-webhook" {
			t.Fatalf("channel names = %q, %q", channels[0].Name(), channels[1].Name())
		}
	})

	t.Run("webhook without authSlot does not need the vault", func(t *testing.T) {
		// A fresh store with no key file at all: a webhook with no authSlot must
		// still build, proving no vault access happens for it.
		bare, err := storage.Open(ctx, t.TempDir())
		if err != nil {
			t.Fatalf("storage.Open: %v", err)
		}
		defer func() { _ = bare.Close() }()
		channels, err := buildAlertChannels(ctx, bare, []alertChannelConfig{
			{name: "ops-webhook", kind: "webhook", endpoint: "https://ops.example.com/hook"},
		})
		if err != nil || len(channels) != 1 {
			t.Fatalf("no-auth webhook: err=%v channels=%d", err, len(channels))
		}
	})

	t.Run("missing slot is a named startup error", func(t *testing.T) {
		_, err := buildAlertChannels(ctx, store, []alertChannelConfig{
			{name: "ops-webhook", kind: "webhook", endpoint: "https://ops.example.com/hook", authSlot: "absent-slot"},
		})
		if err == nil {
			t.Fatal("missing slot should be a startup error")
		}
		if !strings.Contains(err.Error(), "ops-webhook") || !strings.Contains(err.Error(), "absent-slot") {
			t.Fatalf("error should name the channel and slot: %v", err)
		}
	})
}

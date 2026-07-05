// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command fakeopenai serves a directory of canned per-month costs responses
// as a fake OpenAI Admin endpoint (see the fakeopenai package) for offline
// end-to-end verification of the openai-cost connector:
//
//	go run ./internal/devtools/fakeopenai/cmd --dir <tree> --addr 127.0.0.1:10012
//
// Every request is logged to stdout (method, path, whether a cursor was
// presented) — never any header or key. It is a development tool only.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakeopenai"
)

func main() {
	dir := flag.String("dir", "", "directory of <YYYY-MM>.json canned responses")
	addr := flag.String("addr", "127.0.0.1:10012", "listen address")
	apiKey := flag.String("api-key", fakeopenai.AdminKey, "expected Bearer key value")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "fakeopenai: --dir is required")
		os.Exit(2)
	}
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "fakeopenai: --dir %s is not a directory\n", *dir)
		os.Exit(2)
	}

	handler := fakeopenai.NewWithKey(*dir, *apiKey)
	handler.LogWriter = os.Stdout
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("fakeopenai serving %s on %s\n", *dir, *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeopenai:", err)
		os.Exit(1)
	}
}

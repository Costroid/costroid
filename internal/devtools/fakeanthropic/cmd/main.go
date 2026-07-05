// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command fakeanthropic serves a directory of canned per-month cost-report
// responses as a fake Anthropic Admin endpoint (see the fakeanthropic
// package) for offline end-to-end verification of the anthropic-cost
// connector:
//
//	go run ./internal/devtools/fakeanthropic/cmd --dir <tree> --addr 127.0.0.1:10011
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

	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
)

func main() {
	dir := flag.String("dir", "", "directory of <YYYY-MM>.json canned responses")
	addr := flag.String("addr", "127.0.0.1:10011", "listen address")
	apiKey := flag.String("api-key", fakeanthropic.AdminKey, "expected x-api-key value")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "fakeanthropic: --dir is required")
		os.Exit(2)
	}
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "fakeanthropic: --dir %s is not a directory\n", *dir)
		os.Exit(2)
	}

	handler := fakeanthropic.NewWithKey(*dir, *apiKey)
	handler.LogWriter = os.Stdout
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("fakeanthropic serving %s on %s\n", *dir, *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeanthropic:", err)
		os.Exit(1)
	}
}

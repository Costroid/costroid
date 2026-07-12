// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command fakebigquery serves canned BigQuery v2 FOCUS row envelopes for
// manual offline verification. Tests register runtime-generated keys directly;
// the standalone command is primarily useful for request-shape debugging.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakebigquery"
)

func main() {
	dir := flag.String("dir", "", "directory of <YYYY-MM>.json BigQuery row envelopes")
	addr := flag.String("addr", "127.0.0.1:10013", "listen address")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "fakebigquery: --dir is required")
		os.Exit(2)
	}
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "fakebigquery: --dir %s is not a directory\n", *dir)
		os.Exit(2)
	}
	handler := fakebigquery.New(*dir)
	handler.LogWriter = os.Stdout
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("fakebigquery serving %s on %s\n", *dir, *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "fakebigquery:", err)
		os.Exit(1)
	}
}

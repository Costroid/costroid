// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command fakes3 serves a directory tree as a fake S3 endpoint (see the
// fakes3 package) for offline end-to-end verification of the
// aws-focus-s3 connector:
//
//	go run ./internal/devtools/fakes3/cmd --dir testdata/aws-focus-s3/fixture --addr 127.0.0.1:9911
//
// It is a development tool only — never part of the product.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakes3"
)

func main() {
	dir := flag.String("dir", "", "directory tree to serve (<dir>/<bucket>/<key...>)")
	addr := flag.String("addr", "127.0.0.1:9911", "listen address")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "fakes3: --dir is required")
		os.Exit(2)
	}
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "fakes3: --dir %s is not a directory\n", *dir)
		os.Exit(2)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           fakes3.New(*dir),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("fakes3 serving %s on %s\n", *dir, *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "fakes3:", err)
		os.Exit(1)
	}
}

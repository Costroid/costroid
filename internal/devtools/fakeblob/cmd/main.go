// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command fakeblob serves a directory tree as a fake Azure Blob Storage
// endpoint (see the fakeblob package) for offline end-to-end
// verification of the azure-focus connector:
//
//	go run ./internal/devtools/fakeblob/cmd --dir <tree> --addr 127.0.0.1:10001
//
// Every Get Blob request is logged to stdout ("Get Blob <key>"), so
// end-to-end scripts can prove an unchanged re-sync performed ZERO Get
// Blob calls. It is a development tool only — never part of the product.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakeblob"
)

func main() {
	dir := flag.String("dir", "", "directory tree to serve (<dir>/<account>/<container>/<key...>)")
	addr := flag.String("addr", "127.0.0.1:10001", "listen address")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "fakeblob: --dir is required")
		os.Exit(2)
	}
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "fakeblob: --dir %s is not a directory\n", *dir)
		os.Exit(2)
	}

	handler := fakeblob.New(*dir)
	srv := &http.Server{
		Addr: *addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A blob GET is any non-listing request below /<account>/<container>/.
			if r.URL.Query().Get("comp") != "list" && strings.Count(strings.Trim(r.URL.Path, "/"), "/") >= 2 {
				fmt.Printf("fakeblob: Get Blob %s\n", strings.TrimPrefix(r.URL.Path, "/"))
			}
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("fakeblob serving %s on %s\n", *dir, *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeblob:", err)
		os.Exit(1)
	}
}

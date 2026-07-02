// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command costroid is the Costroid server binary. It serves the HTTP API
// and the embedded web dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/webdist"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = "usage: costroid serve [--addr host:port]"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "costroid:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addrFlag := flags.String("addr", "", `listen address (overrides $COSTROID_ADDR; default ":8080")`)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing serve flags: %w", err)
	}
	addr := resolveAddr(*addrFlag, os.Getenv("COSTROID_ADDR"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:    addr,
		Handler: api.NewHandler(version, webdist.FS()),
	}

	errc := make(chan error, 1)
	go func() {
		fmt.Printf("costroid %s listening on %s\n", version, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("serving HTTP on %s: %w", addr, err)
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return <-errc
}

// resolveAddr picks the listen address: the --addr flag wins over
// $COSTROID_ADDR, which wins over the default.
func resolveAddr(flagAddr, envAddr string) string {
	if flagAddr != "" {
		return flagAddr
	}
	if envAddr != "" {
		return envAddr
	}
	return ":8080"
}

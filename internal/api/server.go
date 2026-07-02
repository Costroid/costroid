// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package api implements the Costroid HTTP API on top of the server
// scaffolding generated from contracts/openapi.yaml (see api.gen.go).
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
)

// serviceName is the service identity reported by /api/v1/meta.
const serviceName = "costroid"

// focusVersion is the FOCUS specification version the internal model
// targets (decision D4).
const focusVersion = "1.4"

// Server implements the generated ServerInterface.
type Server struct {
	version string
}

var _ ServerInterface = (*Server)(nil)

// NewServer returns a Server reporting the given binary version.
func NewServer(version string) *Server {
	return &Server{version: version}
}

// GetHealthz implements GET /healthz.
func (s *Server) GetHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// GetMeta implements GET /api/v1/meta.
func (s *Server) GetMeta(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(Meta{
		Name:         serviceName,
		Version:      s.version,
		FocusVersion: focusVersion,
	})
}

// NewHandler returns the root HTTP handler: the API routes from the
// generated scaffolding plus the built dashboard served from static at /.
func NewHandler(version string, static fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(static))
	return HandlerFromMux(NewServer(version), mux)
}

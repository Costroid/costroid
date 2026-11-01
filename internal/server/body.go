package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func tooLarge() *refusal {
	return refuse(http.StatusRequestEntityTooLarge, codeBodyTooLarge, "The request body is over the limit.")
}

// readBody reads a request, inflating gzip, and holds both forms to the cap,
// so a small compressed body cannot expand past it.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return nil, tooLarge()
	}
	if err != nil {
		return nil, fmt.Errorf("server: read body: %w", err)
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		return raw, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, refuse(http.StatusBadRequest, codeMalformed, "The body is not valid gzip.")
	}
	inflated, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes+1))
	if err != nil {
		return nil, refuse(http.StatusBadRequest, codeMalformed, "The body is not valid gzip.")
	}
	if len(inflated) > maxBodyBytes {
		return nil, tooLarge()
	}
	return inflated, nil
}

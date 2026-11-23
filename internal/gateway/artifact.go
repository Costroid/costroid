package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// maxArchiveBytes bounds the artifact's zip. One request file is far smaller.
const maxArchiveBytes = 1 << 20

// Artifacts reads one artifact of a run through GitHub's REST API, with a
// token that holds actions: read. The run is this job's own, or the run a
// workflow_run event follows.
type Artifacts struct {
	API        string
	Repository string
	Run        string
	Token      string
	HTTP       *http.Client
	Every      time.Duration
}

// errNotYet means the artifact has not been uploaded.
var errNotYet = errors.New("gateway: the artifact is not uploaded yet")

// ErrNoRequest means the run ended and left no request: its agent's job was
// skipped or ended before it sealed one, so no key is wanted.
var ErrNoRequest = errors.New("gateway: the run ended with no sealed request")

// runCompleted is the status GitHub gives a run all of whose jobs have ended.
const runCompleted = "completed"

// Wait polls until the artifact named holds the request, or ctx ends. A run
// that has ended without the artifact answers ErrNoRequest. The artifact is
// asked for once more after the run is read as ended, because it may have
// arrived between the two reads.
func (a Artifacts) Wait(ctx context.Context, name string) (Request, error) {
	for {
		request, err := a.fetch(ctx, name)
		if !errors.Is(err, errNotYet) {
			return request, err
		}
		ended, err := a.ended(ctx)
		if err != nil {
			return Request{}, err
		}
		if ended {
			request, err := a.fetch(ctx, name)
			if errors.Is(err, errNotYet) {
				return Request{}, ErrNoRequest
			}
			return request, err
		}
		select {
		case <-ctx.Done():
			return Request{}, fmt.Errorf("gateway: no artifact %s arrived: %w", name, ctx.Err())
		case <-time.After(a.Every):
		}
	}
}

func (a Artifacts) fetch(ctx context.Context, name string) (Request, error) {
	route := fmt.Sprintf("%s/repos/%s/actions/runs/%s/artifacts?name=%s", a.API, a.Repository, url.PathEscape(a.Run), url.QueryEscape(name))
	var listing struct {
		Artifacts []struct {
			Download string `json:"archive_download_url"`
			Expired  bool   `json:"expired"`
		} `json:"artifacts"`
	}
	body, err := a.get(ctx, route, "the artifact list")
	if err != nil {
		return Request{}, err
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return Request{}, fmt.Errorf("gateway: parse the artifact list: %w", err)
	}
	if len(listing.Artifacts) == 0 || listing.Artifacts[0].Expired {
		return Request{}, errNotYet
	}
	archive, err := a.get(ctx, listing.Artifacts[0].Download, "the artifact")
	if err != nil {
		return Request{}, err
	}
	return requestIn(archive)
}

// ended reads whether the run has ended.
func (a Artifacts) ended(ctx context.Context) (bool, error) {
	body, err := a.get(ctx, fmt.Sprintf("%s/repos/%s/actions/runs/%s", a.API, a.Repository, url.PathEscape(a.Run)), "the run's status")
	if err != nil {
		return false, err
	}
	var run struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &run); err != nil {
		return false, fmt.Errorf("gateway: parse the run's status: %w", err)
	}
	return run.Status == runCompleted, nil
}

func requestIn(archive []byte) (Request, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Request{}, fmt.Errorf("gateway: read the artifact: %w", err)
	}
	for _, file := range reader.File {
		if file.Name != RequestFile {
			continue
		}
		content, err := file.Open()
		if err != nil {
			return Request{}, fmt.Errorf("gateway: open %s in the artifact: %w", RequestFile, err)
		}
		defer content.Close()
		return ReadRequest(content)
	}
	return Request{}, fmt.Errorf("gateway: the artifact holds no %s", RequestFile)
}

// get reads one answer of GitHub's, which what names in an error.
func (a Artifacts) get(ctx context.Context, address, what string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+a.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := a.HTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("gateway: fetch %s: %w", what, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway: fetch %s: GitHub answered %d", what, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gateway: read %s: %w", what, err)
	}
	if len(data) > maxArchiveBytes {
		return nil, fmt.Errorf("gateway: %s is over %d bytes", what, maxArchiveBytes)
	}
	return data, nil
}

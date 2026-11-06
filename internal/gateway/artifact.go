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

// Artifacts reads one artifact of the current run through GitHub's REST API,
// with a token that holds actions: read.
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

// Wait polls until the artifact named holds the request, or ctx ends.
func (a Artifacts) Wait(ctx context.Context, name string) (Request, error) {
	for {
		request, err := a.fetch(ctx, name)
		if !errors.Is(err, errNotYet) {
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
	body, err := a.get(ctx, route)
	if err != nil {
		return Request{}, err
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return Request{}, fmt.Errorf("gateway: parse the artifact list: %w", err)
	}
	if len(listing.Artifacts) == 0 || listing.Artifacts[0].Expired {
		return Request{}, errNotYet
	}
	archive, err := a.get(ctx, listing.Artifacts[0].Download)
	if err != nil {
		return Request{}, err
	}
	return requestIn(archive)
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

func (a Artifacts) get(ctx context.Context, address string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+a.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := a.HTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("gateway: fetch the artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway: fetch the artifact: GitHub answered %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gateway: read the artifact: %w", err)
	}
	if len(data) > maxArchiveBytes {
		return nil, fmt.Errorf("gateway: the artifact is over %d bytes", maxArchiveBytes)
	}
	return data, nil
}

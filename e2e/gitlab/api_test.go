//go:build gitlab

package gitlab

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// apiRoot is GitLab's REST root as the instance names itself. The client's
// dial ignores the address and reaches the container.
const apiRoot = "http://127.0.0.1/api/v4"

// client calls GitLab's REST API with one token.
type client struct {
	t     *testing.T
	http  *http.Client
	token string
}

func (in *instance) client(token string) *client {
	return &client{
		t:     in.t,
		token: token,
		http: &http.Client{
			Timeout:   time.Minute,
			Transport: &http.Transport{DisableKeepAlives: true, DialContext: in.dial},
		},
	}
}

// call sends one request. On a 2xx answer it decodes the body into out, or
// copies it when out is a *string, and returns the status. On any other answer
// it returns the status and the body; a request that got no answer returns 0
// and the error.
func (c *client) call(method, path string, in, out any) (int, string) {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, apiRoot+path, body)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	request.Header.Set("PRIVATE-TOKEN", c.token)
	if in != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err.Error()
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, err.Error()
	}
	if response.StatusCode/100 != 2 {
		return response.StatusCode, string(data)
	}
	c.decode(method, path, data, out)
	return response.StatusCode, ""
}

func (c *client) decode(method, path string, data []byte, out any) {
	c.t.Helper()
	switch target := out.(type) {
	case nil:
	case *string:
		*target = string(data)
	default:
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

// must is call that fails the test on anything but a 2xx answer.
func (c *client) must(method, path string, in, out any) {
	c.t.Helper()
	if status, body := c.call(method, path, in, out); status/100 != 2 {
		c.t.Fatalf("%s %s: status %d: %s", method, path, status, body)
	}
}

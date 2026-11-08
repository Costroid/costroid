package otlpemit

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// batchSpans is the most spans one request carries, the default of
// OTEL_BSP_MAX_EXPORT_BATCH_SIZE
// (https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/,
// read 2026-11-08).
const batchSpans = 512

// Retries follow OTLP 1.11.1 (https://opentelemetry.io/docs/specs/otlp/, read
// 2026-11-08): 429, 502, 503 and 504 are retried, with exponential back-off
// and jitter, or after the answer's Retry-After. A wait past maxWait ends the
// tries.
const (
	attempts    = 3
	firstWait   = time.Second
	maxWait     = 30 * time.Second
	maxResponse = 64 << 10
	maxMessage  = 200
)

var retryable = map[int]bool{
	http.StatusTooManyRequests:    true,
	http.StatusBadGateway:         true,
	http.StatusServiceUnavailable: true,
	http.StatusGatewayTimeout:     true,
}

// Result is what one Emit sent.
type Result struct {
	Rows, Spans int
	// Rejected counts the spans an endpoint answered as refused in a partial
	// success, and Message is its reason, cleaned of control characters.
	Rejected int64
	Message  string
}

// RefusedError is an endpoint that answered a request with a status other
// than 2xx, after any retries. The spans of that request were not taken.
type RefusedError struct {
	Status int
	Spans  int
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("the endpoint answered HTTP %d", e.Status)
}

// Emitter sends rows as spans.
type Emitter struct {
	Config  Config
	Version string
	// Sleep waits between tries. Nil waits on a timer.
	Sleep func(context.Context, time.Duration) error
}

// Emit sends a rows file. It sends nothing when a line is not a row of the
// contract. It stops at the first request the endpoint refuses, and Result
// counts what went before.
func (e Emitter) Emit(ctx context.Context, rows []byte) (Result, error) {
	requests, count, err := readRequests(rows)
	if err != nil {
		return Result{}, err
	}
	result := Result{Rows: count}
	for first := 0; first < len(requests); first += batchSpans {
		batch := make([]span, 0, batchSpans)
		for _, q := range requests[first:min(first+batchSpans, len(requests))] {
			batch = append(batch, spanOf(q))
		}
		rejected, message, err := e.send(ctx, batch)
		if err != nil {
			return result, err
		}
		result.Spans += len(batch)
		result.Rejected += rejected
		if message != "" {
			result.Message = message
		}
	}
	return result, nil
}

func (e Emitter) send(ctx context.Context, batch []span) (int64, string, error) {
	body, contentType, err := e.body(batch)
	if err != nil {
		return 0, "", err
	}
	client := e.client()
	backoff := firstWait
	for attempt := 1; ; attempt++ {
		answer, err := e.post(ctx, client, body, contentType)
		switch {
		case err != nil && ctx.Err() != nil:
			return 0, "", err
		case err == nil && answer.status/100 == 2:
			rejected, message := partialSuccess(answer)
			return rejected, message, nil
		case err == nil && !retryable[answer.status]:
			return 0, "", &RefusedError{Status: answer.status, Spans: len(batch)}
		}
		delay := jitter(backoff)
		if asked, ok := answer.retryAfter(); err == nil && ok {
			delay = asked
		}
		if attempt == attempts || delay > maxWait {
			if err != nil {
				return 0, "", err
			}
			return 0, "", &RefusedError{Status: answer.status, Spans: len(batch)}
		}
		if err := e.sleep(ctx, delay); err != nil {
			return 0, "", err
		}
		backoff *= 2
	}
}

func (e Emitter) body(batch []span) ([]byte, string, error) {
	var body []byte
	contentType := "application/x-protobuf"
	if e.Config.Protocol == JSON {
		encoded, err := encodeJSON(batch, e.Version)
		if err != nil {
			return nil, "", fmt.Errorf("encode spans: %w", err)
		}
		body, contentType = encoded, "application/json"
	} else {
		body = encodeProtobuf(batch, e.Version)
	}
	if !e.Config.Gzip {
		return body, contentType, nil
	}
	var packed bytes.Buffer
	writer := gzip.NewWriter(&packed)
	if _, err := writer.Write(body); err != nil {
		return nil, "", fmt.Errorf("compress spans: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("compress spans: %w", err)
	}
	return packed.Bytes(), contentType, nil
}

// client follows no redirect: a header such as an API key would go with it to
// whatever host the redirect names.
func (e Emitter) client() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: e.Config.TLS},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// answer is an endpoint's response, read whole up to maxResponse.
type answer struct {
	status      int
	header      http.Header
	body        []byte
	contentType string
}

func (e Emitter) post(ctx context.Context, client *http.Client, body []byte, contentType string) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, e.Config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Config.Endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return answer{}, fmt.Errorf("build the request: %w", err)
	}
	for _, header := range e.Config.Headers {
		request.Header.Add(header[0], header[1])
	}
	request.Header.Set("Content-Type", contentType)
	if e.Config.Gzip {
		request.Header.Set("Content-Encoding", "gzip")
	}
	response, err := client.Do(request)
	if err != nil {
		return answer{}, fmt.Errorf("send to %s: %w", Redacted(e.Config.Endpoint), cause(err))
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return answer{}, fmt.Errorf("read the answer of %s: %w", Redacted(e.Config.Endpoint), err)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return answer{status: response.StatusCode, header: response.Header, body: data, contentType: mediaType}, nil
}

// cause drops the *url.Error around a transport's error, whose text repeats
// the endpoint whole.
func cause(err error) error {
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		return wrapped.Err
	}
	return err
}

// retryAfter is the wait the answer asks for, in seconds or as a date.
func (a answer) retryAfter() (time.Duration, bool) {
	value := a.header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, int64(maxWait/time.Second)+1)) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}

// jitter waits between half and all of delay, so senders that failed together
// do not retry together.
func jitter(delay time.Duration) time.Duration {
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + rand.N(half)
}

func (e Emitter) sleep(ctx context.Context, delay time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// partialSuccess reads ExportTraceServiceResponse.partial_success. An answer
// that holds none, or none that reads, refused nothing.
func partialSuccess(a answer) (int64, string) {
	var rejected int64
	var message string
	if a.contentType == "application/json" {
		var payload struct {
			PartialSuccess struct {
				RejectedSpans json.RawMessage `json:"rejectedSpans"`
				ErrorMessage  string          `json:"errorMessage"`
			} `json:"partialSuccess"`
		}
		if json.Unmarshal(a.body, &payload) != nil {
			return 0, ""
		}
		rejected, _ = strconv.ParseInt(strings.Trim(string(payload.PartialSuccess.RejectedSpans), `"`), 10, 64)
		message = payload.PartialSuccess.ErrorMessage
	} else {
		rejected, message = protobufPartialSuccess(a.body)
	}
	return max(rejected, 0), clean(message)
}

// protobufPartialSuccess reads field 1 of the response, and in it
// rejected_spans (1) and error_message (2).
func protobufPartialSuccess(body []byte) (int64, string) {
	partial, _ := protobufField(body, 1, wireBytes)
	count, _ := protobufField(partial, 1, wireVarint)
	message, _ := protobufField(partial, 2, wireBytes)
	rejected, _ := binary.Uvarint(count)
	return int64(rejected), string(message)
}

// protobufField returns the last value of a field in a message: a varint's
// bytes, or a length-delimited field's data. It skips fields of other numbers,
// and reports false on a message that does not read.
func protobufField(message []byte, number, wire int) ([]byte, bool) {
	var found []byte
	for len(message) > 0 {
		tag, n := binary.Uvarint(message)
		if n <= 0 {
			return nil, false
		}
		message = message[n:]
		var value []byte
		switch int(tag & 7) {
		case wireVarint:
			_, size := binary.Uvarint(message)
			if size <= 0 {
				return nil, false
			}
			value, message = message[:size], message[size:]
		case wireFixed64:
			if len(message) < 8 {
				return nil, false
			}
			value, message = message[:8], message[8:]
		case wireBytes:
			size, n := binary.Uvarint(message)
			if n <= 0 || size > uint64(len(message)-n) {
				return nil, false
			}
			value, message = message[n:n+int(size)], message[n+int(size):]
		case wireFixed32:
			if len(message) < 4 {
				return nil, false
			}
			value, message = message[:4], message[4:]
		default:
			return nil, false
		}
		if int(tag>>3) == number && int(tag&7) == wire {
			found = value
		}
	}
	return found, true
}

// clean keeps an endpoint's message to one printable line of at most
// maxMessage characters.
func clean(message string) string {
	var out strings.Builder
	count := 0
	for _, r := range message {
		if count == maxMessage {
			break
		}
		if r < ' ' || r == 0x7f {
			r = ' '
		}
		out.WriteRune(r)
		count++
	}
	return strings.TrimSpace(out.String())
}

// Redacted is an endpoint as a line may print it: scheme, host and path, with
// no user information and no query.
func Redacted(endpoint *url.URL) string {
	shown := url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host, Path: endpoint.Path}
	return shown.String()
}

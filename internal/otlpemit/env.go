package otlpemit

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Protocol is an OTLP/HTTP body encoding.
type Protocol string

// The protocols of OTEL_EXPORTER_OTLP_PROTOCOL that emit-otlp sends.
const (
	Protobuf Protocol = "http/protobuf"
	JSON     Protocol = "http/json"
)

// protocolGRPC is the third protocol the variable names, which emit-otlp
// does not send.
const protocolGRPC = "grpc"

// The defaults of the OTLP exporter specification
// (https://opentelemetry.io/docs/specs/otel/protocol/exporter/, read
// 2026-11-08): http://localhost:4318 with v1/traces appended, http/protobuf,
// a 10 s timeout and no compression.
const (
	defaultEndpoint = "http://localhost:4318/"
	tracesPath      = "v1/traces"
	defaultTimeout  = 10 * time.Second
)

// Config is where and how the spans go.
type Config struct {
	Endpoint *url.URL
	// Headers are sent with every request. Their values are credentials as
	// often as not, so nothing prints them.
	Headers  [][2]string
	Protocol Protocol
	Timeout  time.Duration
	Gzip     bool
	// TLS is nil unless a certificate variable is set.
	TLS *tls.Config
}

// Warning is a variable whose value the exporter specification says to
// ignore: it was read as unset, or one entry of it was.
type Warning struct {
	Variable string
	// Value and Want are empty for an entry of the headers, whose values are
	// credentials as often as not.
	Value, Want string
}

func (w Warning) String() string {
	if w.Value == "" {
		return fmt.Sprintf("%s holds an entry that is not key=value, so emit-otlp leaves the entry out", w.Variable)
	}
	return fmt.Sprintf("%s holds %q, which is not %s, so emit-otlp reads it as unset", w.Variable, w.Value, w.Want)
}

// variable reads the traces form of an exporter variable, then the general
// form: the specification lets a signal's own variable override.
type variable struct {
	getenv func(string) string
}

func (v variable) read(name string) (string, string) {
	traces := "OTEL_EXPORTER_OTLP_TRACES_" + name
	if value := strings.TrimSpace(v.getenv(traces)); value != "" {
		return traces, value
	}
	general := "OTEL_EXPORTER_OTLP_" + name
	return general, strings.TrimSpace(v.getenv(general))
}

// FromEnvironment reads the OTLP exporter variables. A value the
// specification says to ignore is read as unset and returned as a warning. An
// endpoint that is not http or https, the grpc protocol and a certificate
// that does not load are errors.
func FromEnvironment(getenv func(string) string) (Config, []Warning, error) {
	v := variable{getenv: getenv}
	config := Config{Protocol: Protobuf, Timeout: defaultTimeout}
	var warnings []Warning
	endpoint, err := v.endpoint()
	if err != nil {
		return Config{}, nil, err
	}
	config.Endpoint = endpoint
	name, value := v.read("PROTOCOL")
	switch value {
	case "":
	case string(Protobuf), string(JSON):
		config.Protocol = Protocol(value)
	case protocolGRPC:
		return Config{}, nil, fmt.Errorf("%s is grpc, and emit-otlp sends OTLP over HTTP: set it to http/protobuf or http/json", name)
	default:
		warnings = append(warnings, Warning{Variable: name, Value: value, Want: "http/protobuf or http/json"})
	}
	name, value = v.read("TIMEOUT")
	if value != "" {
		milliseconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || milliseconds <= 0 {
			warnings = append(warnings, Warning{Variable: name, Value: value, Want: "a whole number of milliseconds above 0"})
		} else {
			config.Timeout = time.Duration(milliseconds) * time.Millisecond
		}
	}
	name, value = v.read("COMPRESSION")
	switch value {
	case "", "none":
	case "gzip":
		config.Gzip = true
	default:
		warnings = append(warnings, Warning{Variable: name, Value: value, Want: "gzip or none"})
	}
	name, value = v.read("HEADERS")
	headers, skipped := parseHeaders(value)
	config.Headers = headers
	if skipped {
		warnings = append(warnings, Warning{Variable: name})
	}
	config.TLS, err = v.tls()
	if err != nil {
		return Config{}, nil, err
	}
	return config, warnings, nil
}

// endpoint is the traces variable's URL as it is, or the general variable's
// with v1/traces appended, or the default.
func (v variable) endpoint() (*url.URL, error) {
	name, value := v.read("ENDPOINT")
	signal := strings.HasPrefix(name, "OTEL_EXPORTER_OTLP_TRACES_")
	if value == "" {
		value, signal = defaultEndpoint, false
	}
	address, err := url.Parse(value)
	if err != nil || address.Host == "" || address.Scheme != "http" && address.Scheme != "https" {
		return nil, fmt.Errorf("%s is not an http or https URL", name)
	}
	switch {
	case signal && address.Path == "":
		address.Path = "/"
	case !signal:
		address = address.JoinPath(tracesPath)
	}
	return address, nil
}

// parseHeaders reads key=value pairs separated by commas, the W3C Baggage
// form the specification names, with each value percent-decoded. It reports
// whether an entry was left out.
func parseHeaders(value string) ([][2]string, bool) {
	var headers [][2]string
	skipped := false
	for _, entry := range strings.Split(value, ",") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		key, raw, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		decoded, err := url.PathUnescape(strings.TrimSpace(raw))
		if !found || key == "" || err != nil || !validHeader(key, decoded) {
			skipped = true
			continue
		}
		headers = append(headers, [2]string{key, decoded})
	}
	return headers, skipped
}

// validHeader holds a header to what HTTP can carry: a token for its name,
// and no control character in its value.
func validHeader(key, value string) bool {
	for _, r := range key {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, r) {
			return false
		}
	}
	for _, r := range value {
		if r < ' ' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

// tls loads the certificate variables. The certificate is the trusted root
// for the endpoint's own; the client certificate and key are sent for mutual
// TLS and are set together or not at all.
func (v variable) tls() (*tls.Config, error) {
	certificateName, certificate := v.read("CERTIFICATE")
	certName, cert := v.read("CLIENT_CERTIFICATE")
	keyName, key := v.read("CLIENT_KEY")
	if certificate == "" && cert == "" && key == "" {
		return nil, nil
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if certificate != "" {
		data, err := os.ReadFile(certificate)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", certificateName, err)
		}
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("%s names %s, which holds no PEM certificate", certificateName, certificate)
		}
	}
	if (cert == "") != (key == "") {
		return nil, fmt.Errorf("%s and %s are set together or not at all", certName, keyName)
	}
	if cert != "" {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, fmt.Errorf("%s and %s: %w", certName, keyName, err)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	return config, nil
}

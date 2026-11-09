package scm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm/kmstest"
)

func unhex(t *testing.T, text string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(strings.ReplaceAll(text, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// TestSignV4MatchesAWSTestVector holds the signature to the case
// post-x-www-form-urlencoded of AWS's Signature Version 4 test suite, read on
// 2026-11-09 from
// https://github.com/awslabs/aws-c-auth/tree/main/tests/aws-signing-test-suite/v4/post-x-www-form-urlencoded.
func TestSignV4MatchesAWSTestVector(t *testing.T) {
	body := []byte("Param1=value1")
	payload := sha256.Sum256(body)
	headers := map[string]string{
		"content-length":       "13",
		"content-type":         "application/x-www-form-urlencoded",
		"host":                 "example.amazonaws.com",
		"x-amz-content-sha256": hex.EncodeToString(payload[:]),
		"x-amz-date":           "20150830T123600Z",
	}
	at := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	signed, signature := signV4("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "service", http.MethodPost, "/", headers, body, at)
	if signed != "content-length;content-type;host;x-amz-content-sha256;x-amz-date" {
		t.Errorf("signed headers = %s", signed)
	}
	if signature != "d3875051da38690788ef43de4db0d8f280229d82040bfac253562e56c3f20e0b" {
		t.Errorf("signature = %s", signature)
	}
}

// TestWrapPaddedMatchesRFC5649 holds the key wrap to both examples of RFC
// 5649 section 6: one of several blocks, and one of a single block.
func TestWrapPaddedMatchesRFC5649(t *testing.T) {
	kek := unhex(t, "5840df6e29b02af1 ab493b705bf16ea1 ae8338f4dcc176a8")
	for _, example := range []struct{ key, wrap string }{
		{"c37b7e6492584340 bed1220780894115 5068f738", "138bdeaa9b8fa7fc 61f97742e72248ee 5ae6ae5360d1ae6a 5f54f373fa543b6a"},
		{"466f7250617369", "afbeb0f07dfbf541 9200f2ccb50bb24f"},
	} {
		wrapped, err := wrapPadded(kek, unhex(t, example.key))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(wrapped, unhex(t, example.wrap)) {
			t.Errorf("wrap of %s = %x", example.key, wrapped)
		}
		unwrapped, err := kmstest.UnwrapPadded(kek, wrapped)
		if err != nil || !bytes.Equal(unwrapped, unhex(t, example.key)) {
			t.Errorf("the stub's unwrap of %s = %x, %v", example.key, unwrapped, err)
		}
	}
	if _, err := wrapPadded(kek, nil); err == nil {
		t.Error("an empty key was wrapped")
	}
}

// captured is one request a test server took.
type captured struct {
	method, path, host string
	header             http.Header
	body               []byte
}

func capturing(t *testing.T, status int, answer string) (*httptest.Server, *[]captured) {
	t.Helper()
	var requests []captured
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests = append(requests, captured{r.Method, r.URL.Path, r.Host, r.Header.Clone(), body})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

const (
	shapeAccessKey = "AKIAREQUESTSHAPE"
	shapeSecret    = "the secret access key of this test"
	shapeSession   = "the session token of this test"
	shapeKey       = "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
)

func TestKMSSignerRequestShape(t *testing.T) {
	signature := []byte("a signature from the stand-in")
	answer := `{"KeyId":"` + shapeKey + `","Signature":"` + base64.StdEncoding.EncodeToString(signature) + `","SigningAlgorithm":"RSASSA_PKCS1_V1_5_SHA_256"}`
	server, requests := capturing(t, http.StatusOK, answer)
	at := time.Date(2026, 11, 9, 10, 30, 0, 0, time.UTC)
	kms, err := KMSFromEnvironment(environment(map[string]string{
		"AWS_ACCESS_KEY_ID": shapeAccessKey, "AWS_SECRET_ACCESS_KEY": shapeSecret, "AWS_SESSION_TOKEN": shapeSession,
		"AWS_REGION": "us-east-1", "AWS_ENDPOINT_URL_KMS": server.URL,
	}), shapeKey, server.Client(), func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("header.claims"))
	got, err := kms.Signer(shapeKey).Sign(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, signature) {
		t.Errorf("signature = %q", got)
	}
	if len(*requests) != 1 {
		t.Fatalf("%d requests", len(*requests))
	}
	request := (*requests)[0]
	if request.method != http.MethodPost || request.path != "/" {
		t.Errorf("%s %s", request.method, request.path)
	}
	for name, want := range map[string]string{
		"Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": "TrentService.Sign",
		"X-Amz-Date": "20261109T103000Z", "X-Amz-Security-Token": shapeSession,
	} {
		if got := request.header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(request.body, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"KeyId": shapeKey, "Message": base64.StdEncoding.EncodeToString(digest[:]),
		"MessageType": "DIGEST", "SigningAlgorithm": "RSASSA_PKCS1_V1_5_SHA_256",
	}
	if len(fields) != len(want) {
		t.Errorf("body = %s", request.body)
	}
	for name, value := range want {
		if fields[name] != value {
			t.Errorf("%s = %v, want %v", name, fields[name], value)
		}
	}
	signed := map[string]string{
		"content-type": "application/x-amz-json-1.1", "host": request.host, "x-amz-date": "20261109T103000Z",
		"x-amz-security-token": shapeSession, "x-amz-target": "TrentService.Sign",
	}
	names, expected := signV4(shapeSecret, "eu-west-1", "kms", http.MethodPost, "/", signed, request.body, at)
	authorization := "AWS4-HMAC-SHA256 Credential=" + shapeAccessKey + "/20261109/eu-west-1/kms/aws4_request, SignedHeaders=" + names + ", Signature=" + expected
	if got := request.header.Get("Authorization"); got != authorization {
		t.Errorf("Authorization = %s\nwant            %s", got, authorization)
	}
	if names != "content-type;host;x-amz-date;x-amz-security-token;x-amz-target" {
		t.Errorf("signed headers = %s", names)
	}
	sent := request.body
	for name, values := range request.header {
		sent = append(sent, []byte(name+": "+strings.Join(values, ","))...)
	}
	if bytes.Contains(sent, []byte(shapeSecret)) {
		t.Error("the request carries the secret access key")
	}
}

func TestKMSRefusalNamesTheTypeAndNothingElse(t *testing.T) {
	server, _ := capturing(t, http.StatusBadRequest, `{"__type":"com.amazonaws.kms#AccessDeniedException","message":"User arn:aws:iam::111122223333:user/x is not authorized"}`)
	kms, err := KMSFromEnvironment(environment(map[string]string{
		"AWS_ACCESS_KEY_ID": shapeAccessKey, "AWS_SECRET_ACCESS_KEY": shapeSecret, "AWS_ENDPOINT_URL": server.URL,
	}), shapeKey, server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = kms.Signer(shapeKey).Sign(context.Background(), [sha256.Size]byte{})
	if err == nil || err.Error() != "scm: KMS Sign answered 400 AccessDeniedException" {
		t.Errorf("err = %v", err)
	}
}

func TestKMSSignerRefusesAnotherAlgorithm(t *testing.T) {
	server, _ := capturing(t, http.StatusOK, `{"Signature":"AAAA","SigningAlgorithm":"RSASSA_PSS_SHA_256"}`)
	kms, err := KMSFromEnvironment(environment(map[string]string{
		"AWS_ACCESS_KEY_ID": shapeAccessKey, "AWS_SECRET_ACCESS_KEY": shapeSecret, "AWS_ENDPOINT_URL_KMS": server.URL,
	}), shapeKey, server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kms.Signer(shapeKey).Sign(context.Background(), [sha256.Size]byte{}); err == nil {
		t.Error("a PSS signature was taken as PKCS #1 v1.5")
	}
}

func TestKMSFromEnvironmentNeedsCredentialsARegionAndHTTPS(t *testing.T) {
	full := map[string]string{"AWS_ACCESS_KEY_ID": shapeAccessKey, "AWS_SECRET_ACCESS_KEY": shapeSecret, "AWS_REGION": "eu-central-1"}
	without := func(name string) map[string]string {
		values := map[string]string{}
		for key, value := range full {
			if key != name {
				values[key] = value
			}
		}
		return values
	}
	cases := map[string]struct {
		values map[string]string
		key    string
		host   string
	}{
		"no access key id":        {values: without("AWS_ACCESS_KEY_ID"), key: "alias/app"},
		"no secret":               {values: without("AWS_SECRET_ACCESS_KEY"), key: "alias/app"},
		"no region":               {values: without("AWS_REGION"), key: "alias/app"},
		"plain http":              {values: map[string]string{"AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "b", "AWS_REGION": "eu-central-1", "AWS_ENDPOINT_URL_KMS": "http://kms.example"}, key: "alias/app"},
		"the variable's region":   {values: full, key: "alias/app", host: "kms.eu-central-1.amazonaws.com"},
		"the default region":      {values: map[string]string{"AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "b", "AWS_DEFAULT_REGION": "ap-southeast-2"}, key: "alias/app", host: "kms.ap-southeast-2.amazonaws.com"},
		"the ARN's region":        {values: full, key: shapeKey, host: "kms.eu-west-1.amazonaws.com"},
		"an ARN with no variable": {values: without("AWS_REGION"), key: shapeKey, host: "kms.eu-west-1.amazonaws.com"},
	}
	for name, tc := range cases {
		kms, err := KMSFromEnvironment(environment(tc.values), tc.key, http.DefaultClient, time.Now)
		switch {
		case tc.host == "" && err == nil:
			t.Errorf("%s: no error", name)
		case tc.host == "":
			if strings.Contains(err.Error(), shapeSecret) {
				t.Errorf("%s: the error names the secret: %v", name, err)
			}
		case err != nil:
			t.Errorf("%s: %v", name, err)
		case kms.Host() != tc.host:
			t.Errorf("%s: host %s", name, kms.Host())
		}
	}
	if _, err := KMSFromEnvironment(environment(full), "alias/app", nil, time.Now); err == nil {
		t.Error("no client was taken")
	}
}

func TestPrepareImportAsksForTheRSAWrapAndImportsWithNoExpiry(t *testing.T) {
	stub := kmstest.New(t)
	kms, err := KMSFromEnvironment(stub.Getenv, kmstest.ImportKey, stub.Server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := kms.PrepareImport(context.Background(), kmstest.ImportKey)
	if err != nil {
		t.Fatal(err)
	}
	signing := stub.Key(kmstest.SigningKey)
	material, err := x509.MarshalPKCS8PrivateKey(signing)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Import(context.Background(), material); err != nil {
		t.Fatal(err)
	}
	if imported := stub.Key(kmstest.ImportKey); imported == nil || !imported.Equal(signing) {
		t.Fatal("the stub holds another key")
	}
	actions := []string{}
	for _, call := range stub.Calls() {
		actions = append(actions, call.Action)
		if bytes.Contains(call.Body, material) || bytes.Contains(call.Body, []byte(base64.StdEncoding.EncodeToString(material))) {
			t.Errorf("%s carried the key in the clear", call.Action)
		}
	}
	if !slices.Equal(actions, []string{"GetParametersForImport", "ImportKeyMaterial"}) {
		t.Errorf("actions = %v", actions)
	}
	var parameters map[string]string
	if err := json.Unmarshal(stub.Calls()[0].Body, &parameters); err != nil {
		t.Fatal(err)
	}
	if parameters["WrappingAlgorithm"] != "RSA_AES_KEY_WRAP_SHA_256" || parameters["WrappingKeySpec"] != "RSA_4096" {
		t.Errorf("parameters = %v", parameters)
	}
}

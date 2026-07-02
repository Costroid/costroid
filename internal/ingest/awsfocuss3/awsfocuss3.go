// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package awsfocuss3 implements the "aws-focus-s3" connector (decisions
// D16, D24): live ingestion of an AWS Data Exports "FOCUS 1.2 with AWS
// columns" export — gzipped CSV — directly from S3. It is the live-sync
// successor to the local-file aws-focus connector (D21) and reuses its
// CSV parsing; only discovery and transport differ.
//
// # Discovery model
//
// Discover lists the export under s3://<bucket>/<prefix>, where <prefix>
// is the export root as AWS delivers it — the S3 prefix configured on
// the Data Export plus the export name, i.e. the folder containing the
// data/ and metadata/ subfolders. Discovery is manifest-driven, per the
// AWS Data Exports delivery contract:
//
//   - Billing periods are the metadata/BILLING_PERIOD=YYYY-MM/
//     partitions that contain a partition-level <export-name>-Manifest.json.
//   - That partition-level manifest always describes the most recent
//     refresh, in BOTH delivery modes ("overwrite existing" and "create
//     new" — the latter also writes a per-execution manifest copy under
//     metadata/<partition>/<timestamp>-<execution-id>/, which is ignored).
//     AWS delivers the manifest only after all data files, so the files
//     it lists are complete.
//   - A period's CURRENT data files are exactly the manifest's dataFiles
//     list — never a raw listing of data/, because overwrite delivery
//     leaves stale extra chunks ("empty data") when an export shrinks.
//   - Each data file is a complete gzipped CSV with its own header row;
//     chunk files are read in manifest order with row numbering
//     continuing across chunks.
//
// Discover yields one ingest.Connector per billing period, so the
// pipeline's pinned replace semantics (see the ingest package) give
// restatement handling per period: AWS re-delivers a period → same
// SourceIdentity → transactional replace; identical content → no-op.
//
// # Source identity
//
// SourceIdentity is "<bucket>/<prefix>/<billing-period>" — the bucket
// and export prefix are part of the identity so that two same-named
// exports (e.g. two payer accounts delivering to different buckets)
// never silently replace each other's data. The trade-off: moving an
// export to a new bucket or prefix creates a NEW identity, and data
// ingested under the old one stays in the store until the FOCUS
// correction/supersede machinery (a later slice) can retire it.
//
// # Content hash
//
// ContentHash is computed WITHOUT re-downloading data: it is
// "sha256:<hex>" over the manifest's current data-file list joined as
// "<key>\t<ETag>\n" lines, sorted by key. S3 ETags are content-derived
// (MD5 of the bytes for single-part uploads; multipart ETags are treated
// as opaque change tokens), so a re-delivered period changes the hash
// exactly when its bytes changed. Each chunk is later fetched with
// If-Match=<discovered ETag>, so a delivery racing the read fails
// cleanly instead of mixing data generations.
//
// # Credentials (decision D24)
//
// Authentication uses ONLY the AWS SDK default credential chain —
// environment variables, shared config/SSO profiles, IAM roles —
// resolved by config.LoadDefaultConfig. Costroid persists no AWS
// credentials, accepts no credential flags, and never logs anything
// credential-shaped. The connector performs read-only calls only:
// ListObjectsV2 and GetObject.
//
// The least-privilege IAM policy (note: s3:ListBucket must be attached
// to the BUCKET ARN with an s3:prefix condition — attached to an object
// ARN it grants nothing):
//
//	{
//	  "Version": "2012-10-17",
//	  "Statement": [
//	    {
//	      "Sid": "ListExportPrefix",
//	      "Effect": "Allow",
//	      "Action": "s3:ListBucket",
//	      "Resource": "arn:aws:s3:::<bucket>",
//	      "Condition": {"StringLike": {"s3:prefix": "<prefix>/*"}}
//	    },
//	    {
//	      "Sid": "ReadExportObjects",
//	      "Effect": "Allow",
//	      "Action": "s3:GetObject",
//	      "Resource": "arn:aws:s3:::<bucket>/<prefix>/*"
//	    }
//	  ]
//	}
//
// # AWS_* environment variables
//
// The SDK's standard variables configure the connector; Costroid adds
// none of its own (which is why none appear in .env.example):
//
//   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN,
//     AWS_PROFILE, AWS_REGION (or a profile region) — the ambient chain.
//   - AWS_ENDPOINT_URL_S3 (or the global AWS_ENDPOINT_URL) — the SDK's
//     native endpoint override, honored automatically. When one is set,
//     the connector enables path-style addressing so local endpoints
//     (the fakes3 dev tool, MinIO, LocalStack) work without DNS tricks.
//   - AWS_EC2_METADATA_DISABLED=true — skips the EC2 IMDS credential
//     lookup; useful off-cloud and in hermetic tests to make a missing
//     credential chain fail fast instead of probing the network.
package awsfocuss3

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
)

// Name is the connector's registry name.
const Name = "aws-focus-s3"

// api is the read-only slice of the S3 API the connector uses.
type api interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Discover authenticates via the ambient AWS credential chain (D24),
// lists the AWS Data Export under s3://<bucket>/<prefix>, and returns
// one connector per discovered billing period, oldest first. See the
// package documentation for the discovery model and required IAM policy.
func Discover(ctx context.Context, bucket, prefix string) ([]*Connector, error) {
	if bucket == "" || prefix == "" {
		return nil, errors.New("bucket and prefix must not be empty")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("no AWS credentials found in the default chain (environment variables, "+
			"shared config/SSO profile, or IAM role) — the aws-focus-s3 connector uses only ambient "+
			"credentials and stores none (decision D24): %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New("no AWS region configured — set AWS_REGION or a profile region")
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		// Custom endpoints (fakes3, MinIO, LocalStack) need path-style
		// addressing: virtual-host style would resolve the bucket as a
		// DNS subdomain of the endpoint.
		if os.Getenv("AWS_ENDPOINT_URL_S3") != "" || os.Getenv("AWS_ENDPOINT_URL") != "" {
			o.UsePathStyle = true
		}
		// Exports predating S3 object checksums carry none, which the
		// SDK logs per GetObject; content integrity here is pinned via
		// ETag + If-Match instead, so the warning is pure noise.
		o.DisableLogOutputChecksumValidationSkipped = true
	})
	return discover(ctx, client, bucket, prefix)
}

// manifestKey matches a PARTITION-LEVEL manifest: the billing-period
// partition directly followed by the manifest file (real deliveries name
// it <export-name>-Manifest.json; per-execution manifest copies live one
// folder deeper and must not match).
var manifestKey = regexp.MustCompile(`^metadata/(BILLING_PERIOD=(\d{4}-\d{2}))/[^/]*Manifest\.json$`)

func discover(ctx context.Context, client api, bucket, prefix string) ([]*Connector, error) {
	prefix = strings.Trim(prefix, "/")
	root := "s3://" + bucket + "/" + prefix

	meta, err := listAll(ctx, client, bucket, prefix+"/metadata/")
	if err != nil {
		return nil, classify(err, "listing "+root+"/metadata/")
	}

	type period struct {
		partition string
		manifests []string
	}
	byPeriod := map[string]*period{}
	for key := range meta {
		m := manifestKey.FindStringSubmatch(strings.TrimPrefix(key, prefix+"/"))
		if m == nil {
			continue
		}
		if byPeriod[m[2]] == nil {
			byPeriod[m[2]] = &period{partition: m[1]}
		}
		byPeriod[m[2]].manifests = append(byPeriod[m[2]].manifests, key)
	}
	if len(byPeriod) == 0 {
		return nil, fmt.Errorf("no billing periods found under %s — expected "+
			"metadata/BILLING_PERIOD=YYYY-MM/<export-name>-Manifest.json below the export root; "+
			"point --prefix at the Data Export's root folder (the one containing data/ and metadata/, "+
			"i.e. the configured S3 prefix plus the export name) and check that the export has "+
			"completed at least one delivery", root)
	}

	periods := make([]string, 0, len(byPeriod))
	for p := range byPeriod {
		periods = append(periods, p)
	}
	sort.Strings(periods)

	conns := make([]*Connector, 0, len(periods))
	for _, p := range periods {
		info := byPeriod[p]
		if len(info.manifests) > 1 {
			// AWS writes exactly one partition-level manifest per period;
			// picking one of several by map order would nondeterministically
			// ingest whichever delivery the stray copy describes.
			sort.Strings(info.manifests)
			return nil, fmt.Errorf("billing period %s has %d partition-level manifests (s3://%s/%s) — "+
				"AWS Data Exports writes exactly one per partition, so a stray copy is an anomaly; "+
				"remove the extra object(s) and re-run ingest",
				p, len(info.manifests), bucket, strings.Join(info.manifests, ", s3://"+bucket+"/"))
		}
		files, err := currentFiles(ctx, client, bucket, prefix, info.partition, info.manifests[0])
		if err != nil {
			return nil, err
		}
		conns = append(conns, &Connector{
			client: client,
			bucket: bucket,
			prefix: prefix,
			period: p,
			files:  files,
		})
	}
	return conns, nil
}

// manifest is the subset of the Data Exports Manifest.json the connector
// reads. The manifest schema is not formally published by AWS, so the
// parser is deliberately liberal: dataFiles entries may be plain strings
// (s3:// URIs or bucket-relative keys) or objects carrying s3Uri or key.
// The billing period is derived from the manifest's S3 key, never from
// the body.
type manifest struct {
	DataFiles []manifestFile `json:"dataFiles"`
}

type manifestFile struct {
	uri string
}

func (f *manifestFile) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		f.uri = s
		return nil
	}
	var obj struct {
		S3Uri string `json:"s3Uri"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("dataFiles entry is neither a string nor an object: %w", err)
	}
	switch {
	case obj.S3Uri != "":
		f.uri = obj.S3Uri
	case obj.Key != "":
		f.uri = obj.Key
	default:
		return errors.New("dataFiles entry carries neither s3Uri nor key")
	}
	return nil
}

// key resolves a dataFiles entry to a key in bucket.
func (f manifestFile) key(bucket string) (string, error) {
	if rest, ok := strings.CutPrefix(f.uri, "s3://"); ok {
		b, key, found := strings.Cut(rest, "/")
		if !found || key == "" {
			return "", fmt.Errorf("dataFiles URI %q has no key part", f.uri)
		}
		if b != bucket {
			return "", fmt.Errorf("dataFiles URI %q references bucket %q, not %q", f.uri, b, bucket)
		}
		return key, nil
	}
	return strings.TrimPrefix(f.uri, "/"), nil
}

// dataFile is one current chunk of a billing period.
type dataFile struct {
	key  string
	etag string
}

// currentFiles fetches and parses a period's partition-level manifest and
// resolves its current data files with their ETags.
func currentFiles(ctx context.Context, client api, bucket, prefix, partition, manifestKey string) ([]dataFile, error) {
	manifestURI := "s3://" + bucket + "/" + manifestKey
	obj, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(manifestKey)})
	if err != nil {
		return nil, classify(err, "fetching manifest "+manifestURI)
	}
	body, err := io.ReadAll(obj.Body)
	closeErr := obj.Body.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestURI, err)
	}

	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("malformed manifest %s: %w", manifestURI, err)
	}
	if len(m.DataFiles) == 0 {
		return nil, fmt.Errorf("malformed manifest %s: it lists no data files (dataFiles is empty or missing)", manifestURI)
	}

	keys := make([]string, 0, len(m.DataFiles))
	for _, f := range m.DataFiles {
		key, err := f.key(bucket)
		if err != nil {
			return nil, fmt.Errorf("malformed manifest %s: %w", manifestURI, err)
		}
		if !strings.HasSuffix(key, ".csv.gz") {
			return nil, fmt.Errorf("unsupported export format: manifest %s lists %q — this slice reads "+
				"gzipped-CSV FOCUS 1.2 exports only; recreate the AWS Data Export with the gzip/csv "+
				"output configuration (file format \"text/csv\", compression \"gzip\")", manifestURI, key)
		}
		keys = append(keys, key)
	}

	// One data/<partition>/ listing yields every chunk's ETag (covering
	// both delivery modes' layouts) without downloading anything.
	etags, err := listAll(ctx, client, bucket, prefix+"/data/"+partition+"/")
	if err != nil {
		return nil, classify(err, "listing s3://"+bucket+"/"+prefix+"/data/"+partition+"/")
	}
	files := make([]dataFile, 0, len(keys))
	for _, key := range keys {
		etag, ok := etags[key]
		if !ok {
			return nil, fmt.Errorf("manifest %s lists s3://%s/%s but the object is missing — "+
				"the export delivery may be in progress; retry once it completes", manifestURI, bucket, key)
		}
		files = append(files, dataFile{key: key, etag: etag})
	}
	return files, nil
}

// listAll pages through ListObjectsV2 and returns key → ETag.
func listAll(ctx context.Context, client api, bucket, prefix string) (map[string]string, error) {
	out := map[string]string{}
	var token *string
	for {
		resp, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range resp.Contents {
			out[aws.ToString(obj.Key)] = aws.ToString(obj.ETag)
		}
		if !aws.ToBool(resp.IsTruncated) {
			return out, nil
		}
		token = resp.NextContinuationToken
	}
}

// classify turns typed AWS API errors into short actionable messages.
func classify(err error, action string) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AccessDenied":
			return fmt.Errorf("access denied while %s — the credentials lack the connector's read-only "+
				"permissions: s3:ListBucket on the bucket ARN restricted to the export prefix, plus "+
				"s3:GetObject on the export objects (see the awsfocuss3 package documentation for the "+
				"exact least-privilege policy): %w", action, err)
		case "NoSuchBucket":
			return fmt.Errorf("bucket not found while %s — check the --bucket value: %w", action, err)
		case "PreconditionFailed":
			return fmt.Errorf("stale object while %s — the export was re-delivered mid-read; "+
				"re-run ingest — discovery will pick up the new manifest: %w", action, err)
		}
	}
	return fmt.Errorf("%s: %w", action, err)
}

// Connector reads one billing period of one AWS FOCUS Data Export in S3.
// Instances are produced by Discover, one per period.
type Connector struct {
	client api
	bucket string
	prefix string
	period string
	files  []dataFile
}

var _ ingest.Connector = (*Connector)(nil)

// Name implements ingest.Connector.
func (c *Connector) Name() string { return Name }

// FOCUSVersion implements ingest.Connector: AWS Data Exports produces
// FOCUS 1.2 (with proprietary x_ columns, which the pipeline drops).
func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_2 }

// BillingPeriod returns the connector's billing period ("YYYY-MM").
func (c *Connector) BillingPeriod() string { return c.period }

// SourceIdentity implements ingest.Connector. See the package
// documentation for why bucket and prefix are part of the identity and
// the move-the-export trade-off that follows.
func (c *Connector) SourceIdentity() string {
	return c.bucket + "/" + c.prefix + "/" + c.period
}

// ContentHash implements ingest.Connector without touching the network:
// the digest of the discovered current-file list and its S3 ETags (see
// the package documentation for the exact construction).
func (c *Connector) ContentHash(_ context.Context) (string, error) {
	files := make([]dataFile, len(c.files))
	copy(files, c.files)
	sort.Slice(files, func(i, j int) bool { return files[i].key < files[j].key })
	h := sha256.New()
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\t%s\n", f.key, f.etag) // hash.Hash never errors
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// Records implements ingest.Connector: the period's chunks are streamed
// in manifest order, each a complete gzipped CSV with its own header
// row, with row numbering continuing across chunks.
func (c *Connector) Records(ctx context.Context) (ingest.RecordReader, error) {
	return &chunkReader{ctx: ctx, conn: c}, nil
}

// chunkReader streams a period's chunks sequentially, opening each via
// GetObject on demand.
type chunkReader struct {
	// ctx is the Records call's context; the RecordReader interface has
	// no per-Next context, and the pipeline scopes one context to the
	// whole run.
	ctx    context.Context
	conn   *Connector
	next   int
	body   io.ReadCloser
	stream *awsfocus.GzipCSVStream
	rows   int
}

// Next implements ingest.RecordReader.
func (r *chunkReader) Next() (ingest.Row, error) {
	for {
		if r.stream == nil {
			if r.next >= len(r.conn.files) {
				return ingest.Row{}, io.EOF
			}
			if err := r.open(r.conn.files[r.next]); err != nil {
				return ingest.Row{}, err
			}
		}
		row, err := r.stream.Next()
		if errors.Is(err, io.EOF) {
			r.rows = r.stream.Rows()
			if err := r.closeChunk(); err != nil {
				return ingest.Row{}, fmt.Errorf("closing chunk s3://%s/%s: %w", r.conn.bucket, r.conn.files[r.next].key, err)
			}
			r.next++
			continue
		}
		if err != nil {
			return ingest.Row{}, fmt.Errorf("chunk s3://%s/%s: %w", r.conn.bucket, r.conn.files[r.next].key, err)
		}
		return row, nil
	}
}

func (r *chunkReader) open(f dataFile) error {
	// If-Match pins the chunk to the ETag seen at discovery: a delivery
	// racing the read fails cleanly instead of mixing data generations.
	obj, err := r.conn.client.GetObject(r.ctx, &s3.GetObjectInput{
		Bucket:  aws.String(r.conn.bucket),
		Key:     aws.String(f.key),
		IfMatch: aws.String(f.etag),
	})
	if err != nil {
		return classify(err, fmt.Sprintf("fetching chunk s3://%s/%s", r.conn.bucket, f.key))
	}
	stream, err := awsfocus.NewGzipCSVStream(obj.Body, r.rows)
	if err != nil {
		_ = obj.Body.Close()
		return fmt.Errorf("chunk s3://%s/%s: %w", r.conn.bucket, f.key, err)
	}
	r.body, r.stream = obj.Body, stream
	return nil
}

func (r *chunkReader) closeChunk() error {
	streamErr := r.stream.Close()
	bodyErr := r.body.Close()
	r.stream, r.body = nil, nil
	if streamErr != nil {
		return streamErr
	}
	return bodyErr
}

// Close implements ingest.RecordReader.
func (r *chunkReader) Close() error {
	if r.stream == nil {
		return nil
	}
	return r.closeChunk()
}

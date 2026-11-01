// Package otlp reads OTLP/HTTP traces and logs into per-request usage.
//
// It is the ingest edge for telemetry. Decoding keeps only allowlisted scalar
// attributes, so content never reaches a Usage, and every field left behind is
// counted by name. A record that carries no token counts yields nothing.
package otlp

import (
	"errors"
	"maps"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// Encoding is an OTLP/HTTP body format.
type Encoding int

// The two body formats of OTLP/HTTP.
const (
	JSON Encoding = iota + 1
	Protobuf
)

// Signal is the OTLP signal a request carries.
type Signal int

// The signals that can hold per-request token counts.
const (
	Traces Signal = iota + 1
	Logs
)

// ErrMalformed means a body did not decode.
var ErrMalformed = errors.New("otlp: malformed payload")

// Count is one token class of one request.
type Count struct {
	Class  focus.TokenClass
	Tokens int64
}

// Usage is one model request as a dialect reports it.
type Usage struct {
	// ID is the provider's request or response id when the record has one, and
	// a stand-in derived from the record when it does not.
	ID                 string
	Dialect            Dialect
	Provider           string
	Model              string
	ServiceTier        string
	ContextInputTokens int64
	Start, End         time.Time
	Counts             []Count
	// AssumedWrite is the cache write class that took tokens whose lifetime the
	// dialect did not report. Empty when it reported all.
	AssumedWrite focus.TokenClass
	// Notes state a limit of this dialect in words and travel on the row.
	Notes []string

	Session          string
	Agent            string
	AgentVersion     string
	CostBasis        focus.CostBasis
	EnforcementGrade string

	// Repo, PullRequest, CommitSHA and RunID are the sender's join keys.
	Repo        string
	PullRequest string
	CommitSHA   string
	RunID       string
	// Email is held in memory only, to be replaced before anything is stored.
	Email string
}

// Batch is one decoded request.
type Batch struct {
	Usage []Usage
	// Rejected counts records that carried token counts the edge could not use.
	Rejected int
	// Drops counts every field left behind, by label.
	Drops map[string]int64
}

// record is one span or log record with its resource's attributes merged in.
type record struct {
	attrs   attributes
	name    string
	start   uint64
	end     uint64
	traceID string
	spanID  string
	log     bool
}

// Parse reads one request body.
func Parse(body []byte, encoding Encoding, signal Signal) (Batch, error) {
	c := &collector{drops: map[string]int64{}}
	var records []record
	var err error
	switch encoding {
	case JSON:
		records, err = decodeJSON(body, signal, c)
	case Protobuf:
		records, err = decodeProtobuf(body, signal, c)
	default:
		err = ErrMalformed
	}
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Drops: c.drops}
	for i := range records {
		usage, status := interpret(&records[i])
		switch status {
		case statusUsage:
			batch.Usage = append(batch.Usage, usage)
		case statusRejected:
			batch.Rejected++
		}
		if records[i].attrs[keyEmail].text != "" {
			c.drop(DropEmail)
		}
	}
	return batch, nil
}

func merged(resource attributes, own attributes) attributes {
	all := make(attributes, len(resource)+len(own))
	maps.Copy(all, resource)
	maps.Copy(all, own)
	return all
}

package server

import (
	"encoding/binary"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/otlp"
)

const protobufType = "application/x-protobuf"

// The partial-success messages share a shape across signals.
const (
	fieldPartialSuccess = 1
	fieldRejected       = 1
	wireVarint          = 0
	wireBytes           = 2
)

// invalidUsage counts a request whose model or agent misses the tag pattern.
const invalidUsage = "invalid_usage"

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	s.handleTelemetry(w, r, otlp.Traces)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	s.handleTelemetry(w, r, otlp.Logs)
}

// handleMetrics accepts an export and stores nothing. Rows come from logs and
// spans, and an exporter that sends all three signals must not see an error.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := readBody(w, r); err != nil {
		s.fail(w, r, err)
		return
	}
	drops := map[string]int64{otlp.DropMetrics: 1}
	if _, err := s.write(r, who, nil, drops); err != nil {
		s.fail(w, r, err)
		return
	}
	s.answerTelemetry(w, r, 0)
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request, signal otlp.Signal) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	batch, err := otlp.Parse(body, encodingOf(r), signal)
	if errors.Is(err, otlp.ErrMalformed) {
		s.fail(w, r, refuse(http.StatusBadRequest, codeMalformed, "The body is not a valid OTLP request."))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	charges, rejected := chargesOf(batch, s.Now)
	if rejected > 0 {
		batch.Drops[invalidUsage] += int64(rejected)
	}
	if _, err := s.write(r, who, charges, batch.Drops); err != nil {
		s.fail(w, r, err)
		return
	}
	s.answerTelemetry(w, r, batch.Rejected+rejected)
}

func encodingOf(r *http.Request) otlp.Encoding {
	if strings.HasPrefix(r.Header.Get(contentTypeHeader), jsonType) {
		return otlp.JSON
	}
	return otlp.Protobuf
}

var schemePrefix = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)

// repoOf turns vcs.repository.url.full into the tag's form: no scheme, no
// credentials, no .git, lower case.
func repoOf(url string) string {
	repo := schemePrefix.ReplaceAllString(strings.ToLower(url), "")
	if _, after, found := strings.Cut(repo, "@"); found {
		repo = after
	}
	return strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git")
}

func chargesOf(batch otlp.Batch, now func() time.Time) ([]charge, int) {
	var charges []charge
	rejected := 0
	for i := range batch.Usage {
		usage := &batch.Usage[i]
		if !modelTag.MatchString(usage.Model) || !agentTag.MatchString(usage.Agent) {
			rejected++
			continue
		}
		for _, count := range usage.Counts {
			charges = append(charges, chargeOf(usage, count, batch.Drops, now))
		}
	}
	return charges, rejected
}

func chargeOf(usage *otlp.Usage, count otlp.Count, batchDrops map[string]int64, now func() time.Time) charge {
	start, end := usage.Start, usage.End
	if start.IsZero() {
		start = now()
	}
	if end.Before(start) {
		end = start
	}
	return charge{
		rowID:        rowIdentity(usage.ID, count.Class),
		provider:     usage.Provider,
		model:        usage.Model,
		serviceTier:  usage.ServiceTier,
		context:      usage.ContextInputTokens,
		class:        count.Class,
		tokens:       count.Tokens,
		start:        start,
		end:          end,
		session:      optionalTag(usage.Session, sessionTag, batchDrops),
		agent:        usage.Agent,
		agentVersion: optionalTag(usage.AgentVersion, versionTag, batchDrops),
		basis:        usage.CostBasis,
		enforcement:  usage.EnforcementGrade,
		source:       sourceTelemetry,
		notes:        usage.Notes,
		repo:         repoOf(usage.Repo),
		pr:           usage.PullRequest,
		sha:          strings.ToLower(usage.CommitSHA),
		run:          usage.RunID,
		email:        usage.Email,
	}
}

// answerTelemetry answers in the request's encoding. Rejected records go in
// the partial-success field OTLP gives them, because the exporter must not
// retry a record that will never be accepted.
func (s *Server) answerTelemetry(w http.ResponseWriter, r *http.Request, rejected int) {
	if encodingOf(r) == otlp.JSON {
		answer := map[string]any{}
		if rejected > 0 {
			answer["partialSuccess"] = map[string]any{"rejected": rejected}
		}
		writeJSON(w, http.StatusOK, answer)
		return
	}
	w.Header().Set(contentTypeHeader, protobufType)
	w.WriteHeader(http.StatusOK)
	if rejected > 0 {
		_, _ = w.Write(partialSuccess(rejected))
	}
}

func partialSuccess(rejected int) []byte {
	inner := binary.AppendUvarint(nil, fieldRejected<<3|wireVarint)
	inner = binary.AppendUvarint(inner, uint64(rejected))
	out := binary.AppendUvarint(nil, fieldPartialSuccess<<3|wireBytes)
	out = binary.AppendUvarint(out, uint64(len(inner)))
	return append(out, inner...)
}

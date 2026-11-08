// Package mcpserver answers the Model Context Protocol over one connection, so
// an agent can ask what its run has cost while it works. Its four tools only
// read: the run's status file, the newest check run on a pull request, the
// policy over a rows file, and a ledger's view of a pull request. Every amount
// in an answer is a decimal string beside its currency.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/status"
)

// Name is the name the server gives itself when a client connects.
const Name = "costroid"

// The tools' names.
const (
	RunStatus   = "run_status"
	PullResult  = "pull_result"
	PolicyCheck = "policy_check"
	LedgerPull  = "ledger_pull"
)

// Options are what the tools read through.
type Options struct {
	Version string
	// Getenv names the status file, as costroid run reads it.
	Getenv func(string) string
	// Result reads the newest result object on a pull request's head.
	Result func(ctx context.Context, number int) (history.Posted, error)
	// Policy gives the verdict costroid policy gives on the same request.
	Policy func(ctx context.Context, request PolicyRequest) (policy.Verdict, error)
	// Ledger is the costroid serve the person named, nil for none.
	Ledger *Ledger
}

// PolicyRequest is costroid policy's flags, as policy_check takes them.
type PolicyRequest struct {
	Rows          string `json:"rows" jsonschema:"the rows file costroid run wrote"`
	Config        string `json:"config,omitempty" jsonschema:"costroid.yml as the base branch holds it; costroid.yml in the working directory when empty"`
	WorkflowAdded string `json:"workflow_added,omitempty" jsonschema:"when the workflow reached the base branch, as RFC 3339; without it the warn-only period has not ended"`
	At            string `json:"at,omitempty" jsonschema:"the time of the check, as RFC 3339; now when empty"`
}

// pullRequest names a pull request by its number.
type pullRequest struct {
	PullRequest int `json:"pull_request" jsonschema:"the pull request's number"`
}

// ledgerRequest names a pull request in the ledger.
type ledgerRequest struct {
	PullRequest int    `json:"pull_request" jsonschema:"the pull request's number"`
	Repository  string `json:"repository,omitempty" jsonschema:"the repository as host/owner/name, such as github.com/example/app; the job's own when empty"`
}

// statusAnswer is run_status's answer. Status is null when there is no
// status file, and Missing then says why.
type statusAnswer struct {
	Status  *status.Status `json:"status"`
	Missing string         `json:"missing,omitempty"`
}

// resultAnswer is pull_result's answer: the result object as the check run
// carries it, and the check run's id.
type resultAnswer struct {
	CheckRun int64           `json:"check_run"`
	Result   json.RawMessage `json:"result"`
}

var errNoPullRequest = errors.New("pull_request must be a pull request's number, 1 or more")

// New returns the server with its four tools.
func New(options Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: options.Version}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	mcp.AddTool(server, readTool(RunStatus, false,
		"What the costroid run that started this agent has spent so far, its cap, what is left and what holds it, read from the run's status file. "+
			"Amounts are decimal strings beside their currency. "+
			"When no costroid run has started an agent in this job, status is null and missing says why."),
		options.runStatus)
	mcp.AddTool(server, readTool(PullResult, true,
		"The result object of the newest costroid check run on a pull request's head commit: the conclusion, the figures per currency and cost basis, the token counts, the cap and the policy's verdict. "+
			"It is read from the check run, never from the comment. "+
			"Amounts and token counts are decimal strings, each figure beside its currency."),
		options.pullResult)
	mcp.AddTool(server, readTool(PolicyCheck, false,
		"The verdict the costroid check gives, worked out offline: the policy keys of a costroid.yml held to a rows file that costroid run wrote. "+
			"Name the base branch's costroid.yml, which the check reads. "+
			"The answer is the one costroid policy --json prints. Amounts are decimal strings in USD."),
		options.policyCheck)
	mcp.AddTool(server, readTool(LedgerPull, true,
		"One pull request in the costroid serve ledger this server was started with: its totals per currency and cost basis, its budget spend and its lines. "+
			"Amounts are decimal strings beside their currency."),
		options.ledgerPull)
	return server
}

// readTool describes a tool that changes nothing. openWorld says it reaches a
// host over the network.
func readTool(name string, openWorld bool, description string) *mcp.Tool {
	destructive := false
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			OpenWorldHint:   &openWorld,
		},
	}
}

// Serve answers one client over in and out until the client closes in or ctx
// ends.
func Serve(ctx context.Context, options Options, in io.Reader, out io.Writer) error {
	transport := &mcp.IOTransport{Reader: io.NopCloser(in), Writer: nopCloser{out}}
	return New(options).Run(ctx, transport)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func (o Options) runStatus(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	read, err := status.Read(o.Getenv)
	var missing *status.NoFileError
	if errors.As(err, &missing) {
		return nil, statusAnswer{Missing: missing.Error()}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return nil, statusAnswer{Status: &read}, nil
}

func (o Options) pullResult(ctx context.Context, _ *mcp.CallToolRequest, request pullRequest) (*mcp.CallToolResult, any, error) {
	if request.PullRequest < 1 {
		return nil, nil, errNoPullRequest
	}
	posted, err := o.Result(ctx, request.PullRequest)
	if err != nil {
		return nil, nil, err
	}
	return nil, resultAnswer{CheckRun: posted.CheckRun, Result: json.RawMessage(posted.Object)}, nil
}

func (o Options) policyCheck(ctx context.Context, _ *mcp.CallToolRequest, request PolicyRequest) (*mcp.CallToolResult, any, error) {
	verdict, err := o.Policy(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	return nil, verdict, nil
}

func (o Options) ledgerPull(ctx context.Context, _ *mcp.CallToolRequest, request ledgerRequest) (*mcp.CallToolResult, any, error) {
	if o.Ledger == nil {
		return nil, nil, errors.New("no ledger is named: start costroid mcp with --ledger and the URL of a costroid serve")
	}
	if request.PullRequest < 1 {
		return nil, nil, errNoPullRequest
	}
	view, err := o.Ledger.Pull(ctx, request.PullRequest, request.Repository)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	return nil, view, nil
}

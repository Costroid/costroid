package main

import "slices"

// ghawPaths are the keys of gh-aw's token-usage records, and nothing else:
// every property of token-usage.schema.json in the v0.29.8 release of
// github/gh-aw-firewall (SHA-256 6d58ec9ea230d8889bb6ca03320317838334eabda752ef7abe597e33f511509d,
// read 2026-11-07) but those in ghawFreeText. The schema admits other keys; a
// recording drops them. The request id is kept under ghawIDs.
var ghawPaths = []string{
	"_schema",
	"timestamp",
	"event",
	"provider",
	"model",
	"path",
	"requested_endpoint",
	"upstream_endpoint",
	"status",
	"streaming",
	"input_tokens",
	"output_tokens",
	"cache_read_tokens",
	"cache_write_tokens",
	"reasoning_tokens",
	"duration_ms",
	"response_bytes",
	"purpose",
	"agent_name",
	"caller_source",
	"steering",
	"steering.type",
	"steering.threshold",
	"model_fallback",
	"model_fallback.requested_model",
	"model_fallback.requested_provider",
	"model_fallback.model",
	"model_fallback.provider",
	"model_fallback.from_model",
	"model_fallback.from_provider",
	"model_fallback.failures",
	"model_fallback.failures[]",
	"model_fallback.failures[].provider",
	"model_fallback.failures[].model",
	"model_fallback.failures[].status",
	"model_fallback.attempt",
	"model_fallback.reason",
	"model_fallback.status",
	"x_initiator",
	"billing",
	"billing.quota_chat",
	"billing.quota_chat.ent",
	"billing.quota_chat.ov",
	"billing.quota_chat.ovPerm",
	"billing.quota_chat.rem",
	"billing.quota_chat.rst",
	"billing.quota_premium-chat",
	"billing.quota_premium-chat.ent",
	"billing.quota_premium-chat.ov",
	"billing.quota_premium-chat.ovPerm",
	"billing.quota_premium-chat.rem",
	"billing.quota_premium-chat.rst",
	"billing.rate_limit",
	"billing.rate_remaining",
	"billing.rate_reset",
	"ai_credits_accounting_policy",
	"ai_credits_dynamic_selector",
	"ai_credits_fallback_pricing_used",
	"ai_credits_pricing_api_version",
	"ai_credits_pricing_catalog_id",
	"ai_credits_pricing_catalog_version",
	"ai_credits_pricing_discount_percent",
	"ai_credits_pricing_observed_at",
	"ai_credits_pricing_source",
	"ai_credits_pricing_tier",
	"ai_credits_this_response",
	"ai_credits_total",
}

// ghawFreeText are the schema's properties that a recording drops: the schema
// gives them no list of values, so they can hold any text an upstream wrote.
// model_fallback.reason is a closed list and stays.
var ghawFreeText = []string{"model_fallback.failures[].reason"}

// ghawInfoPaths are the keys of aw_info.json a recording keeps: the releases
// the run used and the cap its proxy held it to. The file also names the
// repository, the workflow, the commit and the person who started the run,
// and a recording drops them.
var ghawInfoPaths = []string{
	"engine_id",
	"version",
	"agent_version",
	"cli_version",
	"awf_version",
	"firewall_enabled",
	"model_config",
	"model_config.max_ai_credits",
	"model_config.max_ai_credits_source",
}

// ghawIDs are the keys of a token-usage record that hold an identifier.
var ghawIDs = map[string]*idKind{
	"request_id": &requestIDs,
}

var ghawAllowlist = newAllowlist(slices.Concat(ghawPaths, ghawInfoPaths), nil, ghawIDs, nil)

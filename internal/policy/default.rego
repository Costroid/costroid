# The default pack. It holds one run's rows to the settings that costroid.yml
# states at the base commit, and returns one result per rule that ran.
#
# Input:
#   trust      "token_verified" when a ledger checked every row's run identity
#              against the job's OIDC token, "unverified" otherwise.
#   rows       the number of rows the check read.
#   usage      per currency: list_cost as a decimal string, rows,
#              unattributed_rows and joinable_rows.
#   settings   run_budget_usd and max_run_cost_usd as decimal strings, each
#              present only when set.
#   warn_only  true while a rule that fails must warn instead.
package costroid

budget_rules := {
	"run_budget": "run_budget_usd",
	"run_cost_threshold": "max_run_cost_usd",
}

results contains result if {
	some rule, setting in budget_rules
	limit := input.settings[setting]
	result := object.union({"rule": rule, "limit_usd": limit}, budget(limit))
}

# Attribution only warns, so it says nothing when it has nothing to warn of.
results contains {"rule": "attribution", "outcome": "warn", "reason": "joinable_rows"} if {
	total("joinable_rows") > 0
}

# A figure the agent's job wrote can prove a run went over a limit, and can
# never prove it stayed under one.
budget(limit) := over_limit if {
	over(limit)
} else := {"outcome": "unproven", "reason": "no_rows"} if {
	input.rows == 0
} else := {"outcome": "unproven", "reason": "unverified_rows"} if {
	input.trust != "token_verified"
} else := {"outcome": "unproven", "reason": "unattributed_rows"} if {
	total("unattributed_rows") > 0
} else := {"outcome": "unproven", "reason": "other_currency"} if {
	some usage in input.usage
	usage.currency != "USD"
} else := {"outcome": "pass", "reason": "within_limit"}

over(limit) if {
	some usage in input.usage
	usage.currency == "USD"
	costroid.compare_decimal(usage.list_cost, limit) > 0
}

over_limit := {"outcome": "warn", "reason": "over_limit", "held_by_warn_only": true} if {
	input.warn_only
} else := {"outcome": "fail", "reason": "over_limit"}

total(field) := sum([usage[field] | some usage in input.usage])

outcomes := {result.outcome | some result in results}

conclusion := "failure" if {
	"fail" in outcomes
} else := "success" if {
	outcomes == {"pass"}
} else := "neutral"

verdict := {"conclusion": conclusion, "results": results}

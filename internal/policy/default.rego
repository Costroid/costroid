# The default pack. It holds one run's rows to the settings that costroid.yml
# states at the base commit, and returns one result per rule that ran.
#
# Input:
#   trust       "token_verified" when a ledger checked every row's run identity
#               against the job's OIDC token, "unverified" otherwise.
#   rows        the number of rows the check read.
#   usage       per currency: list_cost as a decimal string, rows,
#               unattributed_rows and joinable_rows.
#   settings    each amount key that is set, as a decimal string, and models
#               when it is set.
#   model_rows  present when models is set. outside: the rows whose model the
#               allowlist refuses; unreadable: the rows whose model cannot be
#               read.
#   baseline    present when 10 or more earlier runs give a median: its trust,
#               and for each increase key that is set, the USD amount a run
#               may reach.
#   lifetime    present when the pull request's earlier runs were read: its
#               trust, total_usd with this run included, may_repeat and
#               other_currency.
#   warn_only   true while a rule that fails must warn instead.
package costroid

budget_rules := {
	"run_budget": "run_budget_usd",
	"run_cost_threshold": "max_run_cost_usd",
}

results contains result if {
	some rule, setting in budget_rules
	limit := input.settings[setting]
	result := object.union({"rule": rule, "limit_usd": limit}, run_held(limit, "over_limit", input.trust))
}

results contains result if {
	limit := input.settings.max_increase_usd
	result := object.union({"rule": "run_increase", "limit_usd": limit}, increase("max_increase_usd"))
}

results contains result if {
	limit := input.settings.max_increase_percent
	result := object.union({"rule": "run_increase_percent", "limit_percent": limit}, increase("max_increase_percent"))
}

results contains result if {
	limit := input.settings.pr_budget_usd
	result := object.union({"rule": "pr_budget", "limit_usd": limit}, pull_request(limit))
}

# The month to date comes only from a ledger, and no ledger reaches the pack
# yet, so this rule never compares.
results contains {"rule": "monthly_budget", "limit_usd": limit, "outcome": "unproven", "reason": "no_ledger"} if {
	limit := input.settings.monthly_budget_usd
}

results contains result if {
	input.settings.models
	result := object.union({"rule": "model_allowlist"}, allowlist)
}

# Attribution only warns, so it says nothing when it has nothing to warn of.
results contains {"rule": "attribution", "outcome": "warn", "reason": "joinable_rows"} if {
	total("joinable_rows") > 0
}

# A figure the agent's job wrote can prove a run went over a limit, and can
# never prove it stayed under one.
run_held(limit, reason, trust) := broken(reason) if {
	over(limit)
} else := {"outcome": "unproven", "reason": "no_rows"} if {
	input.rows == 0
} else := kept(trust, within_limit)

# The pack does not choose the baseline. One the agents' jobs wrote can show
# this run over it, and never within it.
increase(setting) := run_held(input.baseline[setting], "over_baseline", both(input.baseline.trust)) if {
	input.baseline[setting]
} else := {"outcome": "unproven", "reason": "no_baseline"}

# The pull request's total holds its earlier runs, so it can be over the
# budget while this run wrote no rows.
pull_request(limit) := {"outcome": "unproven", "reason": "no_lifetime"} if {
	not input.lifetime
} else := {"outcome": "unproven", "reason": "repeat_possible"} if {
	input.lifetime.may_repeat
} else := broken("over_limit") if {
	costroid.compare_decimal(input.lifetime.total_usd, limit) > 0
} else := {"outcome": "unproven", "reason": "no_rows"} if {
	input.rows == 0
} else := kept(both(input.lifetime.trust), pull_request_within)

pull_request_within := {"outcome": "unproven", "reason": "other_currency"} if {
	input.lifetime.other_currency
} else := within_limit

# The allowlist reads every row, whatever its currency.
allowlist := broken("model_outside_allowlist") if {
	input.model_rows.outside > 0
} else := {"outcome": "unproven", "reason": "no_rows"} if {
	input.rows == 0
} else := {"outcome": "unproven", "reason": "unreadable_model"} if {
	input.model_rows.unreadable > 0
} else := kept(input.trust, {"outcome": "pass", "reason": "models_on_allowlist"})

# A USD limit says nothing of rows in another currency.
within_limit := {"outcome": "unproven", "reason": "other_currency"} if {
	some usage in input.usage
	usage.currency != "USD"
} else := {"outcome": "pass", "reason": "within_limit"}

# kept is the outcome when nothing was broken. Only rows a ledger checked and
# attributed reach it.
kept(trust, outcome) := {"outcome": "unproven", "reason": "unverified_rows"} if {
	trust != "token_verified"
} else := {"outcome": "unproven", "reason": "unattributed_rows"} if {
	total("unattributed_rows") > 0
} else := outcome

# both is token_verified only when this run and the earlier runs are.
both(earlier) := "token_verified" if {
	input.trust == "token_verified"
	earlier == "token_verified"
} else := "unverified"

over(limit) if {
	some usage in input.usage
	usage.currency == "USD"
	costroid.compare_decimal(usage.list_cost, limit) > 0
}

broken(reason) := {"outcome": "warn", "reason": reason, "held_by_warn_only": true} if {
	input.warn_only
} else := {"outcome": "fail", "reason": reason}

total(field) := sum([usage[field] | some usage in input.usage])

outcomes := {result.outcome | some result in results}

conclusion := "failure" if {
	"fail" in outcomes
} else := "success" if {
	outcomes == {"pass"}
} else := "neutral"

verdict := {"conclusion": conclusion, "results": results}

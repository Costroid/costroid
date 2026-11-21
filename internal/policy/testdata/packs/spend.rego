# Holds a run's EUR cost and the pull request's USD total to the team's own
# amounts, and warns on a run over the baseline's P95.
package costroid.pack

results contains {"rule": "eur_run", "outcome": "fail", "reason": "over_limit"} if {
	some usage in input.usage
	usage.currency == "EUR"
	costroid.compare_decimal(usage.list_cost, "20") > 0
}

results contains {"rule": "pr_total", "outcome": "warn", "reason": "over_limit"} if {
	costroid.compare_decimal(input.lifetime.total_usd, "100") > 0
}

results contains {"rule": "run_over_p95", "outcome": "warn", "reason": "over_p95"} if {
	some usage in input.usage
	usage.currency == "USD"
	costroid.compare_decimal(usage.list_cost, input.baseline.p95_usd) > 0
}

results contains {"rule": "unattributed", "outcome": "warn", "reason": "unattributed_rows"} if {
	count([usage | some usage in input.usage; usage.unattributed_rows > 0]) > 0
}

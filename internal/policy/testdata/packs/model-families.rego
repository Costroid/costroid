# Warns on rows of a model outside the team's families, and fails when one
# model holds more than 500 rows of a run.
package costroid.pack

families := {"claude-", "gpt-", "gemini-"}

results contains {"rule": "model_family", "outcome": "warn", "reason": "other_family", "message": model} if {
	some usage in input.usage
	some model, _ in usage.models
	not in_family(model)
}

results contains {"rule": "model_rows", "outcome": "fail", "reason": "over_rows", "message": model} if {
	some usage in input.usage
	some model, rows in usage.models
	rows > 500
}

in_family(model) if {
	some family in families
	startswith(model, family)
}

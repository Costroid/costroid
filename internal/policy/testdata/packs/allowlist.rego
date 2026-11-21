# Fails a run on rows nobody checked that name a model the base commit's
# allowlist does not list exactly, and warns on preview and experimental
# models.
package costroid.pack

results contains {"rule": "checked_models", "outcome": "fail", "reason": "unlisted_model", "message": model} if {
	input.trust != "token_verified"
	some usage in input.usage
	some model, rows in usage.models
	rows >= 1
	not model in input.settings.models
}

results contains {"rule": "checked_models", "outcome": "warn", "reason": "no_allowlist"} if {
	not input.settings.models
}

results contains {"rule": "preview_model", "outcome": "warn", "reason": "preview", "message": model} if {
	some usage in input.usage
	some model, _ in usage.models
	endswith(model, "-preview")
}

results contains {"rule": "preview_model", "outcome": "warn", "reason": "experimental", "message": model} if {
	some usage in input.usage
	some model, _ in usage.models
	contains(model, "experimental")
}

results contains {"rule": "many_currencies", "outcome": "warn", "reason": "currencies"} if {
	count(input.usage) > 3
	input.rows > 100
}

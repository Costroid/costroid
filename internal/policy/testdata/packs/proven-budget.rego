# Fails a run whose budget Costroid could not prove, so rows nobody checked
# hold a merge instead of passing it with a note.
package costroid.pack

results contains {"rule": "proven_budget", "outcome": "fail", "reason": "budget_unproven", "message": result.reason} if {
	some result in input.results
	result.rule in {"run_budget", "pr_budget"}
	result.outcome == "unproven"
}

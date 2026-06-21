# Costroid — top-level Makefile (M6 T2: the one-command deterministic demo).
#
# `make demo` chains the whole product end-to-end over the synthetic `samples/` packs,
# FULLY OFFLINE and with NO hardware, into a deterministic three-lane FOCUS 1.3 ledger:
#
#   import  (samples/cloud-focus  -> cloud_api)
#   bench   (estimated Gemma 4    -> local_inference)
#   breakeven (local vs cloud, a range — never a hero number)
#   export  (samples/local-logs   -> developer_tool)
#   merge   (all three lanes -> $(LEDGER), byte-identical across re-runs)
#
# Determinism (Hazard 3 / D5): the demo exports SOURCE_DATE_EPOCH from the dated sample
# manifest `as_of` (2026-06-20T00:00:00Z, the gemma4.v1.json as_of) — never "now" — so the
# bench rows, and therefore the merged ledger, are byte-identical on every re-run. The
# default interactive `costroid bench` is untouched (it still stamps the wall clock).
#
# This Makefile is POSIX-make-friendly so it works on Linux AND macOS. Windows has no `make`;
# the README "Quickstart / Demo" section documents the raw `cargo run` equivalents.
#
# Nothing here makes a network call: every step is a local file read of the committed
# synthetic samples. The demo NEVER reads the developer's real logs — it neutralizes every
# provider-discovery env var and points discovery ONLY at samples/local-logs (see DEMO_ENV).

# --- configuration ----------------------------------------------------------------------
CARGO       ?= cargo
# The demo needs the `power` feature (it links the local-inference engine for `bench` /
# `breakeven`). The same binary is byte-identical for `export`/`import` (power only ADDS
# subcommands), so one build serves every step.
BIN         := target/debug/costroid
SAMPLES     := samples
OUT         := target/demo
LEDGER      := $(OUT)/demo-ledger.csv

# The reproducible-builds clock pin (D5): the gemma4.v1.json `as_of`, 2026-06-20T00:00:00Z.
EPOCH       := 1781913600
# The two committed benchmark models (the local_inference lane) + their shared token scenario.
BENCH_MODELS := gemma-4-31b-dense gemma-4-26b-a4b
TOKENS_IN   := 2000
TOKENS_OUT  := 18000
COMPARE_TO  := claude-opus-4-8

# Discovery-env neutralization: point CLAUDE_CONFIG_DIR/CODEX_HOME ONLY at the synthetic
# sample logs and BLANK every other override the discovery code honors (HOME, USERPROFILE,
# ANTHROPIC_API_KEY, CURSOR_DATA_DIR, XDG_STATE_HOME) so the demo can NEVER read the
# developer's real logs — mirroring scripts/focus_conformance.sh's samples/ leg. HOME points
# at a throwaway empty dir so a stray default-path read finds nothing.
DEMO_ENV := env HOME=$(OUT)/nohome USERPROFILE= ANTHROPIC_API_KEY= CURSOR_DATA_DIR= \
            XDG_STATE_HOME= CLAUDE_CONFIG_DIR=$(SAMPLES)/local-logs/claude \
            CODEX_HOME=$(SAMPLES)/local-logs/codex

# ANSI-free section banners (so a 60–90s recording reads well on every terminal / in a pipe).
banner = @printf '\n=== %s ===\n' "$(1)"

.PHONY: help demo demo-build demo-import demo-bench demo-breakeven demo-export demo-verify demo-clean

# --- help (default target) --------------------------------------------------------------
help:
	@printf 'Costroid demo targets (offline, synthetic, deterministic):\n\n'
	@printf '  make demo          Run the full end-to-end demo -> %s\n' "$(LEDGER)"
	@printf '  make demo-verify   Run the demo twice and diff the artifact (fails on any drift)\n'
	@printf '\n  Composable steps (each implies demo-build):\n'
	@printf '  make demo-import   Import the synthetic AWS FOCUS v1.2 export (cloud_api lane)\n'
	@printf '  make demo-bench    Estimated Gemma 4 bench rows (local_inference lane)\n'
	@printf '  make demo-breakeven  Local-vs-cloud break-even (a range, never a hero number)\n'
	@printf '  make demo-export   Export the synthetic dev-tool logs (developer_tool lane)\n'
	@printf '  make demo-clean    Remove the generated %s tree\n' "$(OUT)"
	@printf '\nEverything runs FULLY OFFLINE with NO hardware and NO cloud account.\n'
	@printf 'Output lands under %s (gitignored). Windows (no make): see the README Quickstart.\n' "$(OUT)"

# --- build the power-enabled binary once (every step depends on it) ----------------------
demo-build:
	$(call banner,Building costroid (--features power: the local-inference engine))
	$(CARGO) build -q -p costroid --features power
	@mkdir -p $(OUT) $(OUT)/nohome

# --- (b) cloud_api lane: import the synthetic AWS Bedrock FOCUS v1.2 export --------------
demo-import: demo-build
	$(call banner,cloud_api lane — import a synthetic AWS Bedrock FOCUS v1.2 export)
	@printf '  costroid import %s -> 4 cloud_api rows (source-priced, 9.60 USD)\n' "$(SAMPLES)/cloud-focus/aws-focus-v12.csv"
	@$(BIN) import --format focus-csv --version 1.2 --out csv \
	  $(SAMPLES)/cloud-focus/aws-focus-v12.csv > $(OUT)/cloud.csv
	@printf '  -> %s data rows\n' "$$(( $$(wc -l < $(OUT)/cloud.csv) - 1 ))"

# --- (c) local_inference lane: deterministic estimated Gemma 4 bench rows ----------------
demo-bench: demo-build
	$(call banner,local_inference lane — estimated Gemma 4 bench (no hardware, SOURCE_DATE_EPOCH pinned))
	@printf '  SOURCE_DATE_EPOCH=%s -> byte-identical rows (estimated — pending M3b measurement)\n' "$(EPOCH)"
	@first=1; for model in $(BENCH_MODELS); do \
	  SOURCE_DATE_EPOCH=$(EPOCH) $(BIN) bench --model "$$model" \
	    --tokens-in $(TOKENS_IN) --tokens-out $(TOKENS_OUT) --out csv > $(OUT)/local-$$model.csv; \
	  printf '    %-20s -> 1 local_inference row (%s in + %s out tokens)\n' "$$model" "$(TOKENS_IN)" "$(TOKENS_OUT)"; \
	done

# --- break-even: local-vs-cloud crossover (a range + methodology + stamp; never a number) -
demo-breakeven: demo-build
	$(call banner,break-even — local-vs-cloud crossover (a range, never a hero number))
	@$(BIN) breakeven --model gemma-4-31b-dense --compare-to $(COMPARE_TO) \
	  --tokens-in $(TOKENS_IN) --tokens-out $(TOKENS_OUT) --tokens-per-day 5000000 --plain

# --- (a) developer_tool lane: export the synthetic dev-tool logs (env-neutralized) -------
demo-export: demo-build
	$(call banner,developer_tool lane — export the synthetic Claude/Codex logs (offline))
	@printf '  discovery pointed ONLY at %s/local-logs (real logs unreadable)\n' "$(SAMPLES)"
	@$(DEMO_ENV) $(BIN) export --format csv > $(OUT)/dev.csv
	@printf '  -> %s developer_tool rows\n' "$$(( $$(wc -l < $(OUT)/dev.csv) - 1 ))"

# --- demo = all of it, merged into one deterministic three-lane FOCUS 1.3 ledger ---------
# The merge mechanism mirrors scripts/focus_conformance.sh: the dev-tool export is the header
# + first lane, then each subsequent lane is appended header-skipped (`tail -n +2`). The three
# lanes share ONE FOCUS 1.3 schema (identical headers), so the union is a well-formed CSV.
demo: demo-export demo-import demo-bench demo-breakeven
	$(call banner,merge — one deterministic three-lane FOCUS 1.3 ledger)
	@cp $(OUT)/dev.csv $(LEDGER)
	@tail -n +2 $(OUT)/cloud.csv >> $(LEDGER)
	@for model in $(BENCH_MODELS); do tail -n +2 $(OUT)/local-$$model.csv >> $(LEDGER); done
	@rows=$$(( $$(wc -l < $(LEDGER)) - 1 )); \
	  if [ "$$rows" -ne 20 ]; then \
	    printf '  FAIL: merged ledger has %s rows, expected 20 (14 dev + 4 cloud + 2 local)\n' "$$rows" >&2; \
	    exit 1; \
	  fi; \
	  printf '  wrote %s — %s rows across 3 lanes:\n' "$(LEDGER)" "$$rows"
	@printf '    %6s  %s\n' "$$(tail -n +2 $(LEDGER) | cut -d, -f66 | grep -c developer_tool)" developer_tool
	@printf '    %6s  %s\n' "$$(tail -n +2 $(LEDGER) | cut -d, -f66 | grep -c cloud_api)" cloud_api
	@printf '    %6s  %s\n' "$$(tail -n +2 $(LEDGER) | cut -d, -f66 | grep -c local_inference)" local_inference
	$(call banner,demo complete — fully offline, synthetic, deterministic)

# --- determinism gate: run the demo twice and diff the artifact (fails on any drift) -----
demo-verify:
	$(call banner,demo-verify — proving the ledger is byte-identical across re-runs)
	@$(MAKE) --no-print-directory demo >/dev/null
	@cp $(LEDGER) $(OUT)/demo-ledger.first.csv
	@$(MAKE) --no-print-directory demo >/dev/null
	@if diff -q $(OUT)/demo-ledger.first.csv $(LEDGER) >/dev/null; then \
	  printf '  OK: two demo runs produced a byte-identical %s\n' "$(LEDGER)"; \
	else \
	  printf '  FAIL: the demo ledger differs across re-runs (non-determinism):\n' >&2; \
	  diff $(OUT)/demo-ledger.first.csv $(LEDGER) | sed 's/^/    /' >&2; \
	  exit 1; \
	fi

demo-clean:
	@rm -rf $(OUT)
	@printf 'Removed %s\n' "$(OUT)"

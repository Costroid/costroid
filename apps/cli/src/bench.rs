//! `costroid bench` — the local-inference economics command (M3, behind the off-by-default
//! `power` feature).
//!
//! Two modes:
//! - **estimated / what-if** (default): no subprocess — computes the §3.2 economics from the
//!   bundled Gemma 4 manifest's `estimated_tok_s` + the dated power profile. Works on every OS,
//!   needs no model/hardware (the CI-testable path).
//! - **`--measure`**: runs the model via the chosen runtime subprocess (A2 — `std::process`, not
//!   FFI/HTTP) while reading a configured **wall meter** (the recommended live measured source;
//!   the on-chip sysfs/LHM live reads are the M3b on-hardware step).
//!
//! Emits a `local_inference` FOCUS row (CSV/JSON), with the measurement mode + the dated
//! assumptions stamped (R6/R8/R10). Offline by construction — no network crate.

use anyhow::{bail, Result};
use costroid_power::{
    bundled_models, bundled_power_profiles, estimate_run, run_measured, LlamaCppRunner,
    LocalRunReport, OllamaRunner, ProfileOverrides, RunSpec, Runner, WallMeterPowerSampler,
    DEFAULT_BENCHMARK_SUITE,
};
use costroid_providers::{CanonicalEvent, LocalRunEvent};

use crate::{BenchArgs, ExportFormat, RuntimeArg};

/// A fixed benchmark prompt — Costroid's own (R4: never user content; the model's *output* is
/// never persisted/exported — the runner discards it).
const BENCH_PROMPT: &str =
    "Write a Rust function that reverses a singly linked list in place, with a doc comment.";

impl RuntimeArg {
    fn kind(self) -> &'static str {
        match self {
            RuntimeArg::Ollama => "ollama",
            RuntimeArg::LlamaCpp => "llama.cpp",
        }
    }

    fn default_binary(self) -> &'static str {
        match self {
            RuntimeArg::Ollama => "ollama",
            RuntimeArg::LlamaCpp => "llama-cli",
        }
    }
}

pub fn run_bench(args: &BenchArgs) -> Result<()> {
    let manifest = bundled_models()?;
    let (spec, quant) = manifest.resolve(&args.model, args.quant.as_deref())?;
    let profiles = bundled_power_profiles()?;
    let overrides = ProfileOverrides {
        hardware_profile_id: args.hardware_profile.clone(),
        // The estimated sampler's load comes from the profile; we don't override load_watts from
        // the CLI (the wall meter is the measured override; estimated uses the profile default).
        load_watts: None,
        electricity_rate_per_kwh: args.electricity_rate,
        hardware_price: args.hardware_price,
        hardware_lifetime_seconds: args.hardware_lifetime_seconds,
    };
    let resolved = profiles.resolve(&overrides)?;

    let report = if args.measure {
        // M3a measured path: the recommended live source is the wall meter (true total draw).
        // The on-chip sysfs/LHM live reads are the M3b field-verification.
        let Some(watts) = args.wall_meter_watts else {
            bail!(
                "--measure currently needs --wall-meter-watts (the recommended measured source; \
                 true total-system draw). The on-chip sysfs / LibreHardwareMonitor live reads are \
                 the M3b on-hardware step; estimated mode (omit --measure) needs no hardware."
            );
        };
        let wall = WallMeterPowerSampler::constant(watts)?;
        let runner: Box<dyn Runner> = match args.runtime {
            RuntimeArg::Ollama => Box::new(OllamaRunner),
            RuntimeArg::LlamaCpp => Box::new(LlamaCppRunner),
        };
        let run_spec = RunSpec {
            binary_path: args
                .binary
                .clone()
                .unwrap_or_else(|| args.runtime.default_binary().to_string()),
            // What the RUNTIME loads (an Ollama tag / a GGUF path) — defaults to the manifest id
            // but need not equal it; the economics above key on `args.model` (the manifest id).
            model: args
                .runtime_model
                .clone()
                .unwrap_or_else(|| args.model.clone()),
            quant: quant.clone(),
            prompt: BENCH_PROMPT.to_string(),
            max_tokens: args.tokens_out,
            extra_args: Vec::new(),
        };
        run_measured(
            runner.as_ref(),
            &wall,
            &run_spec,
            // The economic identity is the manifest id (`--model`), not what the runtime loads.
            &args.model,
            &resolved,
            DEFAULT_BENCHMARK_SUITE,
        )?
    } else {
        // Estimated / what-if: no subprocess, no hardware.
        estimate_run(
            spec,
            &quant,
            args.runtime.kind(),
            args.tokens_in,
            args.tokens_out,
            &resolved,
            DEFAULT_BENCHMARK_SUITE,
        )?
    };

    let event = report_to_local_event(&report);
    let rows = costroid_core::focus_records_from_canonical(&[CanonicalEvent::Local(event)])?;
    let output = match args.out {
        ExportFormat::Json => costroid_core::export_focus_json(rows)?,
        ExportFormat::Csv => costroid_core::export_focus_csv(&rows)?,
    };
    print!("{output}");
    Ok(())
}

/// Build the provider-neutral [`LocalRunEvent`] from the harness report — formatting the f64
/// money figures to clean decimal strings (never f64 money in the FOCUS row).
fn report_to_local_event(report: &LocalRunReport) -> LocalRunEvent {
    LocalRunEvent {
        timestamp: chrono::Utc::now(),
        model: report.model.clone(),
        quant: report.quant.clone(),
        runtime_kind: report.runtime_kind.clone(),
        tokens_in: report.tokens_in,
        tokens_out: report.tokens_out,
        run_seconds: report.run_seconds,
        avg_power_watts: report.avg_power_watts,
        measurement_mode: report.measurement_mode.as_focus_str().to_string(),
        energy_wh: report.energy_wh,
        amortized_hw_cost: money_string(report.amortized_hw_cost),
        local_run_cost: money_string(report.local_run_cost),
        electricity_rate_per_kwh: report.electricity_rate_per_kwh,
        hardware_price: report.hardware_price,
        hardware_lifetime_seconds: report.hardware_lifetime_seconds,
        hardware_profile_id: report.hardware_profile_stamp.clone(),
        benchmark_id: report.benchmark_id.clone(),
        billing_currency: report.currency.clone(),
    }
}

/// Format a physical f64 cost to a clean decimal string (rounded, trailing zeros stripped) —
/// the FOCUS money columns are exact decimals, never f64.
fn money_string(value: f64) -> String {
    rust_decimal::Decimal::from_f64_retain(value)
        .map(|d| d.round_dp(10).normalize().to_string())
        .unwrap_or_else(|| "0".to_string())
}

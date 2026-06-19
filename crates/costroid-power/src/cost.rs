//! The local-inference cost model (§3.2) — implemented exactly, with deterministic tests.
//!
//! **M0 scaffold.** These are the literal §3.2 formulas with worked-example tests so M3 builds on
//! a verified core. Energy/power are `f64` (physics); the final `$` figures are mapped to
//! `rust_decimal` for the FOCUS money columns (`x_AmortizedHwCost`, `Cost`, …) where this meets
//! `costroid-focus` at **M3** — that boundary is deliberately not crossed yet. Every result
//! carries the assumptions used; the caller stamps the measurement mode + pricing-snapshot
//! date/hash (R6/R8).

use crate::error::PowerError;

const JOULES_PER_KWH: f64 = 3_600_000.0;
const TOKENS_PER_MILLION: f64 = 1_000_000.0;

/// The transparent assumptions behind a local-run cost (surfaced on every record, R6/R8).
#[derive(Debug, Clone, Copy)]
pub struct CostInputs {
    /// Average power draw over the run, in watts (from the selected [`crate::PowerSampler`]).
    pub avg_power_watts: f64,
    /// Wall-clock run duration, in seconds.
    pub run_seconds: f64,
    /// Electricity rate, in currency units per kWh (dated default; user-overridable — C5/R8).
    pub electricity_rate_per_kwh: f64,
    /// Hardware purchase price, in currency units (for amortization).
    pub hardware_price: f64,
    /// Hardware amortization lifetime, in seconds.
    pub hardware_lifetime_seconds: f64,
    /// Tokens produced in the run (used for the per-1M figure).
    pub tokens_in_run: u64,
}

/// The computed local-run economics for one run (§3.2).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct LocalRunCost {
    /// Energy consumed, in kWh: `(avg_power_watts * run_seconds) / 3_600_000`.
    pub energy_kwh: f64,
    /// Energy cost: `energy_kwh * electricity_rate_per_kwh`.
    pub energy_cost: f64,
    /// Amortized hardware cost: `(hardware_price / hardware_lifetime_seconds) * run_seconds`.
    pub amortized_hw_cost: f64,
    /// Total local run cost: `energy_cost + amortized_hw_cost`.
    pub local_run_cost: f64,
    /// Cost per 1M tokens: `(local_run_cost / tokens_in_run) * 1_000_000`.
    pub local_cost_per_1m: f64,
}

/// Compute the local-run economics exactly per §3.2. Returns typed errors (never panics/NaN)
/// for a non-positive duration, an invalid lifetime, zero tokens, or any **negative** physical
/// input. Negatives are rejected (not silently turned into a negative cost) so the advertised
/// [`PowerError::InvalidProfile`] contract holds and no negative figure can flow into a FOCUS
/// money record at M3; **zero is allowed** for rate/price (legitimate "free electricity" /
/// "ignore hardware amortization" scenarios) and for power (a zero draw integrates to zero energy).
pub fn local_run_cost(inputs: &CostInputs) -> Result<LocalRunCost, PowerError> {
    if inputs.run_seconds <= 0.0 {
        return Err(PowerError::NonPositiveDuration(inputs.run_seconds));
    }
    if inputs.hardware_lifetime_seconds <= 0.0 {
        return Err(PowerError::InvalidProfile(format!(
            "hardware_lifetime_seconds must be positive, got {}",
            inputs.hardware_lifetime_seconds
        )));
    }
    // Reject non-physical NEGATIVE inputs (R6 honesty): a negative power draw, electricity rate, or
    // hardware price is nonsensical and would yield a negative cost. Zero is permitted (see above).
    if inputs.avg_power_watts < 0.0
        || inputs.electricity_rate_per_kwh < 0.0
        || inputs.hardware_price < 0.0
    {
        return Err(PowerError::InvalidProfile(format!(
            "avg_power_watts / electricity_rate_per_kwh / hardware_price must be non-negative, \
             got {} / {} / {}",
            inputs.avg_power_watts, inputs.electricity_rate_per_kwh, inputs.hardware_price
        )));
    }
    if inputs.tokens_in_run == 0 {
        return Err(PowerError::ZeroTokens);
    }

    let energy_kwh = (inputs.avg_power_watts * inputs.run_seconds) / JOULES_PER_KWH;
    let energy_cost = energy_kwh * inputs.electricity_rate_per_kwh;
    let amortized_hw_cost =
        (inputs.hardware_price / inputs.hardware_lifetime_seconds) * inputs.run_seconds;
    let local_run_cost = energy_cost + amortized_hw_cost;
    let local_cost_per_1m = (local_run_cost / inputs.tokens_in_run as f64) * TOKENS_PER_MILLION;

    Ok(LocalRunCost {
        energy_kwh,
        energy_cost,
        amortized_hw_cost,
        local_run_cost,
        local_cost_per_1m,
    })
}

/// Counterfactual cloud cost for the same token volume (§3.2): `input_tokens * input_price +
/// output_tokens * output_price`, priced from a dated, pinned snapshot (R8). Prices are per
/// single token (not per-1M) to match the formula as written.
pub fn cloud_cost(
    input_tokens: u64,
    output_tokens: u64,
    input_price_per_token: f64,
    output_price_per_token: f64,
) -> f64 {
    input_tokens as f64 * input_price_per_token + output_tokens as f64 * output_price_per_token
}

#[cfg(test)]
mod tests {
    use super::*;

    // Worked example: 160 W for 100 s = 16,000 J = 16000/3.6e6 kWh ≈ 0.004444 kWh.
    // At $0.10/kWh → energy_cost ≈ $0.0004444.
    // HW: $2000 over a 3-year life (94,608,000 s) for 100 s → ≈ $0.0021140.
    // 50,000 tokens → per-1M scales by 20.
    #[test]
    fn local_run_cost_matches_the_worked_example() {
        let inputs = CostInputs {
            avg_power_watts: 160.0,
            run_seconds: 100.0,
            electricity_rate_per_kwh: 0.10,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 3.0 * 365.0 * 24.0 * 3600.0,
            tokens_in_run: 50_000,
        };
        let Ok(c) = local_run_cost(&inputs) else {
            panic!("valid inputs must compute")
        };
        assert!((c.energy_kwh - 0.004_444_444).abs() < 1e-6);
        assert!((c.energy_cost - 0.000_444_444).abs() < 1e-7);
        assert!((c.amortized_hw_cost - 0.002_113_99).abs() < 1e-5);
        assert!((c.local_run_cost - (c.energy_cost + c.amortized_hw_cost)).abs() < 1e-12);
        // per-1M = local_run_cost / 50_000 * 1_000_000 = local_run_cost * 20.
        assert!((c.local_cost_per_1m - c.local_run_cost * 20.0).abs() < 1e-9);
    }

    #[test]
    fn degenerate_runs_are_typed_errors_not_panics() {
        let base = CostInputs {
            avg_power_watts: 160.0,
            run_seconds: 100.0,
            electricity_rate_per_kwh: 0.10,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 94_608_000.0,
            tokens_in_run: 50_000,
        };
        assert!(matches!(
            local_run_cost(&CostInputs {
                run_seconds: 0.0,
                ..base
            }),
            Err(PowerError::NonPositiveDuration(_))
        ));
        assert!(matches!(
            local_run_cost(&CostInputs {
                tokens_in_run: 0,
                ..base
            }),
            Err(PowerError::ZeroTokens)
        ));
        assert!(matches!(
            local_run_cost(&CostInputs {
                hardware_lifetime_seconds: 0.0,
                ..base
            }),
            Err(PowerError::InvalidProfile(_))
        ));
    }

    #[test]
    fn negative_physical_inputs_are_rejected_not_turned_into_negative_cost() {
        let base = CostInputs {
            avg_power_watts: 160.0,
            run_seconds: 100.0,
            electricity_rate_per_kwh: 0.10,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 94_608_000.0,
            tokens_in_run: 50_000,
        };
        for bad in [
            CostInputs {
                avg_power_watts: -1.0,
                ..base
            },
            CostInputs {
                electricity_rate_per_kwh: -0.10,
                ..base
            },
            CostInputs {
                hardware_price: -2000.0,
                ..base
            },
        ] {
            assert!(
                matches!(local_run_cost(&bad), Err(PowerError::InvalidProfile(_))),
                "negative physical input must be a typed error, never a negative cost"
            );
        }
        // Zero rate / zero hardware price ARE legitimate scenarios (free power / ignore amortization).
        let Ok(free) = local_run_cost(&CostInputs {
            electricity_rate_per_kwh: 0.0,
            hardware_price: 0.0,
            ..base
        }) else {
            panic!("zero rate + zero hardware price is a valid scenario")
        };
        assert_eq!(free.local_run_cost, 0.0);
    }

    #[test]
    fn cloud_cost_sums_input_and_output_priced_tokens() {
        // 1M in @ $3/M ($0.000003/token) + 0.5M out @ $15/M ($0.000015/token) = $3 + $7.5.
        let c = cloud_cost(1_000_000, 500_000, 0.000_003, 0.000_015);
        assert!((c - 10.5).abs() < 1e-9);
    }
}

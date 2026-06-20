//! The local-inference **subprocess** runner (M3, A2).
//!
//! Per A2 the runner is a **subprocess** to a user-installed `llama.cpp` / `ollama` binary
//! (CLI + stdout/stderr stats) — **not** FFI, **not** the localhost HTTP API. `unsafe_code =
//! "forbid"` holds; no async runtime is reachable.
//!
//! **R4 (Cardinal Rule):** the runner captures **only token counts + timings** from the
//! binary's stats output. The model's generated text is **discarded** — the child's stdout is
//! routed to `/dev/null` (the completion never enters Costroid's memory), and only the stderr
//! stats block is parsed. The fixed benchmark prompt is Costroid's own (not user content);
//! the output is never persisted or exported.
//!
//! In M3a the runner code + the pure stats parsers + a [`StubRunner`] (so the harness is
//! CI-testable with no binary) ship and are golden-tested against committed **stats-only**
//! fixtures. A *real* subprocess run is the M3b on-hardware step.

use std::process::{Command, Stdio};

use crate::error::PowerError;

/// What to run: the binary + model + quant + the fixed benchmark prompt + decode budget.
#[derive(Debug, Clone)]
pub struct RunSpec {
    /// Path to the user-installed runtime binary (e.g. `llama-cli`, `ollama`).
    pub binary_path: String,
    /// The model id or GGUF path passed to the binary.
    pub model: String,
    /// The quantization label (carried for provenance; the GGUF encodes the actual quant).
    pub quant: String,
    /// The FIXED benchmark prompt — Costroid's own, never user content (R4).
    pub prompt: String,
    /// Decode budget (max generated tokens).
    pub max_tokens: u64,
    /// Extra runtime flags (e.g. `--n-gpu-layers 99`); bounded, never content.
    pub extra_args: Vec<String>,
}

/// The runner's output contract (A2): token counts + timings only — never the generated text.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct RunOutput {
    /// Prompt (input) tokens processed.
    pub tokens_in: u64,
    /// Generated (output) tokens.
    pub tokens_out: u64,
    /// Total wall-clock the binary reported for the run, in seconds (the harness prefers its
    /// own measured wall-clock for energy; this is the binary's self-report, for cross-check).
    pub run_seconds: f64,
    /// Decode throughput the binary reported (tokens/sec), if present.
    pub tok_s: Option<f64>,
}

/// A local-inference runtime that produces a [`RunOutput`] from a [`RunSpec`].
pub trait Runner {
    /// A stable kind label for the runtime (`"llama.cpp"` / `"ollama"` / `"stub"`) — rides the
    /// FOCUS row's `x_RuntimeKind`.
    fn kind(&self) -> &str;

    /// Run the spec and return its token counts + timings. Spawns the subprocess, routes
    /// stdout to null (R4 — the completion is discarded), and parses the stderr stats. Returns
    /// a typed [`PowerError`] (missing binary, non-zero exit, unparseable stats) — never panics.
    fn run(&self, spec: &RunSpec) -> Result<RunOutput, PowerError>;
}

/// Run a subprocess, discard its stdout (R4), and return its captured stderr.
fn run_capturing_stderr(spec: &RunSpec, args: &[String]) -> Result<String, PowerError> {
    let output = Command::new(&spec.binary_path)
        .args(args)
        .stdin(Stdio::null())
        // R4: the model's generated text goes to stdout — discard it (never read into memory).
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .output()
        .map_err(|e| PowerError::SensorRead {
            path: spec.binary_path.clone(),
            reason: format!("failed to spawn local-inference runtime: {e}"),
        })?;
    if !output.status.success() {
        return Err(PowerError::SensorRead {
            path: spec.binary_path.clone(),
            reason: format!("runtime exited with status {}", output.status),
        });
    }
    Ok(String::from_utf8_lossy(&output.stderr).into_owned())
}

/// `llama.cpp` (`llama-cli`) subprocess runner.
#[derive(Debug, Clone)]
pub struct LlamaCppRunner;

impl Runner for LlamaCppRunner {
    fn kind(&self) -> &str {
        "llama.cpp"
    }

    fn run(&self, spec: &RunSpec) -> Result<RunOutput, PowerError> {
        let mut args = vec![
            "-m".to_string(),
            spec.model.clone(),
            "-p".to_string(),
            spec.prompt.clone(),
            "-n".to_string(),
            spec.max_tokens.to_string(),
        ];
        args.extend(spec.extra_args.iter().cloned());
        let stderr = run_capturing_stderr(spec, &args)?;
        parse_llama_cpp_stats(&stderr)
    }
}

/// Ollama (`ollama run --verbose`) subprocess runner.
#[derive(Debug, Clone)]
pub struct OllamaRunner;

impl Runner for OllamaRunner {
    fn kind(&self) -> &str {
        "ollama"
    }

    fn run(&self, spec: &RunSpec) -> Result<RunOutput, PowerError> {
        let mut args = vec![
            "run".to_string(),
            spec.model.clone(),
            "--verbose".to_string(),
            spec.prompt.clone(),
        ];
        args.extend(spec.extra_args.iter().cloned());
        let stderr = run_capturing_stderr(spec, &args)?;
        parse_ollama_stats(&stderr)
    }
}

/// A deterministic in-process runner for tests + the CI harness path — no subprocess.
#[derive(Debug, Clone)]
pub struct StubRunner {
    pub output: RunOutput,
}

impl Runner for StubRunner {
    fn kind(&self) -> &str {
        "stub"
    }

    fn run(&self, _spec: &RunSpec) -> Result<RunOutput, PowerError> {
        Ok(self.output)
    }
}

// ============================================================================================
// Pure stats parsers — golden-tested against committed stats-only fixtures (R4: no content).
// ============================================================================================

/// The first whitespace-separated token that parses as `f64`, scanning a slice of a line.
fn first_number(s: &str) -> Option<f64> {
    s.split(|c: char| !(c.is_ascii_digit() || c == '.' || c == '-'))
        .find(|t| !t.is_empty() && t.chars().any(|c| c.is_ascii_digit()))
        .and_then(|t| t.parse::<f64>().ok())
}

/// Parse a duration token like `4.5s`, `4200ms`, `1.5m`, `500µs`, `12ns` into seconds.
fn parse_duration_secs(token: &str) -> Option<f64> {
    let token = token.trim();
    // Order matters: check the longer suffixes first ("ms" before "s", "µs"/"us"/"ns").
    for (suffix, scale) in [
        ("ms", 1e-3),
        ("µs", 1e-6),
        ("us", 1e-6),
        ("ns", 1e-9),
        ("m", 60.0),
        ("s", 1.0),
    ] {
        if let Some(num) = token.strip_suffix(suffix) {
            if let Ok(v) = num.trim().parse::<f64>() {
                return Some(v * scale);
            }
        }
    }
    None
}

/// Parse a `llama.cpp` (`llama_perf_context_print` / `llama_print_timings`) stderr stats block.
///
/// Reads the prompt-eval token count, the decode (eval) token count, the total wall time, and
/// the decode rate — nothing else (R4). Tolerant of the `N runs` vs `N tokens` spelling.
pub fn parse_llama_cpp_stats(stderr: &str) -> Result<RunOutput, PowerError> {
    let mut tokens_in: Option<u64> = None;
    let mut tokens_out: Option<u64> = None;
    let mut run_seconds: Option<f64> = None;
    let mut tok_s: Option<f64> = None;

    for line in stderr.lines() {
        let lower = line.to_lowercase();
        // `... prompt eval time = X ms / N tokens (...)` → N = input tokens.
        if lower.contains("prompt eval time") {
            tokens_in = count_after_slash(line);
        // The decode line: `... eval time = X ms / M runs (... , Y tokens per second)`.
        // Excludes the prompt-eval + sampler lines.
        } else if lower.contains("eval time") && !lower.contains("prompt") {
            tokens_out = count_after_slash(line);
            tok_s = rate_in_parens(line);
        // `... total time = X ms / K tokens` → total wall seconds.
        } else if lower.contains("total time") {
            if let Some(ms) = first_number(line.split('=').nth(1).unwrap_or("")) {
                run_seconds = Some(ms / 1000.0);
            }
        }
    }

    finish(tokens_in, tokens_out, run_seconds, tok_s, "llama.cpp")
}

/// Parse an `ollama run --verbose` stats block (`eval count`, `eval duration`, `eval rate`, …).
pub fn parse_ollama_stats(stderr: &str) -> Result<RunOutput, PowerError> {
    let mut tokens_in: Option<u64> = None;
    let mut tokens_out: Option<u64> = None;
    let mut run_seconds: Option<f64> = None;
    let mut tok_s: Option<f64> = None;

    for line in stderr.lines() {
        let lower = line.to_lowercase();
        let value = line.split(':').nth(1).unwrap_or("").trim();
        if lower.starts_with("prompt eval count") {
            tokens_in = first_number(value).map(|v| v as u64);
        } else if lower.starts_with("eval count") {
            tokens_out = first_number(value).map(|v| v as u64);
        } else if lower.starts_with("total duration") {
            run_seconds = parse_duration_secs(value);
        } else if lower.starts_with("eval rate") {
            tok_s = first_number(value);
        }
    }

    finish(tokens_in, tokens_out, run_seconds, tok_s, "ollama")
}

/// The integer token count after the `/` on a llama.cpp timing line (`= X ms / N tokens`).
fn count_after_slash(line: &str) -> Option<u64> {
    line.split('/')
        .nth(1)
        .and_then(first_number)
        .map(|v| v as u64)
}

/// The `tokens per second` rate inside the parenthetical of a llama.cpp timing line.
fn rate_in_parens(line: &str) -> Option<f64> {
    let open = line.find('(')?;
    let inner = &line[open + 1..];
    // The rate is the last number before "tokens per second".
    let idx = inner.to_lowercase().find("tokens per second")?;
    first_number_reverse(&inner[..idx])
}

/// The last whitespace-separated `f64` in a slice (for "…, 47.62 tokens per second").
fn first_number_reverse(s: &str) -> Option<f64> {
    s.split(|c: char| !(c.is_ascii_digit() || c == '.' || c == '-'))
        .rfind(|t| !t.is_empty() && t.chars().any(|c| c.is_ascii_digit()))
        .and_then(|t| t.parse::<f64>().ok())
}

/// Assemble a [`RunOutput`], failing closed if a required field is missing.
fn finish(
    tokens_in: Option<u64>,
    tokens_out: Option<u64>,
    run_seconds: Option<f64>,
    tok_s: Option<f64>,
    kind: &str,
) -> Result<RunOutput, PowerError> {
    let (Some(tokens_in), Some(tokens_out), Some(run_seconds)) =
        (tokens_in, tokens_out, run_seconds)
    else {
        return Err(PowerError::SensorRead {
            path: kind.to_string(),
            reason: format!(
                "could not parse token counts + timing from {kind} stats \
                 (in={tokens_in:?} out={tokens_out:?} secs={run_seconds:?})"
            ),
        });
    };
    if tokens_out == 0 {
        return Err(PowerError::ZeroTokens);
    }
    Ok(RunOutput {
        tokens_in,
        tokens_out,
        run_seconds,
        tok_s,
    })
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;

    // Stats-only fixtures (R4): NO prompt, NO completion — only the timing block lines.
    const LLAMA_CPP_STATS: &str = include_str!("../../../fixtures/local/llama-cpp-timings.txt");
    const OLLAMA_STATS: &str = include_str!("../../../fixtures/local/ollama-verbose.txt");

    #[test]
    fn parses_llama_cpp_token_counts_and_timing() {
        let Ok(out) = parse_llama_cpp_stats(LLAMA_CPP_STATS) else {
            panic!("the committed llama.cpp stats fixture must parse");
        };
        assert_eq!(out.tokens_in, 100);
        assert_eq!(out.tokens_out, 200);
        assert!((out.run_seconds - 4.5).abs() < 1e-9);
        let Some(tok_s) = out.tok_s else {
            panic!("the decode rate should parse");
        };
        assert!((tok_s - 47.62).abs() < 1e-6);
    }

    #[test]
    fn parses_ollama_token_counts_and_timing() {
        let Ok(out) = parse_ollama_stats(OLLAMA_STATS) else {
            panic!("the committed ollama stats fixture must parse");
        };
        assert_eq!(out.tokens_in, 100);
        assert_eq!(out.tokens_out, 200);
        assert!((out.run_seconds - 4.5).abs() < 1e-9);
        let Some(tok_s) = out.tok_s else {
            panic!("the decode rate should parse");
        };
        assert!((tok_s - 47.62).abs() < 1e-6);
    }

    #[test]
    fn r4_the_stats_fixtures_carry_no_prompt_or_completion_content() {
        // The fixtures are stats-only by construction; guard it so a future real capture that
        // accidentally includes the generated text can't slip in.
        for fixture in [LLAMA_CPP_STATS, OLLAMA_STATS] {
            let lower = fixture.to_lowercase();
            for forbidden in ["prompt:", "completion", "response:", "assistant:", "user:"] {
                assert!(
                    !lower.contains(forbidden),
                    "R4: a stats fixture must carry no content marker `{forbidden}`"
                );
            }
        }
    }

    #[test]
    fn unparseable_stats_fail_closed_not_panic() {
        assert!(parse_llama_cpp_stats("garbage with no timing block").is_err());
        assert!(parse_ollama_stats("nothing useful here").is_err());
    }

    #[test]
    fn stub_runner_is_deterministic_and_needs_no_subprocess() {
        let stub = StubRunner {
            output: RunOutput {
                tokens_in: 50,
                tokens_out: 1_200,
                run_seconds: 12.5,
                tok_s: Some(96.0),
            },
        };
        let spec = RunSpec {
            binary_path: "unused".to_string(),
            model: "gemma-4-26b-a4b".to_string(),
            quant: "Q4_K_M".to_string(),
            prompt: "fixed benchmark prompt".to_string(),
            max_tokens: 1_200,
            extra_args: vec![],
        };
        let Ok(out) = stub.run(&spec) else {
            panic!("stub runner never fails");
        };
        assert_eq!(out.tokens_out, 1_200);
        assert_eq!(stub.kind(), "stub");
    }

    #[test]
    fn parse_duration_handles_units() {
        assert_eq!(parse_duration_secs("4.5s"), Some(4.5));
        assert_eq!(parse_duration_secs("500ms"), Some(0.5));
        assert_eq!(parse_duration_secs("2m"), Some(120.0));
    }
}

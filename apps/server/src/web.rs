//! Server-rendered HTML + plain-text views over the R4-safe data models (M5 T6).
//!
//! Everything is server-rendered — tables + inline SVG, **no JavaScript** — so each view works with
//! scripting off entirely (the most robust accessible fallback) and references **only** the embedded
//! first-party stylesheet ([`CSS`], baked in via `include_str!`): **zero external requests**, fully
//! offline. Every chart's signal is also carried as a table (never color-alone), and the honesty
//! cues are text: the comparison labels the cloud side a **counterfactual list-price estimate**
//! (MED6) and the break-even view shows the **measurement mode** (MED7). A `?plain` text rendering
//! is offered for screen-reader / pipe use.
//!
//! Note (M5 deviation, flagged for the coordinator): D2 chose vendored `htmx` + `uPlot`, but the
//! offline build cannot fetch those upstream files, so this ships first-party embedded assets
//! (server-rendered SVG + `include_str!` CSS) — same guarantees (embedded, zero CDN, offline,
//! accessible). Swapping in the real libs later is additive (files into `assets/` + `<script>`).

use crate::data::{BreakevenView, ComparisonView, GroupSpend, TimelineBucket, TimelineView, Views};

/// The first-party stylesheet, embedded in the binary (no external reference; works offline).
pub const CSS: &str = include_str!("../assets/costroid.css");
pub const CSS_PATH: &str = "/assets/costroid.css";

/// HTML-escape a bounded metadata value (defense-in-depth — R4 values are ids/numbers/enums with
/// no HTML metacharacters, but we never interpolate a raw value into markup unescaped).
fn esc(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for ch in value.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&#39;"),
            _ => out.push(ch),
        }
    }
    out
}

/// Wrap a view body in the shared layout: links ONLY the embedded same-origin stylesheet.
fn layout(title: &str, active: &str, body: &str) -> String {
    let nav = |path: &str, label: &str| -> String {
        let class = if path == active {
            " class=\"active\""
        } else {
            ""
        };
        format!("<a href=\"{path}\"{class}>{label}</a>")
    };
    format!(
        "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">\
         <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\
         <title>Costroid — {title}</title>\
         <link rel=\"stylesheet\" href=\"{CSS_PATH}\"></head><body>\
         <h1>Costroid</h1>\
         <nav>{home}{timeline}{comparison}{breakeven}</nav>\
         {body}\
         <footer>Local-only · loopback · no network · estimates reconcile against your provider invoice.</footer>\
         </body></html>",
        title = esc(title),
        home = nav("/", "home"),
        timeline = nav("/timeline", "timeline"),
        comparison = nav("/comparison", "comparison"),
        breakeven = nav("/breakeven", "break-even"),
        body = body,
    )
}

/// A simple server-rendered SVG bar chart (no JS). `bars` is `(label, value, css_class)`; the table
/// beside it carries the same numbers (never color-alone).
fn svg_bars(bars: &[(String, f64, &str)]) -> String {
    if bars.is_empty() {
        return String::new();
    }
    let max = bars.iter().map(|(_, v, _)| *v).fold(0.0_f64, f64::max);
    let width = 60.0_f64;
    let gap = 12.0_f64;
    let height = 120.0_f64;
    let mut rects = String::new();
    for (index, (label, value, class)) in bars.iter().enumerate() {
        let x = gap + (index as f64) * (width + gap);
        let h = if max > 0.0 {
            (value / max) * (height - 24.0)
        } else {
            0.0
        };
        let y = height - 18.0 - h;
        let class_attr = if class.is_empty() {
            String::new()
        } else {
            format!(" class=\"{}\"", esc(class))
        };
        rects.push_str(&format!(
            "<rect x=\"{x:.0}\" y=\"{y:.1}\" width=\"{width:.0}\" height=\"{h:.1}\"{class_attr}></rect>\
             <text x=\"{tx:.0}\" y=\"{ty:.0}\" text-anchor=\"middle\">{label}</text>",
            tx = x + width / 2.0,
            ty = height - 4.0,
            label = esc(label),
        ));
    }
    let total_width = gap + (bars.len() as f64) * (width + gap);
    format!(
        "<svg class=\"chart\" role=\"img\" aria-label=\"bar chart (figures in the table below)\" \
         width=\"{total_width:.0}\" height=\"{height:.0}\" viewBox=\"0 0 {total_width:.0} {height:.0}\">\
         {rects}</svg>"
    )
}

fn to_f64(value: &str) -> f64 {
    value.parse::<f64>().unwrap_or(0.0)
}

pub fn index_html(_views: &Views) -> String {
    let body = "<p class=\"muted\">Local-only views over your stored cost ledger.</p>\
        <ul>\
        <li><a href=\"/timeline\">Timeline</a> — spend over time, by tool/model</li>\
        <li><a href=\"/comparison\">Comparison</a> — actual local vs counterfactual cloud list price</li>\
        <li><a href=\"/breakeven\">Break-even</a> — the local-vs-cloud crossover</li>\
        </ul>\
        <p class=\"muted\">JSON API: <code>/api/timeline</code> · <code>/api/comparison</code> · \
        <code>/api/breakeven</code>. Append <code>?plain</code> to any view for text output.</p>";
    layout("local cost views", "/", body)
}

pub fn timeline_html(view: &TimelineView) -> String {
    let bars: Vec<(String, f64, &str)> = view
        .buckets
        .iter()
        .map(|b: &TimelineBucket| {
            let label = b.start.get(0..10).unwrap_or(&b.start).to_string();
            (label, to_f64(&b.effective_usd), "")
        })
        .collect();
    let mut body = format!("<h2>Spend over time ({})</h2>", esc(&view.period));
    if view.buckets.is_empty() {
        body.push_str("<p class=\"note\">No spend recorded yet.</p>");
    } else {
        body.push_str(&svg_bars(&bars));
        body.push_str(
            "<table><caption>effective spend per period (USD)</caption>\
            <tr><th>period start</th><th>spend</th></tr>",
        );
        for b in &view.buckets {
            body.push_str(&format!(
                "<tr><td>{}</td><td class=\"num\">{}</td></tr>",
                esc(&b.start),
                esc(&b.effective_usd)
            ));
        }
        body.push_str("</table>");
    }
    body.push_str(&group_table(&view.by_group));
    layout("timeline", "/timeline", &body)
}

fn group_table(groups: &[GroupSpend]) -> String {
    if groups.is_empty() {
        return String::new();
    }
    let mut out = String::from(
        "<h2>By tool / model</h2><table><caption>effective spend by lane + model (USD)</caption>\
         <tr><th>lane</th><th>model</th><th>spend</th><th>tokens</th></tr>",
    );
    for g in groups {
        out.push_str(&format!(
            "<tr><td>{}</td><td>{}</td><td class=\"num\">{}</td><td class=\"num\">{}</td></tr>",
            esc(&g.lane),
            esc(&g.group),
            esc(&g.effective_usd),
            esc(&g.tokens)
        ));
    }
    out.push_str("</table>");
    out
}

pub fn comparison_html(view: &ComparisonView) -> String {
    let mut body = String::from("<h2>Actual vs counterfactual cloud</h2>");
    if !view.has_data {
        body.push_str(
            "<p class=\"note\">No local or dev-tool spend recorded yet — nothing to compare.</p>",
        );
        return layout("comparison", "/comparison", &body);
    }
    let bars = [
        ("actual".to_string(), to_f64(&view.actual_total_usd), ""),
        (
            "cloud*".to_string(),
            to_f64(&view.cloud_counterfactual_usd),
            "cloud",
        ),
    ];
    body.push_str(&svg_bars(&bars));
    body.push_str(&format!(
        "<table><caption>over {} tokens</caption>\
         <tr><th>line</th><th>USD</th></tr>\
         <tr><td>actual local</td><td class=\"num\">{}</td></tr>\
         <tr><td>actual dev-tool</td><td class=\"num\">{}</td></tr>\
         <tr><td>actual total</td><td class=\"num\">{}</td></tr>\
         <tr><td>cloud (counterfactual*)</td><td class=\"num\">{}</td></tr></table>",
        esc(&view.tokens),
        esc(&view.local_usd),
        esc(&view.dev_usd),
        esc(&view.actual_total_usd),
        esc(&view.cloud_counterfactual_usd),
    ));
    // MED6: the cloud side is a counterfactual list-price estimate, with the pricing snapshot.
    body.push_str(&format!(
        "<p class=\"note\">* Cloud = counterfactual list-price estimate (your tokens × \
         {model} list price — not your actual cloud bill). Pricing snapshot {snap}.</p>",
        model = esc(&view.cloud_model),
        snap = esc(&view.pricing_snapshot_id),
    ));
    layout("comparison", "/comparison", &body)
}

pub fn breakeven_html(view: &BreakevenView) -> String {
    let mut body = String::from("<h2>Local-vs-cloud break-even</h2>");
    if view.no_local {
        body.push_str(&format!(
            "<p class=\"verdict warn\">{}</p>",
            esc(view
                .reason
                .as_deref()
                .unwrap_or("no local runs recorded yet"))
        ));
        return layout("break-even", "/breakeven", &body);
    }

    // The verdict (text cue, never color-alone).
    let (verdict_class, verdict) = match view.outcome.as_str() {
        "crosses_at" => (
            "",
            format!(
                "Local breaks even at {} tokens/day.",
                esc(view.crossover_tokens_per_day.as_deref().unwrap_or("?"))
            ),
        ),
        "always" => ("", "Local is cheaper at every volume.".to_string()),
        "infeasible" => (
            " warn",
            format!(
                "INFEASIBLE: would break even at {} tokens/day, beyond this machine.",
                esc(view.crossover_tokens_per_day.as_deref().unwrap_or("?"))
            ),
        ),
        _ => (
            " warn",
            format!(
                "NEVER breaks even — {}.",
                esc(view
                    .reason
                    .as_deref()
                    .unwrap_or("cloud is at or below the local energy rate"))
            ),
        ),
    };
    body.push_str(&format!(
        "<p class=\"verdict{verdict_class}\">{verdict}</p>"
    ));

    // The sensitivity band (R6 — a range, not a hero number).
    if let (Some(low), Some(high)) = (&view.band_low, &view.band_high) {
        body.push_str(&format!(
            "<p>Sensitivity range: <span class=\"data\">{} … {}</span> tokens/day{}</p>",
            esc(low),
            esc(high),
            if view.band_has_never {
                " — some scenarios never break even"
            } else {
                ""
            }
        ));
    }

    // The assumption stamp — incl. the measurement mode (MED7) + the pricing snapshot.
    body.push_str(
        "<h2>Assumptions (estimate)</h2><table><tr><th>assumption</th><th>value</th></tr>",
    );
    let row =
        |k: &str, v: &str| format!("<tr><td>{}</td><td class=\"num\">{}</td></tr>", k, esc(v));
    if let Some(e) = &view.local_energy_per_million_usd {
        body.push_str(&row("local energy ($/1M tok)", e));
    }
    if let Some(c) = &view.cloud_blended_per_million_usd {
        body.push_str(&row("cloud blended ($/1M tok)", c));
    }
    body.push_str(&row("cloud model", &view.cloud_model));
    body.push_str(&row("hardware ($)", &view.hardware_usd));
    body.push_str(&row("depreciation (days)", &view.depreciation_days));
    body.push_str(&row("output share", &view.output_share));
    body.push_str(&row("measurement", &view.measurement_mode));
    if let Some(snap) = &view.pricing_snapshot_id {
        body.push_str(&row("pricing snapshot", snap));
    }
    body.push_str("</table>");

    // MED6: the cloud side is a list-price counterfactual.
    body.push_str(
        "<p class=\"note\">Cloud = counterfactual list-price estimate (your tokens × current list \
         prices — not your actual cloud bill).</p>",
    );

    // The labeled, dated DeepSWE-Bench $/task reference — never folded into the crossover math.
    if !view.deepswe_reference.is_empty() {
        body.push_str(
            "<h2>Cloud $/task reference (DeepSWE-Bench — labeled, dated; not the crossover)</h2>\
             <table><tr><th>model</th><th>$/task</th><th>benchmark</th><th>as of</th></tr>",
        );
        for point in &view.deepswe_reference {
            body.push_str(&format!(
                "<tr><td>{}</td><td class=\"num\">{}</td><td>{}</td><td>{}</td></tr>",
                esc(&point.model),
                esc(point.dollars_per_task.as_deref().unwrap_or("n/a")),
                esc(&point.benchmark),
                esc(&point.as_of)
            ));
        }
        body.push_str("</table>");
    }
    layout("break-even", "/breakeven", &body)
}

// ---- Plain-text renderings (?plain) — screen-reader / pipe friendly, the same numbers ----

pub fn timeline_plain(view: &TimelineView) -> String {
    let mut out = format!("Spend over time ({})\n", view.period);
    if view.buckets.is_empty() {
        out.push_str("  (no spend recorded yet)\n");
    } else {
        for b in &view.buckets {
            out.push_str(&format!("  {}  ${}\n", b.start, b.effective_usd));
        }
    }
    out.push_str("\nBy lane / model:\n");
    for g in &view.by_group {
        out.push_str(&format!(
            "  {} / {}  ${}  ({} tokens)\n",
            g.lane, g.group, g.effective_usd, g.tokens
        ));
    }
    out
}

pub fn comparison_plain(view: &ComparisonView) -> String {
    if !view.has_data {
        return "Comparison: no local or dev-tool spend recorded yet.\n".to_string();
    }
    format!(
        "Actual vs counterfactual cloud (over {tokens} tokens)\n\
         actual local:    ${local}\n\
         actual dev-tool: ${dev}\n\
         actual total:    ${total}\n\
         cloud (counterfactual list-price estimate, {model}, snapshot {snap}): ${cloud}\n",
        tokens = view.tokens,
        local = view.local_usd,
        dev = view.dev_usd,
        total = view.actual_total_usd,
        model = view.cloud_model,
        snap = view.pricing_snapshot_id,
        cloud = view.cloud_counterfactual_usd,
    )
}

pub fn breakeven_plain(view: &BreakevenView) -> String {
    if view.no_local {
        return format!(
            "Break-even: {}\n",
            view.reason
                .as_deref()
                .unwrap_or("no local runs recorded yet")
        );
    }
    let verdict = match view.outcome.as_str() {
        "crosses_at" => format!(
            "Local breaks even at {} tokens/day.",
            view.crossover_tokens_per_day.as_deref().unwrap_or("?")
        ),
        "always" => "Local is cheaper at every volume.".to_string(),
        "infeasible" => format!(
            "INFEASIBLE: would break even at {} tokens/day, beyond this machine.",
            view.crossover_tokens_per_day.as_deref().unwrap_or("?")
        ),
        _ => format!(
            "NEVER breaks even — {}.",
            view.reason
                .as_deref()
                .unwrap_or("cloud is at or below the local energy rate")
        ),
    };
    let mut out = format!("{verdict}\n");
    if let (Some(low), Some(high)) = (&view.band_low, &view.band_high) {
        out.push_str(&format!("Sensitivity range: {low} … {high} tokens/day\n"));
    }
    out.push_str(&format!(
        "Assumptions (estimate): local ${}/1M, cloud ${}/1M, hardware ${}, {} days, \
         output share {}, measurement {}\n",
        view.local_energy_per_million_usd
            .as_deref()
            .unwrap_or("n/a"),
        view.cloud_blended_per_million_usd
            .as_deref()
            .unwrap_or("n/a"),
        view.hardware_usd,
        view.depreciation_days,
        view.output_share,
        view.measurement_mode,
    ));
    out.push_str("Cloud = counterfactual list-price estimate (not your actual cloud bill).\n");
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::{self, Scenario};
    use costroid_core::focus_records_from_canonical;
    use costroid_providers::{CanonicalEvent, LocalRunEvent};

    fn local_rows() -> Vec<costroid_core::FocusRecord> {
        let Some(ts) = chrono::DateTime::from_timestamp(1_750_000_000, 0) else {
            panic!("a valid timestamp");
        };
        let event = LocalRunEvent {
            timestamp: ts,
            model: "gemma-4-26b-a4b".to_string(),
            quant: "Q4_K_M".to_string(),
            runtime_kind: "ollama".to_string(),
            tokens_in: 1_000,
            tokens_out: 9_000,
            run_seconds: 10.0,
            avg_power_watts: 100.0,
            measurement_mode: "estimated".to_string(),
            energy_wh: 0.5,
            amortized_hw_cost: "0.002".to_string(),
            local_run_cost: "0.005".to_string(),
            electricity_rate_per_kwh: 0.16,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 94_608_000.0,
            hardware_profile_id: "strix-halo-128gb@2026-06-20".to_string(),
            benchmark_id: "test".to_string(),
            billing_currency: "USD".to_string(),
        };
        let Ok(rows) = focus_records_from_canonical(&[CanonicalEvent::Local(event)]) else {
            panic!("normalize");
        };
        rows
    }

    fn views() -> Views {
        let Ok(views) = data::build_views(&local_rows(), &Scenario::default()) else {
            panic!("views build");
        };
        views
    }

    /// The load-bearing offline guarantee: every fetch-triggering attribute in the served markup
    /// (and the CSS) points only at a same-origin path — NO external host. Parsed, not a raw grep.
    #[test]
    fn served_markup_references_only_embedded_same_origin_assets() {
        let views = views();
        let pages = [
            index_html(&views),
            timeline_html(&views.timeline),
            comparison_html(&views.comparison),
            breakeven_html(&views.breakeven),
        ];
        for page in &pages {
            for needle in ["href=\"", "src=\""] {
                let mut rest = page.as_str();
                while let Some(idx) = rest.find(needle) {
                    rest = &rest[idx + needle.len()..];
                    let end = rest.find('"').unwrap_or(rest.len());
                    let value = &rest[..end];
                    assert!(
                        value.starts_with('/') && !value.starts_with("//"),
                        "fetch-triggering attribute must be same-origin (start with `/`, not `//` \
                         or a scheme), got `{value}` in:\n{page}"
                    );
                    rest = &rest[end..];
                }
            }
            // No scheme / protocol-relative / CDN reference anywhere in the markup.
            assert!(!page.contains("http://"), "no http scheme:\n{page}");
            assert!(!page.contains("https://"), "no https scheme:\n{page}");
            assert!(!page.contains("//cdn"), "no CDN reference:\n{page}");
        }
        // The CSS is non-empty and carries no external url() reference.
        assert!(!CSS.is_empty(), "the embedded CSS is non-empty");
        assert!(
            !CSS.contains("http://") && !CSS.contains("https://"),
            "CSS has no external url"
        );
    }

    #[test]
    fn comparison_renders_the_counterfactual_label_and_snapshot() {
        let html = comparison_html(&views().comparison);
        assert!(
            html.contains("counterfactual list-price estimate"),
            "MED6 label rendered:\n{html}"
        );
        assert!(
            html.contains("Pricing snapshot"),
            "snapshot rendered:\n{html}"
        );
    }

    #[test]
    fn breakeven_renders_the_measurement_mode_and_a_text_verdict() {
        let html = breakeven_html(&views().breakeven);
        assert!(
            html.contains("measurement"),
            "MED7 measurement row rendered:\n{html}"
        );
        assert!(
            html.contains("estimated"),
            "measurement mode value rendered:\n{html}"
        );
        // Never color-alone: the verdict is a text sentence.
        assert!(
            html.contains("breaks even") || html.contains("NEVER") || html.contains("every volume"),
            "a text verdict is rendered:\n{html}"
        );
    }

    #[test]
    fn plain_renderings_carry_the_same_honesty_cues() {
        let views = views();
        assert!(comparison_plain(&views.comparison).contains("counterfactual list-price estimate"));
        let be = breakeven_plain(&views.breakeven);
        assert!(
            be.contains("measurement"),
            "plain break-even shows the measurement mode"
        );
        assert!(be.contains("counterfactual list-price estimate"));
        assert!(!timeline_plain(&views.timeline).is_empty());
    }

    #[test]
    fn escaping_neutralizes_html_metacharacters() {
        assert_eq!(esc("a<b>&\"'"), "a&lt;b&gt;&amp;&quot;&#39;");
    }
}

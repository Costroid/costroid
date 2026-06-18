//! Install the bundled JetBrains Mono as the taskbar's only font.
//!
//! The brand uses JetBrains Mono for everything (DESIGN-SYSTEM "Brand basics"); bundling
//! the .ttf directly (OFL-1.1 — see `assets/JetBrainsMono-LICENSE.txt`) means the chrome
//! needs no system font. The `default_fonts` egui feature is OFF: its `epaint_default_fonts`
//! crate declares `(MIT OR Apache-2.0) AND OFL-1.1 AND Ubuntu-font-1.0`, and OFL-1.1 /
//! Ubuntu-font-1.0 are NOT in the cargo-deny allowlist, so pulling that crate would fail the
//! license gate — and we want only the single brand font anyway. (The bundled JetBrains Mono
//! .ttf is ITSELF OFL-1.1, but as a vendored ASSET — not a crate — it sits outside
//! cargo-deny's view, so its license is recorded manually in the LICENSE.txt beside it and
//! accepted under the T18 deps-license gate.) JetBrains Mono lacks glyphs outside its
//! coverage (e.g. braille), which the dot-grid avoids by painting, not typesetting; broader
//! glyph coverage for the in-window braille meters is a T19 concern.

use std::sync::Arc;

/// JetBrains Mono Regular, v2.304 (OFL-1.1). See `assets/JetBrainsMono-LICENSE.txt`.
const JETBRAINS_MONO: &[u8] = include_bytes!("../assets/JetBrainsMono-Regular.ttf");

/// Make JetBrains Mono the first choice for both the proportional and monospace families.
pub fn install(ctx: &egui::Context) {
    let mut fonts = egui::FontDefinitions::empty();
    fonts.font_data.insert(
        "jetbrains-mono".to_owned(),
        Arc::new(egui::FontData::from_static(JETBRAINS_MONO)),
    );
    for family in [egui::FontFamily::Proportional, egui::FontFamily::Monospace] {
        fonts
            .families
            .entry(family)
            .or_default()
            .insert(0, "jetbrains-mono".to_owned());
    }
    ctx.set_fonts(fonts);
}

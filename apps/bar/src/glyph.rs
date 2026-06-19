//! The `C⠉` brand mark as a live dot-grid, hand-rasterized to RGBA.
//!
//! The tray icon is the brand's `C⠉` mark whose **3×3 dot grid is the warning meter**:
//! it fills to the `0..=8` dot-density step of the most-constrained quota window
//! (DESIGN-SYSTEM). Because `tray-icon` takes a rasterized RGBA bitmap (never a
//! font glyph), the mark is drawn here from first principles — the same dot geometry the
//! in-window painter reuses (so the language is identical edge-to-edge). The glyph dots
//! are computed directly, never from a library constant, exactly as the CLI computes its
//! braille from the codepoint (DESIGN-SYSTEM "the braille rendering primitive").
//!
//! This module is egui-free and fully deterministic — `render_tray(step)` is a pure
//! function of the step, so its bytes are unit-testable.

/// Side length (px) of the square tray bitmap. 64 downsamples crisply to the ~16–32 px
/// the OS tray renders at.
pub const ICON_SIZE: u32 = 64;

/// Unit-square x-centers of the grid's three columns (right ~half of the mark; the `C`
/// occupies the left). Shared by the rasterizer and the in-window painter.
pub const GRID_COLS: [f32; 3] = [0.555, 0.715, 0.875];
/// Unit-square y-centers of the grid's three rows.
pub const GRID_ROWS: [f32; 3] = [0.255, 0.5, 0.745];
/// Dot radius in unit-square fraction of the mark's side.
pub const DOT_RADIUS: f32 = 0.082;

/// Order in which the nine grid dots fill as severity rises — a *rising* meter: bottom
/// row first, left→right, then middle, then top (row-major dot indices,
/// `0..=2` top, `3..=5` middle, `6..=8` bottom).
pub const FILL_ORDER: [usize; 9] = [6, 7, 8, 3, 4, 5, 0, 1, 2];

/// Row-major unit-square centers of the nine grid dots (`0` top-left … `8` bottom-right).
pub fn dot_centers() -> [(f32, f32); 9] {
    let mut centers = [(0.0, 0.0); 9];
    for (row, &cy) in GRID_ROWS.iter().enumerate() {
        for (col, &cx) in GRID_COLS.iter().enumerate() {
            centers[row * 3 + col] = (cx, cy);
        }
    }
    centers
}

/// How many of the nine grid dots are filled at a given `0..=8` severity step.
///
/// `0` → none (the idle / empty grid); `1..=7` → that many; `8` → all nine (the full
/// "critical" grid — the design pin's top step fills the whole grid, so the last step
/// jumps `7 → 9` dots).
pub fn dots_filled(step: u8) -> usize {
    match step {
        0 => 0,
        n if n >= 8 => 9,
        n => n as usize,
    }
}

/// RGBA (`[r, g, b, a]`) for the *filled* dots at a severity step — the brand warning
/// ramp keyed to the step number (DESIGN-SYSTEM "Brand
/// basics"): `1–2` green · `3` yellow · `4` orange · `5–6` red · `7` brown · `8`
/// critical. The dot *count* is the primary, grayscale-safe cue; this tint is secondary.
///
/// Build-time decision (T18): the pin fixes the ramp's named colors but not exact hexes;
/// these are on-brand, accessible values, flagged for T19/T21 polish
/// (DESIGN-SYSTEM). The pin's literal "full black grid" at step 8 would be
/// invisible on the dark mark, so step 8 renders as the densest critical red instead.
pub fn step_fill_color(step: u8) -> [u8; 4] {
    match step {
        0 => EMPTY_DOT,
        1 | 2 => [0x5b, 0xd1, 0x7a, 0xff], // green
        3 => [0xe8, 0xd4, 0x4a, 0xff],     // yellow
        4 => [0xe8, 0x92, 0x3d, 0xff],     // orange
        5 | 6 => [0xe0, 0x53, 0x3d, 0xff], // red
        7 => [0x9c, 0x5a, 0x2e, 0xff],     // brown / over
        _ => [0xc8, 0x36, 0x2a, 0xff],     // 8 = critical (densest)
    }
}

/// RGBA for an *unfilled* grid dot — a dim Graphite so the empty grid still reads as the
/// idle/`?` state (the "empty ring") rather than vanishing.
pub const EMPTY_DOT: [u8; 4] = [0x3a, 0x3a, 0x38, 0xff];

/// RGBA for the `C` of the mark — Bone, the brand's primary ink.
pub const MARK_INK: [u8; 4] = [0xe9, 0xe7, 0xdf, 0xff];

/// A rasterized tray icon: tightly-packed RGBA8 rows, `width * height * 4` bytes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TrayBitmap {
    pub rgba: Vec<u8>,
    pub width: u32,
    pub height: u32,
}

/// Render the `C⠉` mark filled to `step` (`0..=8`) as an RGBA bitmap, ready for
/// `tray_icon::Icon::from_rgba`. Pure and deterministic.
pub fn render_tray(step: u8) -> TrayBitmap {
    let size = ICON_SIZE;
    let mut rgba = vec![0u8; (size * size * 4) as usize];

    let filled = dots_filled(step);
    let lit: [bool; 9] = {
        let mut lit = [false; 9];
        for &idx in FILL_ORDER.iter().take(filled) {
            lit[idx] = true;
        }
        lit
    };
    let fill_color = step_fill_color(step);
    let centers = dot_centers();

    for py in 0..size {
        for px in 0..size {
            // Unit-square coordinate of this pixel's center.
            let u = (px as f32 + 0.5) / size as f32;
            let v = (py as f32 + 0.5) / size as f32;

            let mut color = [0u8; 4]; // transparent by default

            if in_c_mark(u, v) {
                color = MARK_INK;
            } else if let Some(dot) = dot_at(u, v, &centers) {
                color = if lit[dot] { fill_color } else { EMPTY_DOT };
            }

            let offset = ((py * size + px) * 4) as usize;
            rgba[offset..offset + 4].copy_from_slice(&color);
        }
    }

    TrayBitmap {
        rgba,
        width: size,
        height: size,
    }
}

/// Index of the grid dot covering unit-square point `(u, v)`, if any.
fn dot_at(u: f32, v: f32, centers: &[(f32, f32); 9]) -> Option<usize> {
    centers.iter().position(|&(cx, cy)| {
        let (dx, dy) = (u - cx, v - cy);
        dx * dx + dy * dy <= DOT_RADIUS * DOT_RADIUS
    })
}

/// Whether unit-square point `(u, v)` lies on the `C` of the mark — a thick ring with a
/// gap on its right (east), occupying the left portion of the icon.
fn in_c_mark(u: f32, v: f32) -> bool {
    const CX: f32 = 0.235;
    const CY: f32 = 0.5;
    const OUTER: f32 = 0.205;
    const INNER: f32 = 0.105;

    let (dx, dy) = (u - CX, v - CY);
    let dist_sq = dx * dx + dy * dy;
    if !(INNER * INNER..=OUTER * OUTER).contains(&dist_sq) {
        return false;
    }
    // Cut the mouth of the `C`: drop the right-facing wedge (|angle from east| < ~38°).
    // dx > 0 (east half) and |dy| < dx * tan(38°) ≈ dx * 0.78.
    !(dx > 0.0 && dy.abs() < dx * 0.78)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn filled_pixels(bmp: &TrayBitmap, color: [u8; 4]) -> usize {
        bmp.rgba.chunks_exact(4).filter(|px| *px == color).count()
    }

    fn opaque_pixels(bmp: &TrayBitmap) -> usize {
        bmp.rgba.chunks_exact(4).filter(|px| px[3] != 0).count()
    }

    #[test]
    fn render_has_correct_dimensions_and_buffer_length() {
        let bmp = render_tray(4);
        assert_eq!(bmp.width, ICON_SIZE);
        assert_eq!(bmp.height, ICON_SIZE);
        assert_eq!(bmp.rgba.len(), (ICON_SIZE * ICON_SIZE * 4) as usize);
    }

    #[test]
    fn render_is_deterministic_per_step() {
        for step in 0..=10u8 {
            assert_eq!(
                render_tray(step),
                render_tray(step),
                "the tray glyph must be byte-identical for a given step"
            );
        }
    }

    #[test]
    fn dots_filled_covers_the_scale() {
        assert_eq!(dots_filled(0), 0);
        assert_eq!(dots_filled(1), 1);
        assert_eq!(dots_filled(7), 7);
        assert_eq!(dots_filled(8), 9, "the top step fills the whole grid");
        assert_eq!(dots_filled(99), 9, "saturates, never panics");
    }

    #[test]
    fn fill_order_is_a_permutation_of_the_nine_dots() {
        let mut seen = FILL_ORDER;
        seen.sort_unstable();
        assert_eq!(seen, [0, 1, 2, 3, 4, 5, 6, 7, 8]);
    }

    #[test]
    fn higher_step_lights_at_least_as_many_dots_in_the_ramp() {
        // The count of *fill-colored* pixels is non-decreasing across the ramp where the
        // color is stable (1→2, 5→6); across color changes we assert the lit-dot count
        // via `dots_filled` instead (color differs so pixel-equality can't compare).
        for step in 1..8u8 {
            assert!(dots_filled(step + 1) >= dots_filled(step));
        }
    }

    #[test]
    fn idle_step_zero_lights_no_fill_color() {
        let idle = render_tray(0);
        // No dot carries a ramp color at idle; the only non-empty dot color would be a
        // fill color, and step 0's fill color IS the empty color, so every grid dot is
        // dim. Assert at least the mark ink and some empty dots are present (a visible,
        // honest idle grid), and that no "green" fill pixels exist.
        assert!(
            filled_pixels(&idle, MARK_INK) > 0,
            "the C mark is always drawn"
        );
        assert!(
            filled_pixels(&idle, EMPTY_DOT) > 0,
            "idle shows the dim grid"
        );
        assert_eq!(
            filled_pixels(&idle, [0x5b, 0xd1, 0x7a, 0xff]),
            0,
            "idle must never paint a confident (green) fill"
        );
    }

    #[test]
    fn full_step_lights_more_than_idle() {
        let idle = render_tray(0);
        let full = render_tray(8);
        // Step 8 fills all nine dots in the critical color; idle fills none — so the
        // count of empty (dim) dot pixels strictly drops from idle to full.
        assert!(
            filled_pixels(&full, EMPTY_DOT) < filled_pixels(&idle, EMPTY_DOT),
            "a fuller grid must dim fewer dots than idle"
        );
        // Both always draw the C and some opaque ink.
        assert!(opaque_pixels(&full) > 0 && opaque_pixels(&idle) > 0);
    }

    #[test]
    fn dot_centers_are_row_major() {
        let centers = dot_centers();
        assert_eq!(centers[0], (GRID_COLS[0], GRID_ROWS[0]));
        assert_eq!(centers[4], (GRID_COLS[1], GRID_ROWS[1]));
        assert_eq!(centers[8], (GRID_COLS[2], GRID_ROWS[2]));
    }
}

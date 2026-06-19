//! The tray icon (the glance) and its event bridge.
//!
//! The tray renders the `C⠉` dot-grid (`glyph.rs`), shows a full tooltip, toggles the
//! window on left-click, and offers a right-click menu (Open / Refresh / Quit) —
//! DESIGN-SYSTEM
//!
//! Threading is forced by the platforms: eframe owns the one `winit` event loop, which
//! does **not** pump GTK, so on **Linux** the tray runs on its own dedicated GTK-main
//! thread and the app talks to it over a command channel. On **macOS/Windows** the tray
//! is created on the main thread and pumped by eframe's own event loop. Either way menu
//! and click events arrive on `tray-icon`'s global channels, which a small **bridge
//! thread** drains and forwards to the app (waking the UI only when something happens).
//!
//! Everything degrades: if the tray can't be created (no SNI/AppIndicator on Linux, etc.)
//! the app runs window-only rather than crashing (DESIGN-SYSTEM). The
//! macOS/Windows path compiles here but is **not yet field-verified** (no such hardware on
//! the dev box; ARCHITECTURE) — the GUI ships as archives + `cargo install
//! costroid-bar` until that matrix is confirmed.

use std::sync::mpsc::{self, Receiver, Sender};
use std::time::Duration;

use crate::glyph::TrayBitmap;

/// Stable menu-item ids, matched when a menu event arrives.
pub const MENU_OPEN: &str = "costroid.open";
pub const MENU_REFRESH: &str = "costroid.refresh";
pub const MENU_QUIT: &str = "costroid.quit";

/// How often the bridge thread polls the tray's global event channels.
const EVENT_POLL: Duration = Duration::from_millis(120);
/// How often the Linux tray thread polls for icon/quit commands.
#[cfg(target_os = "linux")]
const COMMAND_POLL: Duration = Duration::from_millis(150);

/// An action surfaced by the tray, forwarded to the app.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TrayAction {
    /// Left-click — toggle window visibility.
    Toggle,
    /// "Open Costroid" — ensure the window is shown and focused.
    Show,
    /// "Refresh now" — request a fresh collect.
    Refresh,
    /// "Quit" — exit the whole app.
    Quit,
}

/// A handle the app uses to update the tray icon/tooltip and to shut it down.
pub struct TrayController {
    #[cfg(target_os = "linux")]
    cmd_tx: Option<Sender<TrayCommand>>,
    #[cfg(not(target_os = "linux"))]
    tray: Option<tray_icon::TrayIcon>,
}

impl TrayController {
    /// Push a fresh icon + tooltip to the tray (best-effort; a no-op if degraded).
    pub fn update(&self, bitmap: &TrayBitmap, tooltip: &str) {
        #[cfg(target_os = "linux")]
        if let Some(tx) = &self.cmd_tx {
            let _ = tx.send(TrayCommand::Update {
                rgba: bitmap.rgba.clone(),
                width: bitmap.width,
                height: bitmap.height,
                tooltip: tooltip.to_owned(),
            });
        }
        #[cfg(not(target_os = "linux"))]
        if let Some(tray) = &self.tray {
            if let Ok(icon) =
                tray_icon::Icon::from_rgba(bitmap.rgba.clone(), bitmap.width, bitmap.height)
            {
                let _ = tray.set_icon(Some(icon));
            }
            let _ = tray.set_tooltip(Some(tooltip));
        }
    }

    /// Tell the tray to go away (Linux: stop the GTK thread; elsewhere the icon is removed
    /// when the app process exits).
    pub fn shutdown(&self) {
        #[cfg(target_os = "linux")]
        if let Some(tx) = &self.cmd_tx {
            let _ = tx.send(TrayCommand::Quit);
        }
    }

    /// Whether a tray was actually created (false ⇒ window-only).
    pub fn is_active(&self) -> bool {
        #[cfg(target_os = "linux")]
        let active = self.cmd_tx.is_some();
        #[cfg(not(target_os = "linux"))]
        let active = self.tray.is_some();
        active
    }
}

/// Build the tray (idle glyph + tooltip) and return a controller. Degrades to a
/// window-only controller if creation fails.
pub fn spawn(initial: &TrayBitmap, tooltip: &str) -> TrayController {
    #[cfg(target_os = "linux")]
    {
        spawn_linux(initial, tooltip)
    }
    #[cfg(not(target_os = "linux"))]
    {
        spawn_native(initial, tooltip)
    }
}

/// Spawn the bridge thread that forwards tray menu/click events to the app and wakes the
/// UI when one arrives. Returns the action receiver the app drains each frame.
pub fn spawn_event_bridge(ctx: egui::Context) -> Receiver<TrayAction> {
    let (tx, rx) = mpsc::channel::<TrayAction>();
    let spawned = std::thread::Builder::new()
        .name("costroid-bar-tray-events".to_owned())
        .spawn(move || event_bridge_loop(&ctx, &tx));
    if let Err(err) = spawned {
        eprintln!("costroid-bar: could not start the tray event bridge: {err}");
    }
    rx
}

fn event_bridge_loop(ctx: &egui::Context, tx: &Sender<TrayAction>) {
    use tray_icon::menu::MenuEvent;
    use tray_icon::{MouseButton, MouseButtonState, TrayIconEvent};

    let menu_rx = MenuEvent::receiver();
    let tray_rx = TrayIconEvent::receiver();
    loop {
        let mut woke = false;
        while let Ok(event) = menu_rx.try_recv() {
            let action = match event.id.0.as_str() {
                MENU_OPEN => Some(TrayAction::Show),
                MENU_REFRESH => Some(TrayAction::Refresh),
                MENU_QUIT => Some(TrayAction::Quit),
                _ => None,
            };
            if let Some(action) = action {
                if tx.send(action).is_err() {
                    return;
                }
                woke = true;
            }
        }
        while let Ok(event) = tray_rx.try_recv() {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                if tx.send(TrayAction::Toggle).is_err() {
                    return;
                }
                woke = true;
            }
        }
        if woke {
            ctx.request_repaint();
        }
        std::thread::sleep(EVENT_POLL);
    }
}

/// Build the tray icon + the Open/Refresh/Quit menu. Must run on the thread that pumps
/// the tray (the GTK thread on Linux, the main thread elsewhere).
fn build_tray(
    rgba: &[u8],
    width: u32,
    height: u32,
    tooltip: &str,
) -> anyhow::Result<tray_icon::TrayIcon> {
    use tray_icon::menu::{Menu, MenuItem, PredefinedMenuItem};

    let menu = Menu::new();
    menu.append(&MenuItem::with_id(MENU_OPEN, "Open Costroid", true, None))?;
    menu.append(&PredefinedMenuItem::separator())?;
    menu.append(&MenuItem::with_id(MENU_REFRESH, "Refresh now", true, None))?;
    menu.append(&MenuItem::with_id(MENU_QUIT, "Quit", true, None))?;

    let icon = tray_icon::Icon::from_rgba(rgba.to_vec(), width, height)?;
    let tray = tray_icon::TrayIconBuilder::new()
        .with_menu(Box::new(menu))
        .with_tooltip(tooltip)
        .with_title("costroid")
        .with_icon(icon)
        .build()?;
    Ok(tray)
}

// ----- Linux: the tray runs on a dedicated GTK-main thread -----

#[cfg(target_os = "linux")]
enum TrayCommand {
    Update {
        rgba: Vec<u8>,
        width: u32,
        height: u32,
        tooltip: String,
    },
    Quit,
}

#[cfg(target_os = "linux")]
struct TrayInit {
    rgba: Vec<u8>,
    width: u32,
    height: u32,
    tooltip: String,
}

#[cfg(target_os = "linux")]
fn spawn_linux(initial: &TrayBitmap, tooltip: &str) -> TrayController {
    let (cmd_tx, cmd_rx) = mpsc::channel::<TrayCommand>();
    let init = TrayInit {
        rgba: initial.rgba.clone(),
        width: initial.width,
        height: initial.height,
        tooltip: tooltip.to_owned(),
    };
    let spawned = std::thread::Builder::new()
        .name("costroid-bar-tray".to_owned())
        .spawn(move || run_linux_tray(init, cmd_rx));
    match spawned {
        Ok(_handle) => TrayController {
            cmd_tx: Some(cmd_tx),
        },
        Err(err) => {
            eprintln!("costroid-bar: could not start the tray thread: {err}; running window-only.");
            TrayController { cmd_tx: None }
        }
    }
}

#[cfg(target_os = "linux")]
fn run_linux_tray(init: TrayInit, cmd_rx: Receiver<TrayCommand>) {
    use gtk::glib;

    if let Err(err) = gtk::init() {
        eprintln!(
            "costroid-bar: GTK init failed ({err}); the Linux tray is unavailable — running window-only."
        );
        return;
    }
    let tray = match build_tray(&init.rgba, init.width, init.height, &init.tooltip) {
        Ok(tray) => tray,
        Err(err) => {
            eprintln!("costroid-bar: could not create the tray ({err}); running window-only.");
            return;
        }
    };

    // Apply icon/tooltip updates and handle Quit on the GTK loop. The closure owns the
    // (`!Send`) tray, so it must run on this thread — `timeout_add_local` allows that.
    glib::timeout_add_local(COMMAND_POLL, move || loop {
        match cmd_rx.try_recv() {
            Ok(TrayCommand::Update {
                rgba,
                width,
                height,
                tooltip,
            }) => {
                if let Ok(icon) = tray_icon::Icon::from_rgba(rgba, width, height) {
                    let _ = tray.set_icon(Some(icon));
                }
                let _ = tray.set_tooltip(Some(&tooltip));
            }
            Ok(TrayCommand::Quit) => {
                gtk::main_quit();
                return glib::ControlFlow::Break;
            }
            Err(mpsc::TryRecvError::Empty) => return glib::ControlFlow::Continue,
            Err(mpsc::TryRecvError::Disconnected) => {
                gtk::main_quit();
                return glib::ControlFlow::Break;
            }
        }
    });

    gtk::main();
}

// ----- macOS / Windows: the tray is pumped by eframe's own event loop -----

#[cfg(not(target_os = "linux"))]
fn spawn_native(initial: &TrayBitmap, tooltip: &str) -> TrayController {
    match build_tray(&initial.rgba, initial.width, initial.height, tooltip) {
        Ok(tray) => TrayController { tray: Some(tray) },
        Err(err) => {
            eprintln!("costroid-bar: could not create the tray ({err}); running window-only.");
            TrayController { tray: None }
        }
    }
}

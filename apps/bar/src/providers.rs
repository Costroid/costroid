//! The Providers panel — `snapshot.capabilities` + `snapshot.providers`, plus (under
//! `--features connect`) a DISPLAY-ONLY connection lane.
//!
//! Mirrors the CLI's `render_providers_*`: per provider, each data lane's declared source (the §2b
//! `Capability` descriptor), how it authenticates, the quota windows it can report, and its
//! detection health — *what is available, what is unavailable, and why*. Honest by construction: a
//! lane with no clean source renders "no sanctioned source", never a fabricated one.
//!
//! The connection lane (your own usage-API keys) is **display-only and does ZERO network** — it is
//! a read-only join over the local OS keychain + the non-secret connection registry
//! (`is_connected` AND key-present, the dual gate), NEVER a `--check` probe, NEVER key material, and
//! NO connect/disconnect/reconcile action (those stay in the CLI; STEP6-TASKBAR-DESIGN §9). It is
//! compiled only under `--features connect`; the default build links no `costroid-connect` symbol.

use costroid_core::{AuthMethod, DataSource, ProviderCapabilityView, ProviderStatus};

use crate::app::{color_of, ASH, BONE};
use crate::format::{kind_label, provider_label, provider_state_word};

/// Draw the Providers panel's lane sources (always present; the connection lane is appended by
/// [`draw_connection_lane`] under `--features connect`). Pure of app/thread state.
pub fn draw(
    ui: &mut egui::Ui,
    capabilities: &[ProviderCapabilityView],
    statuses: &[ProviderStatus],
) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new("providers")
                .monospace()
                .strong()
                .color(color_of(BONE)),
        );
    });

    if capabilities.is_empty() {
        text_line(ui, "no providers to describe", ASH);
        return;
    }

    for capability in capabilities {
        let status = find_status(statuses, capability);
        ui.add_space(4.0);
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new(provider_label(capability.provider))
                    .monospace()
                    .strong()
                    .color(color_of(BONE)),
            );
            ui.label(
                egui::RichText::new(format!(" ({})", provider_state_word(status)))
                    .monospace()
                    .color(color_of(ASH)),
            );
        });
        text_line(
            ui,
            &format!("  api cost   {}", data_source_phrase(capability.api_cost)),
            ASH,
        );
        text_line(
            ui,
            &format!("  quota      {}", quota_phrase(capability)),
            ASH,
        );
        text_line(
            ui,
            &format!("  model mix  {}", data_source_phrase(capability.model_mix)),
            ASH,
        );
        text_line(
            ui,
            &format!("  auth       {}", auth_phrase(capability.auth)),
            ASH,
        );
        if let Some(note) = status.and_then(|status| status.message.as_deref()) {
            text_line(ui, &format!("  note: {}", sanitize(note)), ASH);
        }
    }
}

fn find_status<'a>(
    statuses: &'a [ProviderStatus],
    capability: &ProviderCapabilityView,
) -> Option<&'a ProviderStatus> {
    statuses
        .iter()
        .find(|status| status.provider == capability.provider)
}

/// Author-written human copy for a [`DataSource`] — pure ASCII, phrased to match the CLI's
/// `data_source_phrase` ("no sanctioned source").
fn data_source_phrase(source: DataSource) -> &'static str {
    match source {
        DataSource::LocalArtifact => "from local logs",
        DataSource::SanctionedHook => "from the statusLine capture; run setup-statusline",
        DataSource::SanctionedOauth | DataSource::ApiKey => "via your connected key",
        DataSource::Unavailable => "no sanctioned source",
    }
}

/// Author-written human copy for an [`AuthMethod`] (mirrors the CLI's `auth_phrase`).
fn auth_phrase(auth: AuthMethod) -> &'static str {
    match auth {
        AuthMethod::None => "no login required",
        AuthMethod::Oauth => "sanctioned OAuth",
        AuthMethod::ApiKey => "your own API key",
    }
}

/// The quota line: the subscription-quota source plus the windows the provider can report (mirrors
/// the CLI's `quota_phrase`). An empty `quota_kinds` carries no window suffix.
fn quota_phrase(capability: &ProviderCapabilityView) -> String {
    let source = data_source_phrase(capability.subscription_quota);
    if capability.quota_kinds.is_empty() {
        source.to_string()
    } else {
        let kinds = capability
            .quota_kinds
            .iter()
            .map(|kind| kind_label(*kind))
            .collect::<Vec<_>>()
            .join(", ");
        format!("{source} ({kinds})")
    }
}

/// Strip control characters from any provider-/server-supplied string before it reaches the
/// painter (defense in depth — the registry already sanitizes org labels at ingestion).
fn sanitize(value: &str) -> String {
    value.chars().filter(|ch| !ch.is_control()).collect()
}

/// A single indented monospace text line.
fn text_line(ui: &mut egui::Ui, text: &str, ink: [u8; 4]) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(egui::RichText::new(text).monospace().color(color_of(ink)));
    });
}

// ---------------------------------------------------------------------------
// Connection lane (display-only, ZERO network) — `--features connect` only.
// ---------------------------------------------------------------------------

/// A single billing-vendor's connection state for the display-only connection lane, gathered
/// read-only over the existing keychain/registry (NO network). Carries only the non-secret org
/// label — NEVER key material. Mirrors the CLI's `render::ConnectionEntry`.
#[cfg(feature = "connect")]
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConnectionEntry {
    pub vendor: String,
    pub state: ConnectionState,
}

#[cfg(feature = "connect")]
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ConnectionState {
    /// Linked: the key is present in the OS keychain AND the registry marks it connected (the dual
    /// gate). `org` is the non-secret organization label, if captured.
    Connected {
        org: Option<String>,
    },
    NotConnected,
    /// No sanctioned source (e.g. Gemini): carries the pinned unavailable message verbatim.
    Unavailable(String),
}

/// Gather the per-vendor connection lane read-only over the OS keychain + the non-secret registry —
/// NO network, NEVER a `--check` probe, NEVER key material. Degrades to an empty/partial lane if the
/// keychain or registry is unreachable (never aborts). Mirrors the CLI's `gather_connection_entries`.
#[cfg(feature = "connect")]
pub fn gather_connections() -> Vec<ConnectionEntry> {
    use costroid_connect::{ConnectionRegistry, CredentialStore};

    let store = match CredentialStore::new() {
        Ok(store) => store,
        Err(_) => return Vec::new(),
    };
    let registry = match ConnectionRegistry::open() {
        Ok(registry) => registry,
        Err(_) => return Vec::new(),
    };
    connection_entries(&store, &registry)
}

/// Build the lane over an already-open keychain + registry — the dual gate (`is_connected` AND the
/// key present in the keychain), the non-secret org label, and Gemini's pinned "unavailable"
/// message. Read-only, NO network, NEVER key material; a per-vendor read error degrades that vendor
/// to "not connected". Mirrors the CLI's `connection_entries`.
#[cfg(feature = "connect")]
fn connection_entries(
    store: &costroid_connect::CredentialStore,
    registry: &costroid_connect::ConnectionRegistry,
) -> Vec<ConnectionEntry> {
    use costroid_connect::ApiVendor;

    let mut entries = Vec::new();
    for vendor in ApiVendor::ALL {
        let state = match vendor {
            ApiVendor::Gemini => {
                ConnectionState::Unavailable(costroid_core::GEMINI_UNAVAILABLE_MESSAGE.to_string())
            }
            ApiVendor::Anthropic | ApiVendor::OpenAI => {
                let connected = registry.is_connected(vendor).unwrap_or(false)
                    && store
                        .retrieve(vendor)
                        .map(|key| key.is_some())
                        .unwrap_or(false);
                if connected {
                    let org = registry.label(vendor).ok().flatten().map(format_org_label);
                    ConnectionState::Connected { org }
                } else {
                    ConnectionState::NotConnected
                }
            }
        };
        entries.push(ConnectionEntry {
            vendor: vendor.to_string(),
            state,
        });
    }
    entries
}

/// The non-secret organization label, `name (id)` or just `name`, control-chars stripped. Never key
/// material. Mirrors the CLI's `format_org_label`.
#[cfg(feature = "connect")]
fn format_org_label(label: costroid_connect::OrgLabel) -> String {
    let name = sanitize(&label.name);
    match label.id {
        Some(id) => format!("{name} ({})", sanitize(&id)),
        None => name,
    }
}

/// Draw the display-only connection lane (a no-op for an empty slice). Read-only: connected / not
/// connected / the pinned unavailable message + the non-secret org label — NEVER key material, NO
/// action buttons (connect/disconnect/reconcile stay in the CLI). Pure of keychain/thread state.
#[cfg(feature = "connect")]
pub fn draw_connection_lane(ui: &mut egui::Ui, connections: &[ConnectionEntry]) {
    if connections.is_empty() {
        return;
    }
    ui.add_space(4.0);
    text_line(ui, "connections (your own usage API keys)", ASH);
    for entry in connections {
        let detail = match &entry.state {
            ConnectionState::Connected { org: Some(org) } => {
                format!("connected - organization {org}")
            }
            ConnectionState::Connected { org: None } => "connected".to_string(),
            ConnectionState::NotConnected => "not connected".to_string(),
            ConnectionState::Unavailable(message) => sanitize(message),
        };
        text_line(ui, &format!("  {:<10} {detail}", entry.vendor), ASH);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use costroid_core::{AuthMethod, DataSource, LimitKind, ProviderId, ProviderStatusKind};

    fn capability(provider: ProviderId) -> ProviderCapabilityView {
        ProviderCapabilityView {
            provider,
            api_cost: DataSource::LocalArtifact,
            subscription_quota: DataSource::SanctionedHook,
            model_mix: DataSource::LocalArtifact,
            auth: AuthMethod::None,
            quota_kinds: vec![LimitKind::FiveHour, LimitKind::Weekly],
        }
    }

    fn unavailable_capability(provider: ProviderId) -> ProviderCapabilityView {
        ProviderCapabilityView {
            provider,
            api_cost: DataSource::Unavailable,
            subscription_quota: DataSource::Unavailable,
            model_mix: DataSource::LocalArtifact,
            auth: AuthMethod::None,
            quota_kinds: Vec::new(),
        }
    }

    fn status(
        provider: ProviderId,
        kind: ProviderStatusKind,
        message: Option<&str>,
    ) -> ProviderStatus {
        ProviderStatus {
            provider,
            status: kind,
            files: 0,
            usage_events: 0,
            focus_rows: 0,
            limit_windows: 0,
            message: message.map(str::to_string),
        }
    }

    #[test]
    fn lane_phrases_are_honest_and_ascii() {
        assert_eq!(
            data_source_phrase(DataSource::Unavailable),
            "no sanctioned source"
        );
        assert_eq!(
            data_source_phrase(DataSource::LocalArtifact),
            "from local logs"
        );
        assert_eq!(auth_phrase(AuthMethod::ApiKey), "your own API key");
        // An empty quota_kinds carries no window suffix.
        assert_eq!(
            quota_phrase(&unavailable_capability(ProviderId::Cursor)),
            "no sanctioned source"
        );
        // A windowed provider names its kinds.
        assert!(quota_phrase(&capability(ProviderId::ClaudeCode)).contains("(5h, wk)"));
    }

    #[test]
    fn sanitize_strips_control_chars() {
        assert_eq!(sanitize("ok\u{0007}\nname"), "okname");
    }

    #[test]
    fn headless_draw_covers_empty_and_populated() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let caps = vec![
            capability(ProviderId::ClaudeCode),
            unavailable_capability(ProviderId::Cursor),
        ];
        let statuses = vec![
            status(ProviderId::ClaudeCode, ProviderStatusKind::Available, None),
            status(
                ProviderId::Cursor,
                ProviderStatusKind::Detected,
                Some("BETA - usage unavailable - no sanctioned source"),
            ),
        ];
        let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
            draw(ui, &caps, &statuses);
        });
        // The empty-capability state renders the honest note, not a panic.
        let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
            draw(ui, &[], &[]);
        });
    }

    #[cfg(feature = "connect")]
    #[test]
    fn connection_lane_draws_each_state() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let entries = vec![
            ConnectionEntry {
                vendor: "anthropic".to_string(),
                state: ConnectionState::Connected {
                    org: Some("Acme (org-123)".to_string()),
                },
            },
            ConnectionEntry {
                vendor: "openai".to_string(),
                state: ConnectionState::NotConnected,
            },
            ConnectionEntry {
                vendor: "gemini".to_string(),
                state: ConnectionState::Unavailable("unavailable - no sanctioned API".to_string()),
            },
        ];
        let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
            draw_connection_lane(ui, &entries);
            draw_connection_lane(ui, &[]); // empty -> no-op
        });
    }
}

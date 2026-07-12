// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package gcpfocusbq implements the "gcp-focus-bq" connector for Google's
// first-party FOCUS billing export in a Google-managed BigQuery linked dataset.
// The export is Preview (Pre-GA), is provided as-is, and may change schema.
// Costroid therefore probes the live table on every sync, selects a fixed
// explicit column list, warns on additive drift, and fails if a selected column
// disappears. It never claims that the source is fully FOCUS-conformant.
//
// # Discovery and incremental sync
//
// Discovery is deliberately single-tier. Every run performs tables.get (schema
// and time-partitioning probe only) and exactly one aggregate query grouped by
// BillingPeriodStart month. Each month's MAX(x_ExportTime) plus row count is a
// documented change token, not a content digest. The token is encoded into the
// existing four-field storage.SyncState tuple by the CLI and is also returned by
// ContentHash without consuming Records. A matching tenant-scoped tuple skips
// the month before any per-period query. There is no table-level sentinel state.
//
// Corrections are delivered as negation/replacement rows in a later invoice
// month. Costroid's existing per-invoice-month transactional replacement keeps
// those rows additive in the delivery month while preventing double counting on
// a restated sync.
//
// # GBQ gap-fill and extension rules
//
// Rules run before the frozen FOCUS 1.2 to 1.4 transform. "Documented Google
// mitigation" means Google's conformance report points to that extension;
// "Costroid calibration" means Costroid supplies or rejects a value under the
// strict exact-billing posture.
//
//	rule   class                         behavior
//	-----  ----------------------------  --------------------------------------
//	GBQ-1  Costroid calibration          Null ServiceCategory becomes "Other",
//	                                      a legal FOCUS value. No maintained GCP
//	                                      service taxonomy is guessed.
//	GBQ-2  Costroid calibration          Null InvoiceIssuerName becomes
//	                                      "Google Cloud", the definitionally
//	                                      known issuer of this export.
//	GBQ-3  Costroid calibration          When ProviderName and PublisherName are
//	                                      both null, ProviderName becomes
//	                                      "Google Cloud" so the frozen transform
//	                                      derives ServiceProviderName normally.
//	GBQ-4  Costroid calibration          Every other post-transform not-null
//	                                      value is never synthesized. Null money,
//	                                      currency, category, period, account, or
//	                                      service values fail the period with the
//	                                      pipeline's row-numbered errors.
//	GBQ-5  documented Google mitigation  Tags are encoded only from x_Labels.
//	                                      Duplicate label keys fail (FOCUS
//	                                      KeyValueFormat requires uniqueness).
//	                                      x_SystemLabels, x_ProjectLabels, and
//	                                      x_Tags are deferred.
//	GBQ-6  documented Google mitigation  x_Credits is deliberately unread. The
//	                                      row is still ingested and its FOCUS
//	                                      BilledCost/EffectiveCost values remain
//	                                      byte-verbatim; no credit or CUD math is
//	                                      invented.
//	GBQ-7  Costroid calibration          Google's unsupported ChargeCategory
//	                                      Purchase/Credit and PricingCategory
//	                                      Dynamic values receive no special case;
//	                                      any delivered value follows the shared
//	                                      FOCUS validation unchanged.
//
// Nullable columns outside the shared not-null set remain absent. In particular,
// Costroid does not invent InvoiceId, ResourceType, ChargeFrequency, commitment
// fields, capacity reservation fields, or price-sheet fields.
//
// # Credentials and least privilege
//
// The connector accepts service-account JSON only. It parses the PKCS#8 RSA key
// in memory, exchanges an RS256 JWT for a BigQuery-scoped bearer token, and never
// caches the token on disk. Errors never echo JSON, PEM, or credential-bearing
// input. The CLI accepts either GOOGLE_APPLICATION_CREDENTIALS (the env-var file
// leg only) or encrypted-vault JSON; an explicit --credential slot wins.
//
// Runtime access should be limited to dataset-level roles/bigquery.dataViewer on
// the linked dataset plus roles/bigquery.jobUser on the query-job project. That
// pair is a Costroid least-privilege inference, not a Google-documented FOCUS
// requirement, and must be verified on first use. The one-time export-enablement
// administrator roles must never be granted to the connector identity.
package gcpfocusbq

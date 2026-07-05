// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package azurefocus implements the "azure-focus" connector (decisions
// D16, D24, D29): live ingestion of an Azure Cost Management "FOCUS
// 1.2-preview" export — gzipped CSV — directly from Blob Storage. It
// mirrors the aws-focus-s3 connector's shape (discovery yields one
// pipeline connector per billing period) and reuses its CSV parsing;
// discovery, transport, and Azure's documented conformance gap-fill
// differ.
//
// # Discovery model
//
// Discover lists the export under
// <account-url>/<container>/<prefix>, where <prefix> is the export root
// as Azure delivers it — the export's configured storage directory plus
// the export name. Azure writes, per run, size-partitioned .csv.gz files
// (each a complete file with its own header row) plus a manifest.json
// listing them; the manifest is delivered after the data files (the
// run's commit point). The delivery FOLDER LAYOUT
// ([YYYYMMDD-YYYYMMDD]/[RunID]/...) is NOT a stable contract, so —
// exactly like Microsoft's own FinOps hubs — discovery performs ONE flat
// listing, finds blobs whose name ends in "manifest.json", and reads
// everything else from the manifests: the billing period from
// runInfo.startDate, the run's recency from runInfo.submittedTime, and
// the data files from blobs[]. Folder names are never parsed and data
// files are never globbed.
//
// Both delivery modes are handled by the same rule. With
// dataOverwriteBehavior=OverwritePreviousReport there is one run folder
// per month, replaced (at a new folder name) on every run; with
// CreateNewReport superseded runs accumulate and their manifests remain
// listed forever. A period's CURRENT run is simply the listed manifest
// with the greatest runInfo.submittedTime (ties broken by manifest
// name). Azure documents no atomicity for folder replacement, so every
// listed data blob is verified against the manifest's byteCount before
// reading and pinned via If-Match while reading; a mismatch or a
// ConditionNotMet means the run was replaced mid-read — the actionable
// error says to re-run ingest.
//
// Closed months are never hard-frozen: Azure refreshes month-to-date
// daily and, for the first five days of each month, regenerates the
// ENTIRE prior month, so every sync re-lists and re-decides all periods.
//
// A prefix shared by SEVERAL exports is refused per affected period:
// their runs would share a source identity and silently replace each
// other's data on alternating syncs, so a period whose manifests carry
// more than one exportConfig.exportName gets an actionable error telling
// the user to point --prefix at one export's root.
//
// # Manifest-attribution cache and incremental sync
//
// Manifest blobs are immutable once written (a refresh writes a new run
// folder), so a manifest's attribution — its (key, ETag, LastModified,
// size) listing tuple mapped to (billing period, submittedTime) — is
// permanent once the manifest has been fetched. Discover persists these
// attributions (storage.ManifestAttribution, migration 0004) and, on
// every sync, attributes listed manifests from the cache when their
// tuple matches, fetching ONLY never-seen (or changed) manifest blobs.
//
// A period is skipped when its current manifest's tuple equals the
// stored sync tuple (storage.SyncState, upserted by the CLI after every
// successful outcome) — and, tenant-awareness being the caller's job,
// the CLI only supplies tuples whose stored batch tenant matches the
// requested tenant. Because superseded manifests are attributed from
// the cache without fetching, an unchanged re-sync costs ZERO Get Blob
// calls in BOTH delivery modes — one listing, nothing else.
//
// # Source identity
//
// SourceIdentity is
// "<account-host[/account-path]>/<container>/<prefix>/<billing-period>"
// — the account URL (scheme stripped), container, and export prefix are
// part of the identity so two same-named exports in different accounts
// or containers never silently replace each other's data. Moving an
// export creates a NEW identity; data ingested under the old one stays
// until the cross-source supersede machinery (a later slice) retires it.
//
// # Content hash — a documented change token, not a content digest
//
// Azure blob ETags are opaque change tokens: rewriting identical bytes
// yields a new ETag, and no content digest is available without
// downloading. ContentHash is therefore defined as a CHANGE TOKEN:
// "sha256:<hex>" over the current manifest's sorted
// "<blobName>\t<byteCount>\t<dataRowCount>" lines plus
// exportConfig.dataVersion. A regenerated run writes a new run folder,
// so its blob names change and the token changes with it; a
// false-positive re-ingest (token changed, bytes identical) is
// acceptable because the per-period replace is idempotent. The manifest
// tuple skip above — not this hash — is the primary zero-fetch guard.
//
// # Gap-fill: Azure's documented FOCUS conformance holes
//
// Microsoft self-reports 94% FOCUS 1.2 conformance for the 1.2-preview
// dataset (learn.microsoft.com/cloud-computing/finops/focus/
// conformance-summary). The connector owns a documented, tested
// PRE-TRANSFORM normalization step — GapFill, applied to each raw row
// before the UNCHANGED shared 1.2→1.4 transform — with one rule per
// documented hole. No rule ever nulls a column the shared validation
// requires non-null.
//
//	rule   column(s)                 behavior
//	-----  ------------------------  ------------------------------------
//	AZF-1  ServiceName               Empty on EA Marketplace purchases →
//	                                 filled with PublisherName
//	                                 (Microsoft's suggested fill,
//	                                 conformance summary "ServiceName").
//	                                 GATED: PublisherName fill is
//	                                 documented for EA Marketplace rows
//	                                 only, so it applies to a Purchase row
//	                                 only when an affirmative marketplace
//	                                 signal is present — x_PublisherType ==
//	                                 "Marketplace" when Azure emits that
//	                                 column. Without the signal a Purchase
//	                                 row prefers AZF-2.
//	AZF-1b ServiceName               Costroid extension — NOT documented
//	                                 Azure behavior. Any still-empty
//	                                 ServiceName carrying a PublisherName
//	                                 (a non-purchase row, or a purchase
//	                                 with neither a marketplace signal nor
//	                                 an x_SkuMeterSubcategory) is filled
//	                                 with PublisherName as a last resort.
//	                                 It fills row shapes Microsoft does not
//	                                 document; it never nulls or overwrites
//	                                 a populated column.
//	AZF-2  ServiceName               Empty on the documented MCA cases
//	                                 (reservation/savings-plan purchases,
//	                                 rounding adjustments, MACC
//	                                 shortfall, Azure credit records) →
//	                                 filled with x_SkuMeterSubcategory
//	                                 (Microsoft's suggested fill). Rows
//	                                 are NEVER dropped; a row no rule can
//	                                 fill still fails validation loudly.
//	AZF-3  ListUnitPrice             Delivered as 0 where no list price
//	                                 exists (EA/MCA Marketplace,
//	                                 reservation usage) → null, ONLY when
//	                                 ListCost is also 0 (the documented
//	                                 gap shape); a zero price next to a
//	                                 non-zero cost is left as delivered.
//	                                 AMBIGUITY (honest): a genuinely free
//	                                 row with a real 0 list price beside a
//	                                 real 0 ListCost is indistinguishable
//	                                 from Azure's "no price available"
//	                                 placeholder, so this rule nulls it
//	                                 too. Nulling an already-null-meaning
//	                                 zero is harmless for reporting; a
//	                                 price-sheet reconciliation slice that
//	                                 needs the distinction will narrow the
//	                                 gate by row type.
//	AZF-4  ContractedUnitPrice       Same as AZF-3 (same ambiguity), gated
//	                                 on ContractedCost = 0 (EA Marketplace,
//	                                 EA reservation usage with cost
//	                                 allocation, all MCA reservation
//	                                 usage).
//	AZF-5  *PeriodStart/*PeriodEnd   Only MANIFEST timestamps are
//	                                 documented timezone-less by Microsoft;
//	                                 the data columns are not documented to
//	                                 vary. This rule keeps DEFENSIVE
//	                                 parsing anyway — timestamps with or
//	                                 without seconds and with or without a
//	                                 timezone suffix are normalized to
//	                                 RFC 3339 UTC here, in the pre-transform
//	                                 step — so a future export quirk cannot
//	                                 fail an otherwise-conformant row;
//	                                 focus.ParseTime and the shared
//	                                 validation stay unchanged (no
//	                                 cross-source loosening).
//
// ListCost and ContractedCost themselves are mandatory non-null FOCUS
// columns and Azure's zeros are kept EXACTLY as delivered: they are
// documented as known-understated for the row types above
// (price-sheet reconciliation is a later slice). Columns Microsoft
// documents as "currently null/empty" (CapacityReservationId/Status,
// CommitmentDiscountQuantity/Unit, SkuPriceDetails) and the entirely
// absent AvailabilityZone are simply absent — never errors. SkuId /
// SkuPriceId / PublisherName / ChargeDescription nulls on their
// documented row types are genuinely nullable columns and pass through.
//
// Restated months arrive as whole-month re-deliveries and flow through
// the pipeline's per-period transactional replace (decision D26a);
// ChargeClass="Correction" rows pass through unchanged as additive rows
// of the delivering period (D26b).
//
// # Not persisted this slice
//
// Azure's 52 x_ extension columns are parsed but NOT persisted: the
// shared 1.2→1.4 transform drops non-FOCUS columns. x_SkuMeterSubcategory
// is consulted by AZF-2 before the transform. x_BilledCostInUsd and
// friends may matter for invoice reconciliation later; persisting them
// is a documented limitation of this slice.
//
// # Credentials (decision D24 analog)
//
// Authentication uses ONLY azidentity.NewDefaultAzureCredential — the
// ambient chain, in order: Environment (AZURE_TENANT_ID +
// AZURE_CLIENT_ID + AZURE_CLIENT_SECRET or certificate), Workload
// Identity, Managed Identity (IMDS), Azure CLI, Azure Developer CLI,
// Azure PowerShell. The AZURE_TOKEN_CREDENTIALS environment variable
// subsets the chain: "prod" (Environment + Workload + Managed
// Identity), "dev" (CLI/azd/PowerShell), or a single credential name
// such as "EnvironmentCredential". Costroid persists no Azure
// credentials, accepts no credential flags, and rejects account URLs
// carrying query parameters (no SAS tokens, no account keys — D17).
// Nothing credential-shaped is logged, and query strings are stripped
// from any URL that reaches an error message.
//
// The least-privilege grant is the built-in "Storage Blob Data Reader"
// role (2a2b9908-6ea1-4ae2-8e65-a410df84e7d1), scoped to the export
// CONTAINER — not the account:
//
//	az role assignment create \
//	  --assignee <principal-id> \
//	  --role "Storage Blob Data Reader" \
//	  --scope "/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Storage/storageAccounts/<account>/blobServices/default/containers/<container>"
//
// The connector performs read-only calls only: List Blobs and Get Blob.
// The SDK's standard AZURE_* variables configure the chain; Costroid
// adds none of its own, which is why none appear in .env.example.
//
// # Test-only escape: COSTROID_AZURE_INSECURE_NO_AUTH
//
// Setting COSTROID_AZURE_INSECURE_NO_AUTH=1 makes the connector build
// an unauthenticated client (azblob.NewClientWithNoCredential) — and is
// honored ONLY for http:// account URLs, i.e. the local fakeblob dev
// tool; for https:// URLs it is refused with an error. It exists solely
// so tests and the offline end-to-end verification can run against the
// fake without a token; never set it against real Azure (real Azure
// would reject anonymous requests anyway, and the https refusal makes
// the escape unusable there by construction).
package azurefocus

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/storage"
)

// Name is the connector's registry name.
const Name = "azure-focus"

// InsecureNoAuthEnv is the test-only escape that disables
// authentication for http:// account URLs (the fakeblob dev tool). See
// the package documentation; it is refused for https:// URLs.
const InsecureNoAuthEnv = "COSTROID_AZURE_INSECURE_NO_AUTH"

// dataReaderRole names the least-privilege RBAC grant in error messages.
const dataReaderRole = `the built-in "Storage Blob Data Reader" role (2a2b9908-6ea1-4ae2-8e65-a410df84e7d1) scoped to the export container (.../blobServices/default/containers/<container>)`

// credentialChain names the six-credential ambient chain in error
// messages (decision D24 analog).
const credentialChain = "environment variables (AZURE_TENANT_ID + AZURE_CLIENT_ID + AZURE_CLIENT_SECRET or certificate), " +
	"workload identity, managed identity, Azure CLI (`az login`), Azure Developer CLI (`azd auth login`), " +
	"or Azure PowerShell (Connect-AzAccount); AZURE_TOKEN_CREDENTIALS=prod|dev|<CredentialName> pins or trims the chain"

// AttributionStore is the slice of the storage interface discovery uses
// for the manifest-attribution cache (satisfied by storage.Store).
type AttributionStore interface {
	ManifestAttributions(ctx context.Context, connector string) (map[string]storage.ManifestAttribution, error)
	UpsertManifestAttribution(ctx context.Context, attr storage.ManifestAttribution) error
}

// ManifestState is the change-detection tuple of a billing period's
// CURRENT manifest as returned by the flat listing — no Get Blob
// involved. Azure ETags are opaque change tokens (not content digests),
// so the ETag, LastModified, and size together decide "unchanged".
type ManifestState struct {
	Key          string
	ETag         string
	LastModified time.Time
	Size         int64
}

// Equal reports whether two tuples match; used for the incremental-sync
// skip decision.
func (m ManifestState) Equal(o ManifestState) bool {
	return m.Key == o.Key && m.ETag == o.ETag && m.LastModified.Equal(o.LastModified) && m.Size == o.Size
}

// Period is one discovered billing period. A period whose stored sync
// tuple matched its current manifest is SKIPPED: it carries no Connector
// and cost zero Get Blob calls. A period whose current manifest is
// anomalous carries the failure in Err — reported per period, never
// aborting the other periods.
type Period struct {
	// Billing is the billing period, "YYYY-MM".
	Billing string
	// Manifest is the listed tuple of the period's CURRENT manifest
	// (greatest runInfo.submittedTime). The caller persists it after
	// every successful sync outcome so the next run can skip the period.
	Manifest ManifestState
	// Conn reads the period's current data files; nil when the period
	// was skipped or failed discovery.
	Conn *Connector
	// Err is the period's discovery failure; see Discover.
	Err error
}

// Skipped reports whether discovery skipped the period because its
// stored sync tuple matched the current manifest's listing tuple.
func (p Period) Skipped() bool { return p.Conn == nil && p.Err == nil }

// manifestSuffix matches export manifests in the flat listing. The docs
// are ambiguous between "manifest.json" and "_manifest.json", so the
// match is by suffix, which covers both.
const manifestSuffix = "manifest.json"

// Discover authenticates via the ambient Azure credential chain (D24
// analog), performs ONE flat listing of the export under
// <account-url>/<container>/<prefix>, attributes every listed manifest —
// from the persistent attribution cache when its tuple is unchanged,
// fetching only never-seen manifest blobs — and returns one Period per
// billing period, oldest first. prior holds the stored sync tuples keyed
// by source identity; a period whose CURRENT manifest tuple equals the
// stored one is skipped without any Get Blob call. Pass nil to process
// every period (--force). Per-period anomalies (unsupported format,
// byteCount mismatch, missing blob, a cached manifest whose body
// re-fetch fails, several exports sharing the prefix) land in
// Period.Err; only source-level failures (credentials, listing, an
// unattributable manifest — one never seen before whose fetch or parse
// fails, leaving no billing period to pin the failure to) abort
// discovery itself.
func Discover(ctx context.Context, accountURL, containerName, prefix string, prior map[string]ManifestState, attrs AttributionStore) ([]Period, error) {
	if accountURL == "" || containerName == "" || prefix == "" {
		return nil, errors.New("account URL, container, and prefix must not be empty")
	}
	if strings.ContainsAny(containerName, "?#") || strings.ContainsAny(prefix, "?#") {
		// Deliberately not echoed: a query string pasted here is exactly
		// where a SAS token would live, and these values reach error
		// messages and the source identity verbatim.
		return nil, errors.New("--container and --prefix must not carry query parameters — the azure-focus " +
			"connector authenticates only via the ambient credential chain and accepts no SAS tokens or " +
			"account keys (decisions D17, D24)")
	}
	prefix = strings.Trim(prefix, "/")
	root, cc, err := newContainerClient(accountURL, containerName)
	if err != nil {
		return nil, err
	}
	return discover(ctx, cc, root, containerName, prefix, prior, attrs)
}

// newContainerClient parses and validates the account URL, builds the
// service client via the ambient credential chain (or the documented
// test-only no-auth escape), and returns the identity root
// ("<host>[/<account-path>]") plus the container client.
func newContainerClient(accountURL, containerName string) (string, *container.Client, error) {
	u, err := url.Parse(accountURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		// The value is NEVER echoed here: an Azure connection string parses
		// as a scheme-less URL whose whole body — AccountKey included — is
		// one path segment, so no scrub can make echoing it safe (D17).
		return "", nil, errors.New("invalid --account-url: expected the storage account's blob endpoint, " +
			"e.g. https://<account>.blob.core.windows.net/ (the value is not echoed — a connection string's " +
			"AccountKey would survive here)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		// Never echo the value: userinfo (a user:key@ authority), a query
		// string (where a SAS token lives), and a fragment are all
		// credential-shaped, and this connector accepts none of them.
		return "", nil, errors.New("--account-url must not carry userinfo, query parameters, or a fragment — the " +
			"azure-focus connector authenticates only via the ambient credential chain and accepts no SAS tokens " +
			"or account keys (decisions D17, D24)")
	}
	serviceURL := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
	identityRoot := u.Host + strings.TrimSuffix(u.Path, "/")

	if os.Getenv(InsecureNoAuthEnv) == "1" {
		if u.Scheme != "http" {
			return "", nil, fmt.Errorf("%s=1 is a test-only escape for http:// endpoints (the fakeblob dev tool) "+
				"and is refused for %s URLs — unset it to authenticate via the ambient credential chain", InsecureNoAuthEnv, u.Scheme)
		}
		client, err := azblob.NewClientWithNoCredential(serviceURL, nil)
		if err != nil {
			return "", nil, fmt.Errorf("building unauthenticated blob client: %w", err)
		}
		return identityRoot, client.ServiceClient().NewContainerClient(containerName), nil
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", nil, credentialError(err)
	}
	client, err := azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		return "", nil, fmt.Errorf("building blob client: %w", err)
	}
	return identityRoot, client.ServiceClient().NewContainerClient(containerName), nil
}

// blobInfo is the per-blob metadata every listing entry carries — the
// sync tuple source, obtained with zero downloads.
type blobInfo struct {
	etag         string
	lastModified time.Time
	size         int64
}

func discover(ctx context.Context, cc *container.Client, root, containerName, prefix string, prior map[string]ManifestState, attrs AttributionStore) ([]Period, error) {
	exportRoot := root + "/" + containerName + "/" + prefix
	// blobURI renders a container-relative listing key for error
	// messages; listing keys already start with the export prefix.
	blobURI := func(key string) string { return root + "/" + containerName + "/" + key }

	listed, err := listAll(ctx, cc, prefix+"/")
	if err != nil {
		return nil, classify(err, "listing "+exportRoot+"/")
	}

	var manifestKeys []string
	for key := range listed {
		if strings.HasSuffix(key, manifestSuffix) {
			manifestKeys = append(manifestKeys, key)
		}
	}
	if len(manifestKeys) == 0 {
		return nil, fmt.Errorf("no export manifests found under %s/ (%d blob(s) listed) — expected an Azure Cost "+
			"Management FOCUS export delivery, where every run writes its data files plus a manifest.json; point "+
			"--prefix at the export root (the export's configured storage directory plus the export name) and "+
			"check that the export has completed at least one run", exportRoot, len(listed))
	}
	sort.Strings(manifestKeys)

	cached, err := attrs.ManifestAttributions(ctx, Name)
	if err != nil {
		return nil, err
	}

	// Attribute every listed manifest: from the cache when the listing
	// tuple matches (manifest blobs are immutable, so the attribution is
	// permanent), fetching only never-seen or changed manifest blobs.
	// Fetched bodies are kept for the connectors built below.
	attributions := map[string]storage.ManifestAttribution{} // listing key → attribution
	bodies := map[string]*manifest{}                         // listing key → fetched body
	for _, key := range manifestKeys {
		info := listed[key]
		cacheKey := blobURI(key)
		if a, ok := cached[cacheKey]; ok && a.ETag == info.etag && a.LastModified.Equal(info.lastModified) && a.Size == info.size {
			attributions[key] = a
			continue
		}
		m, err := fetchManifest(ctx, cc, blobURI(key), key, info.etag)
		if err != nil {
			// Unattributable: without a parsed body the manifest cannot
			// be assigned to a period, so this cannot degrade per period.
			return nil, err
		}
		submitted, err := parseManifestTime(m.RunInfo.SubmittedTime)
		if err != nil {
			return nil, fmt.Errorf("malformed manifest %s: runInfo.submittedTime: %w", blobURI(key), err)
		}
		start, err := parseManifestTime(m.RunInfo.StartDate)
		if err != nil {
			return nil, fmt.Errorf("malformed manifest %s: runInfo.startDate: %w", blobURI(key), err)
		}
		a := storage.ManifestAttribution{
			Connector:     Name,
			ManifestKey:   cacheKey,
			ETag:          info.etag,
			LastModified:  info.lastModified,
			Size:          info.size,
			BillingPeriod: start.Format("2006-01"),
			SubmittedTime: submitted,
			ExportName:    m.ExportConfig.ExportName,
		}
		if err := attrs.UpsertManifestAttribution(ctx, a); err != nil {
			return nil, err
		}
		attributions[key] = a
		bodies[key] = m
	}

	// Two different exports delivering under one prefix would share a
	// period's source identity and silently replace each other's data on
	// alternating syncs. Refuse such periods with an actionable error —
	// the attribution cache carries each manifest's export name, so this
	// check costs no fetches.
	exportsByPeriod := map[string]map[string]bool{}
	for _, key := range manifestKeys {
		a := attributions[key]
		if exportsByPeriod[a.BillingPeriod] == nil {
			exportsByPeriod[a.BillingPeriod] = map[string]bool{}
		}
		exportsByPeriod[a.BillingPeriod][a.ExportName] = true
	}

	// Group listed manifests by billing period; manifestKeys is sorted, so
	// each period's slice stays sorted for the deterministic lexical pick.
	keysByPeriod := map[string][]string{}
	for _, key := range manifestKeys {
		keysByPeriod[attributions[key].BillingPeriod] = append(keysByPeriod[attributions[key].BillingPeriod], key)
	}

	// A period's current run = the attributed manifest with the greatest
	// submittedTime. A submittedTime TIE between distinct manifests is
	// resolved by change token: identical tokens (e.g. a
	// manifest.json/_manifest.json pair for one run) describe the same data,
	// so the deterministic lexical pick is safe; DIFFERING tokens make
	// "which run is current" genuinely ambiguous, so the period degrades to
	// an actionable error naming the tied manifests rather than being
	// silently tie-broken.
	currentByPeriod := map[string]string{} // billing period → listing key
	tieErrByPeriod := map[string]error{}   // billing period → unresolved-tie error
	periods := make([]string, 0, len(keysByPeriod))
	for p, keys := range keysByPeriod {
		periods = append(periods, p)
		current := keys[0]
		for _, key := range keys[1:] {
			if a, cur := attributions[key], attributions[current]; a.SubmittedTime.After(cur.SubmittedTime) ||
				(a.SubmittedTime.Equal(cur.SubmittedTime) && key > current) {
				current = key
			}
		}
		currentByPeriod[p] = current
		var tied []string
		for _, key := range keys {
			if attributions[key].SubmittedTime.Equal(attributions[current].SubmittedTime) {
				tied = append(tied, key)
			}
		}
		if len(tied) > 1 {
			if err := resolveTie(ctx, cc, p, tied, listed, bodies, blobURI); err != nil {
				tieErrByPeriod[p] = err
			}
		}
	}
	sort.Strings(periods)

	out := make([]Period, 0, len(periods))
	for _, p := range periods {
		names := exportsByPeriod[p]
		if names[""] {
			// An empty exportConfig.exportName is unattributable: it cannot be
			// distinguished from a co-tenant export delivering under the same
			// prefix, so the shared-prefix refusal below could not protect
			// this period. Refuse it rather than conflate distinct exports as
			// {""} (which would defeat the len(names) > 1 check).
			out = append(out, Period{Billing: p, Err: fmt.Errorf(
				"billing period %s has a manifest with no exportConfig.exportName under %s/ — Costroid cannot "+
					"confirm a single export owns this prefix; point --prefix at ONE export's root (its storage "+
					"directory plus the export name)", p, exportRoot)})
			continue
		}
		if len(names) > 1 {
			sorted := make([]string, 0, len(names))
			for n := range names {
				sorted = append(sorted, n)
			}
			sort.Strings(sorted)
			out = append(out, Period{Billing: p, Err: fmt.Errorf(
				"billing period %s has manifests from %d different exports (%s) under %s/ — exports sharing "+
					"a prefix would silently replace each other's data; point --prefix at ONE export's root "+
					"(its storage directory plus the export name)",
				p, len(sorted), strings.Join(sorted, ", "), exportRoot)})
			continue
		}
		if err := tieErrByPeriod[p]; err != nil {
			out = append(out, Period{Billing: p, Err: err})
			continue
		}
		key := currentByPeriod[p]
		info := listed[key]
		state := ManifestState{Key: key, ETag: info.etag, LastModified: info.lastModified, Size: info.size}

		// The incremental-sync skip (decision D16): the current
		// manifest's tuple is unchanged, and no newly-fetched manifest
		// superseded it (a later run would BE the current manifest and
		// carry a different tuple). Zero Get Blob calls were spent.
		if stored, ok := prior[sourceIdentity(root, containerName, prefix, p)]; ok && stored.Equal(state) {
			out = append(out, Period{Billing: p, Manifest: state})
			continue
		}

		m, ok := bodies[key]
		if !ok {
			// Current manifest was attributed from the cache but the
			// period is not skipped (fresh store, --force, tenant
			// switch): fetch its body now — the one case a cached
			// attribution still needs the manifest content. The period
			// is known here, so a failure poisons this period only.
			m, err = fetchManifest(ctx, cc, blobURI(key), key, info.etag)
			if err != nil {
				out = append(out, Period{Billing: p, Manifest: state, Err: err})
				continue
			}
		}
		conn, err := newConnector(cc, root, containerName, prefix, p, key, m, listed)
		if err != nil {
			out = append(out, Period{Billing: p, Manifest: state, Err: err})
			continue
		}
		out = append(out, Period{Billing: p, Manifest: state, Conn: conn})
	}
	return out, nil
}

// resolveTie decides a submittedTime tie between the manifests in tied
// (all sharing a period's winning submittedTime). It returns nil when they
// describe identical data — same documented change token
// (blobName/byteCount/dataRowCount + dataVersion), e.g. a
// manifest.json/_manifest.json pair for one run — so the caller's
// deterministic lexical pick is safe. Manifests with DIFFERING tokens are a
// genuine ambiguity, so it returns an actionable per-period error naming
// them. Bodies are fetched only for tied manifests not already fetched — an
// anomaly path, so the extra Get Blob calls never touch the normal
// zero-fetch flow; a fetch failure degrades the (known) period, never
// aborting discovery.
func resolveTie(ctx context.Context, cc *container.Client, period string, tied []string,
	listed map[string]blobInfo, bodies map[string]*manifest, blobURI func(string) string) error {
	tokens := map[string]bool{}
	for _, key := range tied {
		m, ok := bodies[key]
		if !ok {
			var err error
			m, err = fetchManifest(ctx, cc, blobURI(key), key, listed[key].etag)
			if err != nil {
				return err
			}
			bodies[key] = m
		}
		tokens[contentHash(m)] = true
	}
	if len(tokens) == 1 {
		return nil
	}
	uris := make([]string, 0, len(tied))
	for _, key := range tied {
		uris = append(uris, blobURI(key))
	}
	sort.Strings(uris)
	return fmt.Errorf("billing period %s has %d manifests with the same runInfo.submittedTime but different "+
		"contents (%s) — which export run is current is ambiguous; re-run ingest once the delivery settles, or "+
		"remove the superseded manifest", period, len(uris), strings.Join(uris, ", "))
}

// sourceIdentity builds the replace-key identity of one billing period;
// Connector.SourceIdentity returns the same value.
func sourceIdentity(root, containerName, prefix, period string) string {
	return root + "/" + containerName + "/" + prefix + "/" + period
}

// manifest is the subset of the export manifest.json the connector
// reads. Everything about a run is read from here — never from folder
// names.
type manifest struct {
	Blobs        []manifestBlob `json:"blobs"`
	ExportConfig struct {
		ExportName  string `json:"exportName"`
		DataVersion string `json:"dataVersion"`
		Type        string `json:"type"`
	} `json:"exportConfig"`
	DeliveryConfig struct {
		FileFormat            string `json:"fileFormat"`
		CompressionMode       string `json:"compressionMode"`
		DataOverwriteBehavior string `json:"dataOverwriteBehavior"`
	} `json:"deliveryConfig"`
	RunInfo struct {
		SubmittedTime string `json:"submittedTime"`
		RunID         string `json:"runId"`
		StartDate     string `json:"startDate"`
		EndDate       string `json:"endDate"`
	} `json:"runInfo"`
}

type manifestBlob struct {
	BlobName     string `json:"blobName"`
	ByteCount    int64  `json:"byteCount"`
	DataRowCount int64  `json:"dataRowCount"`
}

// fetchManifest downloads and parses one manifest blob, If-Match pinned
// to its listed ETag so a run replacing it mid-sync fails actionably.
// uri names the blob in error messages.
func fetchManifest(ctx context.Context, cc *container.Client, uri, key, etag string) (*manifest, error) {
	resp, err := cc.NewBlobClient(key).DownloadStream(ctx, &blob.DownloadStreamOptions{
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfMatch: to.Ptr(azcore.ETag(etag))},
		},
	})
	if err != nil {
		return nil, classify(err, "fetching manifest "+uri)
	}
	// Read and close through the retry reader (which owns resp.Body per
	// the azblob contract), so a mid-read retry's replacement body is
	// released too.
	rr := resp.NewRetryReader(ctx, nil)
	body, err := io.ReadAll(rr)
	closeErr := rr.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, classify(err, "reading manifest "+uri)
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("malformed manifest %s: %w", uri, err)
	}
	return &m, nil
}

// parseManifestTime parses the manifest's timestamp wire forms
// leniently: Microsoft's own published samples mix timezone-less values
// ("2025-03-01T00:00:00"), Z-suffixed values with seven fractional
// digits ("2025-06-05T09:19:01.9013967Z"), and endDate appears both
// with and without the Z. Timezone-less values are UTC.
func parseManifestTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("timestamp is empty")
	}
	for _, layout := range []string{
		time.RFC3339,                    // zoned, optional fractional seconds
		"2006-01-02T15:04:05.999999999", // timezone-less, optional fractional seconds
	} {
		if t, err := time.Parse(layout, s); err == nil {
			// Truncate to microseconds at parse time: the attribution cache
			// round-trips submittedTime through a DuckDB TIMESTAMP (µs
			// precision), so a fresh parse keeping Azure's 100 ns digits
			// would compare unequal to its own cached copy and could flip a
			// period's current-run selection between syncs. Truncating here
			// makes the fresh and cached values identical by construction.
			return t.UTC().Truncate(time.Microsecond), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a recognized manifest timestamp", s)
}

// dataFile is one partition of a billing period's current run.
type dataFile struct {
	key  string
	etag string
}

// newConnector validates one period's current manifest against the
// listing and builds its pipeline connector. Failures here are
// per-period: they poison this period only.
func newConnector(cc *container.Client, root, containerName, prefix, period, manifestKey string, m *manifest, listed map[string]blobInfo) (*Connector, error) {
	manifestURI := root + "/" + containerName + "/" + manifestKey
	if v := m.ExportConfig.DataVersion; !strings.HasPrefix(v, "1.2") {
		return nil, fmt.Errorf("unsupported dataset version %q in manifest %s — the azure-focus connector reads "+
			"FOCUS 1.2-preview exports; recreate the export with the FOCUS 1.2-preview dataset (Azure's 1.0/1.0r2 "+
			"datasets have registry slots but no transform yet)", v, manifestURI)
	}
	if f := m.DeliveryConfig.FileFormat; f != "" && !strings.EqualFold(f, "csv") {
		return nil, fmt.Errorf("unsupported export format %q in manifest %s — this slice reads gzipped-CSV FOCUS "+
			"exports only; configure the export with file format CSV and compression gzip", f, manifestURI)
	}
	if c := m.DeliveryConfig.CompressionMode; c != "" && !strings.EqualFold(c, "gzip") {
		return nil, fmt.Errorf("unsupported compression mode %q in manifest %s — this slice reads gzipped-CSV FOCUS "+
			"exports only; configure the export with file format CSV and compression gzip", c, manifestURI)
	}
	if len(m.Blobs) == 0 {
		return nil, fmt.Errorf("malformed manifest %s: it lists no data blobs (blobs is empty or missing)", manifestURI)
	}

	files := make([]dataFile, 0, len(m.Blobs))
	for _, b := range m.Blobs {
		// blobName is container-relative in the published samples, so the
		// verbatim name is tried FIRST — an in-container directory whose
		// first segment happens to equal the container name must not be
		// mangled. Container-prefixed and prefix-relative forms are
		// tolerated as fallbacks.
		raw := strings.TrimPrefix(b.BlobName, "/")
		var key string
		var info blobInfo
		ok := false
		for _, candidate := range []string{raw, strings.TrimPrefix(raw, containerName+"/"), prefix + "/" + raw} {
			if i, found := listed[candidate]; found {
				key, info, ok = candidate, i, true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("manifest %s lists %q but the blob is not in the listing — the export run was "+
				"replaced mid-read; re-run ingest", manifestURI, b.BlobName)
		}
		if !strings.HasSuffix(key, ".csv.gz") {
			return nil, fmt.Errorf("unsupported export format: manifest %s lists %q — this slice reads gzipped-CSV "+
				"FOCUS exports only; configure the export with file format CSV and compression gzip", manifestURI, b.BlobName)
		}
		if info.size != b.ByteCount {
			return nil, fmt.Errorf("manifest %s says %q has %d bytes but the listing reports %d — the export run "+
				"was replaced mid-read; re-run ingest", manifestURI, b.BlobName, b.ByteCount, info.size)
		}
		files = append(files, dataFile{key: key, etag: info.etag})
	}

	return &Connector{
		container:   cc,
		root:        root,
		containerNm: containerName,
		prefix:      prefix,
		period:      period,
		files:       files,
		contentHash: contentHash(m),
	}, nil
}

// contentHash builds the documented change token: sha256 over the
// manifest's sorted "<blobName>\t<byteCount>\t<dataRowCount>" lines plus
// exportConfig.dataVersion. See the package documentation for why this
// is a change token rather than a content digest.
func contentHash(m *manifest) string {
	lines := make([]string, 0, len(m.Blobs))
	for _, b := range m.Blobs {
		lines = append(lines, fmt.Sprintf("%s\t%d\t%d", b.BlobName, b.ByteCount, b.DataRowCount))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		_, _ = fmt.Fprintln(h, l) // hash.Hash never errors
	}
	_, _ = fmt.Fprintln(h, m.ExportConfig.DataVersion)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// listAll pages through the flat listing and returns key → blobInfo.
func listAll(ctx context.Context, cc *container.Client, prefix string) (map[string]blobInfo, error) {
	out := map[string]blobInfo{}
	pager := cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: to.Ptr(prefix)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Segment.BlobItems {
			if item == nil || item.Name == nil || item.Properties == nil {
				continue
			}
			info := blobInfo{}
			if item.Properties.ETag != nil {
				info.etag = string(*item.Properties.ETag)
			}
			if item.Properties.LastModified != nil {
				info.lastModified = item.Properties.LastModified.UTC()
			}
			if item.Properties.ContentLength != nil {
				info.size = *item.Properties.ContentLength
			}
			out[*item.Name] = info
		}
	}
	return out, nil
}

// classify turns Azure SDK errors into short actionable messages. It
// never wraps *azcore.ResponseError verbatim: the raw error embeds the
// full request URL (query string included), and query strings must
// never reach an error or log (decision D17 — that is where a SAS token
// would live).
func classify(err error, action string) error {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		summary := fmt.Sprintf("Azure storage error %s (HTTP %d)", respErr.ErrorCode, respErr.StatusCode)
		switch {
		case bloberror.HasCode(err, bloberror.ConditionNotMet):
			return fmt.Errorf("stale blob while %s — the export run was replaced mid-read; re-run ingest and "+
				"discovery will pick up the new run: %s", action, summary)
		case bloberror.HasCode(err, bloberror.AuthorizationPermissionMismatch, bloberror.AuthorizationFailure,
			bloberror.InsufficientAccountPermissions, bloberror.AccountIsDisabled):
			return fmt.Errorf("access denied while %s — grant the identity %s: %s", action, dataReaderRole, summary)
		case bloberror.HasCode(err, bloberror.ContainerNotFound):
			return fmt.Errorf("container not found while %s — check the --container value: %s", action, summary)
		case bloberror.HasCode(err, bloberror.BlobNotFound):
			return fmt.Errorf("blob not found while %s — the export delivery may be in progress or was replaced; "+
				"re-run ingest once it completes: %s", action, summary)
		case respErr.StatusCode == 403:
			return fmt.Errorf("access denied while %s — grant the identity %s: %s", action, dataReaderRole, summary)
		}
		return fmt.Errorf("%s: %s", action, summary)
	}
	if isCredentialError(err) {
		return credentialError(err)
	}
	if strings.Contains(err.Error(), "authenticated requests are not permitted for non TLS protected") {
		return fmt.Errorf("%s: the account URL is http:// but bearer-token authentication requires https — use the "+
			"storage account's https endpoint (the %s escape exists only for the local fakeblob dev tool)", action, InsecureNoAuthEnv)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Transport errors embed the full request URL; strip its query.
		return fmt.Errorf("%s: %s %s: %v", action, urlErr.Op, scrubURL(urlErr.URL), urlErr.Err)
	}
	return fmt.Errorf("%s: %s", action, scrubURLs(err.Error()))
}

// isCredentialError recognizes an exhausted or failed ambient credential
// chain. azidentity's chain failure is an unexported credential-
// unavailable error, so the DefaultAzureCredential prefix is the
// documented way to recognize it alongside the exported type.
func isCredentialError(err error) bool {
	var authFailed *azidentity.AuthenticationFailedError
	if errors.As(err, &authFailed) {
		return true
	}
	return strings.Contains(err.Error(), "DefaultAzureCredential")
}

// credentialError renders the actionable chain-exhausted message. The
// underlying detail is included with every URL's query string stripped.
func credentialError(err error) error {
	return fmt.Errorf("no Azure credential produced a token — the azure-focus connector authenticates only via the "+
		"ambient credential chain and stores no credentials (decisions D17, D24): sign in or configure one of %s; "+
		"then grant that identity %s. Chain detail: %s", credentialChain, dataReaderRole, scrubURLs(err.Error()))
}

// urlInText finds absolute URLs inside error text so their query
// strings can be stripped before the text is surfaced.
var urlInText = regexp.MustCompile(`https?://[^\s"')]+`)

// scrubURLs strips the query string and fragment from every URL inside
// free-form error text.
func scrubURLs(s string) string {
	return urlInText.ReplaceAllStringFunc(s, scrubURL)
}

// scrubURL strips the query string, fragment, and userinfo from one URL
// — a SAS token lives in the query, and a user:password@ authority is
// credential-shaped too. On parse failure it truncates at the first '?'
// so nothing query-shaped survives.
func scrubURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		// Parse failed: cut at BOTH '?' and '#' so nothing query- or
		// fragment-shaped (a SAS token, an account key) survives.
		base, _, _ := strings.Cut(raw, "?")
		base, _, _ = strings.Cut(base, "#")
		return base
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Connector reads one billing period of one Azure FOCUS export in Blob
// Storage. Instances are produced by Discover, one per period.
type Connector struct {
	container   *container.Client
	root        string
	containerNm string
	prefix      string
	period      string
	files       []dataFile
	contentHash string
}

var _ ingest.Connector = (*Connector)(nil)

// Name implements ingest.Connector.
func (c *Connector) Name() string { return Name }

// FOCUSVersion implements ingest.Connector: the Azure FOCUS 1.2-preview
// dataset declares FOCUS 1.2 (with proprietary x_ columns, which the
// shared pipeline transform drops); discovery rejects other dataVersions.
func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_2 }

// BillingPeriod returns the connector's billing period ("YYYY-MM").
func (c *Connector) BillingPeriod() string { return c.period }

// SourceIdentity implements ingest.Connector. See the package
// documentation for why account, container, and prefix are part of the
// identity and the move-the-export trade-off that follows.
func (c *Connector) SourceIdentity() string {
	return sourceIdentity(c.root, c.containerNm, c.prefix, c.period)
}

// ContentHash implements ingest.Connector without touching the network:
// the documented change token computed from the period's current
// manifest at discovery (see the package documentation).
func (c *Connector) ContentHash(_ context.Context) (string, error) {
	return c.contentHash, nil
}

// Records implements ingest.Connector: the period's partitions are
// streamed in manifest order, each a complete gzipped CSV with its own
// header row, with row numbering continuing across partitions and the
// documented gap-fill rules applied to every row before the shared
// version transform.
func (c *Connector) Records(ctx context.Context) (ingest.RecordReader, error) {
	return &chunkReader{ctx: ctx, conn: c}, nil
}

// chunkReader streams a period's partitions sequentially, opening each
// via Get Blob on demand.
type chunkReader struct {
	// ctx is the Records call's context; the RecordReader interface has
	// no per-Next context, and the pipeline scopes one context to the
	// whole run.
	ctx    context.Context
	conn   *Connector
	next   int
	body   io.ReadCloser
	stream *awsfocus.GzipCSVStream
	rows   int
}

// Next implements ingest.RecordReader.
func (r *chunkReader) Next() (ingest.Row, error) {
	for {
		if r.stream == nil {
			if r.next >= len(r.conn.files) {
				return ingest.Row{}, io.EOF
			}
			if err := r.open(r.conn.files[r.next]); err != nil {
				return ingest.Row{}, err
			}
		}
		row, err := r.stream.Next()
		if errors.Is(err, io.EOF) {
			r.rows = r.stream.Rows()
			if err := r.closeChunk(); err != nil {
				return ingest.Row{}, classify(err, "closing partition "+r.uri(r.conn.files[r.next].key))
			}
			r.next++
			continue
		}
		if err != nil {
			// Mid-stream failures reach here too (the retry reader
			// re-issues Get Blob under the hood), so they go through the
			// same classification — never a raw SDK error dump.
			return ingest.Row{}, classify(err, "reading partition "+r.uri(r.conn.files[r.next].key))
		}
		// The connector-owned pre-transform normalization (see the
		// package documentation's gap-fill rule table).
		GapFill(row.Record)
		return row, nil
	}
}

func (r *chunkReader) uri(key string) string {
	return r.conn.root + "/" + r.conn.containerNm + "/" + key
}

func (r *chunkReader) open(f dataFile) error {
	// If-Match pins the partition to the ETag seen at discovery: a run
	// replacing it mid-read fails cleanly instead of mixing data
	// generations. The retry reader re-issues ranged reads with the same
	// pin on transient failures.
	resp, err := r.conn.container.NewBlobClient(f.key).DownloadStream(r.ctx, &blob.DownloadStreamOptions{
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfMatch: to.Ptr(azcore.ETag(f.etag))},
		},
	})
	if err != nil {
		return classify(err, "fetching partition "+r.uri(f.key))
	}
	body := resp.NewRetryReader(r.ctx, nil)
	stream, err := awsfocus.NewGzipCSVStream(body, r.rows)
	if err != nil {
		_ = body.Close()
		return classify(err, "reading partition "+r.uri(f.key))
	}
	r.body, r.stream = body, stream
	return nil
}

func (r *chunkReader) closeChunk() error {
	streamErr := r.stream.Close()
	bodyErr := r.body.Close()
	r.stream, r.body = nil, nil
	if streamErr != nil {
		return streamErr
	}
	return bodyErr
}

// Close implements ingest.RecordReader.
func (r *chunkReader) Close() error {
	if r.stream == nil {
		return nil
	}
	return r.closeChunk()
}

// GapFill applies the connector's documented pre-transform gap-fill
// rules (AZF-1 … AZF-5, see the package documentation) to one raw
// 1.2-preview row, in place. It feeds the UNCHANGED shared 1.2→1.4
// transform and never nulls a column the shared validation requires
// non-null.
func GapFill(rec focus.RawRecord) {
	// AZF-1 / AZF-2: ServiceName is empty on the documented EA
	// Marketplace and MCA row types. Deterministic fill order: EA
	// Marketplace purchases carry the seller in PublisherName; the MCA
	// cases carry Microsoft's suggested fallback in
	// x_SkuMeterSubcategory (their PublisherName is null on
	// reservation/savings-plan rows); any remaining empty ServiceName
	// with a PublisherName uses it as a last resort. Rows are never
	// dropped and never nulled — an unfillable row still fails the
	// shared ServiceName validation loudly.
	if rec["ServiceName"] == "" {
		// AZF-1 (PublisherName) is documented for EA Marketplace purchases
		// ONLY, so it is gated on an affirmative marketplace signal —
		// x_PublisherType == "Marketplace" when Azure emits that column.
		// Absent the signal a Purchase row prefers AZF-2's MCA fill
		// (x_SkuMeterSubcategory); only a still-empty ServiceName carrying a
		// PublisherName falls back to it as AZF-1b, the Costroid extension
		// (not documented Azure behavior). Rows are never dropped and never
		// nulled — an unfillable row still fails the shared ServiceName
		// validation loudly.
		switch {
		case rec["ChargeCategory"] == "Purchase" && rec["PublisherName"] != "" &&
			strings.EqualFold(rec["x_PublisherType"], "Marketplace"):
			rec["ServiceName"] = rec["PublisherName"] // AZF-1
		case rec["x_SkuMeterSubcategory"] != "":
			rec["ServiceName"] = rec["x_SkuMeterSubcategory"] // AZF-2
		case rec["PublisherName"] != "":
			rec["ServiceName"] = rec["PublisherName"] // AZF-1b (Costroid extension, last resort)
		}
	}

	// AZF-3 / AZF-4: a zero unit price delivered next to a zero
	// corresponding cost is Azure's documented "no price available"
	// placeholder → null (the columns are genuinely nullable in FOCUS
	// and decimal.NullDecimal in the model). The costs themselves are
	// mandatory non-null and stay exactly as delivered.
	nullZeroPlaceholder(rec, "ListUnitPrice", "ListCost")
	nullZeroPlaceholder(rec, "ContractedUnitPrice", "ContractedCost")

	// AZF-5: normalize period timestamps to RFC 3339 UTC — Azure FOCUS
	// exports have shipped with and without seconds and with and without
	// a timezone suffix across dataset versions. Owned here so
	// focus.ParseTime and the shared validation stay unchanged.
	for _, col := range []string{"BillingPeriodStart", "BillingPeriodEnd", "ChargePeriodStart", "ChargePeriodEnd"} {
		normalizeTimestamp(rec, col)
	}
}

// nullZeroPlaceholder implements AZF-3/AZF-4: null the unit price only
// when both it and its corresponding cost are exactly zero — the
// documented gap shape. A zero price beside a non-zero cost (or an
// unparseable value, which validation reports) is left as delivered.
func nullZeroPlaceholder(rec focus.RawRecord, priceCol, costCol string) {
	price := rec[priceCol]
	if price == "" {
		return
	}
	p, err := focus.ParseDecimal(price)
	if err != nil || !p.IsZero() {
		return
	}
	cost, err := focus.ParseDecimal(rec[costCol])
	if err != nil || !cost.IsZero() {
		return
	}
	delete(rec, priceCol)
}

// normalizeTimestamp implements AZF-5 for one column: values already in
// RFC 3339 pass through untouched; timezone-less and/or second-less
// forms are interpreted as UTC and rewritten in RFC 3339. Unrecognized
// forms stay as delivered for the shared validation to report.
func normalizeTimestamp(rec focus.RawRecord, col string) {
	v := strings.TrimSpace(rec[col])
	if v == "" {
		return
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999", // timezone-less, with seconds
		"2006-01-02T15:04Z07:00",        // zoned, without seconds
		"2006-01-02T15:04",              // timezone-less, without seconds
	} {
		if t, err := time.Parse(layout, v); err == nil {
			rec[col] = t.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			return
		}
	}
}

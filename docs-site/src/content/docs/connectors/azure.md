---
title: Azure
description: Incrementally ingest a gzipped Azure Cost Management FOCUS export from Blob Storage.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Configure `azure-focus`

The connector reads gzipped CSV FOCUS exports for Enterprise Agreement (EA) and
Microsoft Customer Agreement (MCA) billing from Azure Blob Storage. Supply the
plain storage endpoint, container, and export root; all three are required.

```sh
costroid ingest --connector azure-focus \
  --account-url https://<account>.blob.core.windows.net/ \
  --container <container> \
  --prefix <directory>/<export-name>
```

Add `--period YYYY-MM` to select one period, `--force` to re-process unchanged
periods, or `--tenant <name>` to select a tenant. See the
[connector overview](/connectors/) for shared replacement and single-writer
behavior.

## Plain account endpoint only

`--account-url` must be the plain Blob Storage endpoint. Costroid rejects a URL
with user information, query parameters, or a fragment, including any URL that
carries a SAS token or account key. Authentication is ambient only.

:::caution[Do not paste a SAS URL]
Use `https://<account>.blob.core.windows.net/` and configure an ambient identity.
Credential-bearing URLs are rejected before any listing begins.
:::

## Authentication

Costroid uses `DefaultAzureCredential`. Its ambient chain checks Environment,
Workload Identity, Managed Identity through IMDS, Azure CLI, Azure Developer
CLI, and Azure PowerShell, in that order. `AZURE_TOKEN_CREDENTIALS` can pin or
trim the chain.

Costroid stores no Azure credentials and accepts no credential flag for this
connector. It makes only the read-only List Blobs and Get Blob calls.

## Least-privilege role

Assign the built-in **Storage Blob Data Reader** role, GUID
`2a2b9908-6ea1-4ae2-8e65-a410df84e7d1`, at the export container rather than the
storage account:

```sh
az role assignment create \
  --assignee <principal-id> \
  --role "Storage Blob Data Reader" \
  --scope "/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Storage/storageAccounts/<account>/blobServices/default/containers/<container>"
```

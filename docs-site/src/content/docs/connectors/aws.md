---
title: AWS
description: Ingest AWS Data Exports FOCUS data from a local gzipped CSV or incrementally from S3.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Local file: `aws-focus`

Use `aws-focus` for an AWS Data Exports FOCUS file already on the Costroid host:

```sh
costroid ingest --connector aws-focus --path <your-focus-export.csv.gz>
```

The input must be a gzipped CSV. A plain `.csv` file is rejected with a
`reading gzip` error. This connector needs no credential, network access, or IAM
permission.

## Live incremental sync: `aws-focus-s3`

Point the live connector at the S3 bucket and export root. The prefix is the
configured export directory followed by the export name; both flags are
required.

```sh
costroid ingest --connector aws-focus-s3 \
  --bucket <bucket> \
  --prefix <prefix>/<export-name>
```

The AWS Data Export must use `text/csv` format with `gzip` compression. Only
gzipped-CSV exports are accepted. Add `--period YYYY-MM` to select one period,
`--force` to re-process unchanged periods, or `--tenant <name>` to select a
tenant. See the [connector overview](/connectors/) for shared replacement and
single-writer behavior.

## Authentication

`aws-focus-s3` uses only the AWS SDK default credential chain: environment
credentials, shared configuration or SSO profiles, and EC2 instance-profile
credentials through IMDS. Costroid stores no AWS credentials and accepts no
credential flag for either AWS connector.

Prefer an IAM role, SSO profile, or instance profile. The connector makes only
the read-only `ListObjectsV2` and `GetObject` calls.

## Least-privilege IAM policy

Grant listing on the bucket ARN, restricted to the export prefix, and reads on
the objects beneath that prefix:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ListExportPrefix",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::<bucket>",
      "Condition": {"StringLike": {"s3:prefix": "<prefix>/*"}}
    },
    {
      "Sid": "ReadExportObjects",
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::<bucket>/<prefix>/*"
    }
  ]
}
```

`s3:ListBucket` must attach to the bucket ARN with the `s3:prefix` condition. It
grants nothing when attached to an object ARN.

:::note[Troubleshooting access denied]
An access-denied error means the active credentials need `s3:ListBucket` on the
bucket ARN restricted to the export prefix, plus `s3:GetObject` on the export
objects.
:::

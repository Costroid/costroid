<!--
SPDX-License-Identifier: Apache-2.0
Copyright 2026 The Costroid Authors
-->

# Contributing to Costroid

Keep changes small, focused, and covered by tests. Before opening a pull
request, run `make generate`, `make lint`, `make test`, and `make build`; the
generated-code check must leave both `git diff --exit-code` and
`git status --porcelain` clean.

Costroid uses the Developer Certificate of Origin. Sign off every commit with
`git commit -s` to add a `Signed-off-by` trailer certifying that you have the
right to submit the change under the repository license. The DCO workflow checks
every pull-request commit.

Never submit secrets, customer billing data, or raw AI prompt or response
content. Report security issues privately as described in
[SECURITY.md](SECURITY.md).

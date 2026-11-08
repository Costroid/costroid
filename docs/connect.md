# Connect an owner to Costroid Platform

`costroid export` sends rows to Costroid Platform under an installation: the GitHub owner or the GitLab.com namespace the rows came through. The platform takes a batch only under an installation its tenant holds, and answers `409 INSTALLATION_NOT_BOUND` to any other. `costroid connect` binds the installation, with a proof that the CI system signs.

The rules it follows are the contract's: [`integration/v0.2`, Identity](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#identity).

## One token per owner

Create one integration token on Costroid Platform for each GitHub owner or GitLab group, with three scopes: `identity:read`, `installations:write` and `rows:write`. Keep it as an organization secret named `COSTROID_PLATFORM_TOKEN` on GitHub, or as a masked CI/CD variable of the group on GitLab. Every repository under the owner then connects and exports with the same token, and revoking it stops them all at once.

`costroid connect` uses the first two scopes and `costroid export` the first and the third. Both read the token from `COSTROID_PLATFORM_TOKEN`, never from an argument, and neither prints it.

## On GitHub Actions

`connect` binds `github-owner:` and the number in `GITHUB_REPOSITORY_OWNER_ID`, the owner's account id. GitHub states that this id is "different from the owner's name", so a rename does not change it. `connect` reads the tenant from `GET /identity`, asks GitHub for the job's OIDC token with the audience `https://app.costroid.com/tenants/<tenant id>`, and sends that token in the body of `PUT /installations/github-owner:<id>`.

The platform takes the proof from a `schedule` run, or from a `push` run on a protected ref. GitHub runs a scheduled workflow only from the default branch, so the tenant is named by a file that passed the owner's branch rules. A pull request, a `workflow_dispatch` or a push to an unprotected branch is refused with `INSTALLATION_PROOF_UNPROTECTED_RUN`.

Run it from a workflow of its own on the default branch, triggered on a daily schedule and on a push to that branch, with this permission and no other:

```yaml
permissions:
  id-token: write
```

The job installs the binary as the [README](../README.md#install) shows, then runs:

```sh
costroid connect --platform-url https://app.costroid.com
```

with `COSTROID_PLATFORM_TOKEN` set from the organization secret. When the default branch has no protection rule, the first binding waits for the first scheduled run.

## On GitLab CI

`connect` binds `gitlab-namespace:` and the number in `CI_PROJECT_NAMESPACE_ID`. GitLab fixes an ID token's audience in the pipeline file, so the job names its tenant there, and `connect` reads the token from `COSTROID_PLATFORM_PROOF`. With the [GitLab template](recipes.md) included, add this job:

```yaml
costroid:connect:
  extends: .costroid-install
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule"
  id_tokens:
    COSTROID_PLATFORM_PROOF:
      aud: https://app.costroid.com/tenants/<tenant id>
  script:
    - costroid connect --platform-url https://app.costroid.com
```

Then add a daily pipeline schedule on the default branch, and keep that branch protected. The platform takes a proof from a pipeline on a protected ref whose source is `push`, `web` or `schedule`.

`connect` binds a namespace only when `CI_SERVER_HOST` is `gitlab.com`. GitLab states that this variable is "the host of the GitLab instance URL, without protocol or port" ([predefined variables](https://docs.gitlab.com/ci/variables/predefined_variables/), read 2026-11-08). A self-managed instance numbers its own namespaces, so its ids are not unique, and the contract takes no proof it issues. There `connect` stops with one line and sends nothing.

When `COSTROID_PLATFORM_PROOF` is empty, `connect` stops and prints the audience to set, with the tenant's id in it.

## When it sends nothing

A binding by a job's proof is current for 7 days after its last proof. When `GET /identity` lists the installation active and proven less than 20 hours ago, `connect` prints that it is bound and sends no proof. The daily run renews the binding, and a push on the same day costs one request.

Outside GitHub Actions and GitLab CI no proof exists. There `connect` needs `--installation-id`, and only reads whether the installation is bound:

```sh
costroid connect --platform-url https://app.costroid.com --installation-id github-owner:7204411
```

## When another tenant holds the owner

One owner belongs to one tenant at a time. A valid proof against an owner that another tenant holds answers `409 INSTALLATION_BOUND_ELSEWHERE` while that tenant's binding is current. A binding that no job renews goes stale 7 days after its last proof, and a valid proof from another tenant then displaces it. A binding made through the GitHub App outranks a job's proof. The contract's table [Who holds an owner](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#who-holds-an-owner) lists every case.

## What connect prints

Each line starts with `costroid:`. A refusal names the platform's status and code, then one sentence, and never the platform's own message.

| Code | The sentence |
| ---- | ------------ |
| `INSTALLATION_PROOF_UNPROTECTED_RUN` | Run costroid connect from a scheduled job, or protect the default branch and run it on a push there. |
| `INSTALLATION_BOUND_ELSEWHERE` | Another tenant holds this owner, and this proof does not outrank its binding, as docs/connect.md explains. |
| `INSTALLATION_PROOF_AUDIENCE_MISMATCH` | The proof names another tenant than the integration token, so set the ID token's aud to `<audience>`. |
| `INSTALLATION_PROOF_EXPIRED` | The proof is past its window or older than the binding's last change, so run the job again for a new one. |
| `INSTALLATION_PROOF_SUBJECT_MISMATCH` | The proof names another owner than `<installation>`, so run costroid connect in a job of that owner. |
| `INSTALLATION_PROOF_CLAIMS_INVALID`, `INSTALLATION_PROOF_SIGNATURE_INVALID`, `INSTALLATION_PROOF_MISSING` | The proof is not the ID token the CI system issued, so pass the job's own token unchanged. |
| `INSTALLATION_PROOF_ISSUER_UNTRUSTED` | The platform trusts proofs from GitHub Actions and GitLab.com only, so a self-managed GitLab or GitHub Enterprise Server cannot bind. |
| `INSTALLATION_PROOF_KEYS_UNAVAILABLE` | The platform could not read the CI system's keys, so run the job again. |
| `AUTH_TOKEN_INVALID` | The integration token is unknown, expired or revoked, so create one on Costroid Platform. |
| `AUTH_TOKEN_SCOPE` | The integration token needs identity:read and installations:write. |

The platform answers `INSTALLATION_PROOF_KEYS_UNAVAILABLE` when it cannot read the CI system's keys, and the same proof can be sent again inside its window. `connect` sends it once more after the platform's `Retry-After`, which is 60 seconds today, and prints that it does. It waits at most 120 seconds. A `Retry-After` that is longer, absent or not a number of seconds ends the command with the sentence above, and so does a second refusal.

A code outside this table gets a sentence that points here, and the contract's [refusals](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#refusals) say what it means. [`docs/troubleshooting.md`](troubleshooting.md#connecting-to-costroid-platform) lists every other line.

| Exit code | Meaning |
| --------- | ------- |
| 0 | The installation is bound to the token's tenant |
| 1 | The platform refused, and the line names its code |
| 125 | `connect` could not read its input, get a proof or reach the platform |

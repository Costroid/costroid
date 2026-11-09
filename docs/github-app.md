# Register the GitHub App

`costroid serve` posts the approve button's checks as a GitHub App. This page registers that App with `costroid app create`, which puts the App's private key in an AWS KMS key and nowhere else.

GitHub generates every App's private key and accepts no key from outside. `app create` takes the key from GitHub once, imports it into KMS as a sign-only key, and zeroes its bytes. From then on KMS signs each JSON Web Token the App needs, with RS256, and the private key never leaves KMS. A token lives ten minutes and is used once, to mint an installation token that lives one hour.

## What it costs

A KMS key is billed monthly by AWS.

## What the key never touches

The App's private key reaches no file, no log, no terminal and no environment variable. `app create` reads GitHub's answer into one buffer, copies the key into another, converts it to the form KMS imports in a third, and zeroes all three after the import, whether it succeeded or not. It travels to KMS only wrapped, under an AES key that is itself encrypted to the wrapping key KMS issued for the import. `costroid serve` never holds it: it sends KMS a SHA-256 digest and gets back a signature.

The Go runtime and the TLS connection to GitHub keep transient copies that a program cannot zero. They end with the `app create` process, which writes nothing to disk.

## 1. Create the KMS key

Create a key with no key material, for signing, with the spec of a 2048-bit RSA key:

```sh
aws kms create-key --origin EXTERNAL --key-spec RSA_2048 --key-usage SIGN_VERIFY
```

Note its `KeyId`. GitHub's documentation does not state the size of the keys it issues (read 2026-11-09). `app create` expects 2048 bits; a key of another size is refused by KMS at the import, with `IncorrectKeyMaterialException`.

The key policy grants two principals:

| Principal                         | Actions                                               |
| --------------------------------- | ----------------------------------------------------- |
| The person who runs `app create`  | `kms:GetParametersForImport`, `kms:ImportKeyMaterial` |
| The role `costroid serve` runs as | `kms:Sign`                                            |

## 2. Run `app create`

Run it on a machine with a browser signed in to GitHub, with AWS credentials in `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and, for temporary credentials, `AWS_SESSION_TOKEN`. `AWS_REGION` names the key's region, unless `--kms-key` is the key's ARN.

```sh
costroid app create --name "Costroid for example" \
  --webhook-url https://costroid.example.com/webhooks/github \
  --kms-key 1234abcd-12ab-34cd-56ef-1234567890ab \
  --org example
```

`--webhook-url` is the https address of `costroid serve`'s `POST /webhooks/github`. `--org` registers the App for an organization you administer; without it, the App is your account's. The App is private to its owner.

`app create` then:

1. Asks KMS for the import's wrapping key and import token, for the `RSA_AES_KEY_WRAP_SHA_256` algorithm. A key KMS will not import into stops it here, before GitHub creates anything.
2. Prints an address on `127.0.0.1`. Open it. The page sends the App's manifest to GitHub, where you confirm the name and create the App.
3. Takes the code GitHub sends the browser back with, if it carries the state this run drew. GitHub's code lasts one hour.
4. Exchanges the code for the App at `POST /app-manifests/{code}/conversions`.
5. Wraps the private key and imports it into the KMS key, with no expiry.
6. Prints the App's id and slug, the KMS key, and the next two steps.

If the import fails after GitHub created the App, `app create` says so and names the App's settings page. No copy of the key was kept, so delete the App there and run `app create` again.

`costroid app manifest` prints the same manifest, for a review before you run `app create`.

## 3. Set the webhook secret

GitHub sets a webhook secret on the new App, and `app create` does not show it. Draw a new one of at least 32 bytes, for example with `openssl rand -hex 32`. Enter it on the App's settings page under the webhook, and give `costroid serve` the same value as `COSTROID_GITHUB_WEBHOOK_SECRET`. [`docs/serve.md`](serve.md#the-github-apps-webhook) describes the route.

## 4. Give `costroid serve` the App

Set these on `costroid serve`:

| Variable                                                          | Value                                                       |
| ----------------------------------------------------------------- | ----------------------------------------------------------- |
| `COSTROID_GITHUB_APP_ID`                                          | The App id `app create` printed                             |
| `COSTROID_GITHUB_APP_KMS_KEY`                                     | The KMS key's id or ARN                                     |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | Credentials of a principal the key policy grants `kms:Sign` |
| `AWS_REGION`                                                      | The key's region, unless the key is named by its ARN        |

Add `api.github.com` and the KMS endpoint, `kms.<region>.amazonaws.com`, to the egress allowlist with `--egress-allow`. `serve` logs each one the list misses at start.

With both App variables set, `serve` logs `webhook route served` with `approve_button` set to `posts as the GitHub App, signed by its KMS key`. With neither, a press of the button is refused and logged, and nothing is posted. With one alone, `serve` does not start.

## 5. Install the App

Install the App from its settings page, under Install App, on the repositories whose checks it should post. Each installation arrives at `POST /webhooks/github` and is kept.

## Permissions and events

The manifest asks for four repository permissions, the ones an installation token asks for and no more:

| Permission      | Access | Why                                                                                                                                               |
| --------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `checks`        | write  | Post the `costroid` check again when the approve button is pressed, and list a commit's check runs. A press reaches only an App with write access |
| `pull_requests` | write  | Read the pull request's head and author for an approval, and post Costroid's comment on a pull request                                            |
| `contents`      | read   | Read `costroid.yml` at the head of the base branch                                                                                                |
| `metadata`      | read   | Read whether the person who pressed the button can write to the repository                                                                        |

It subscribes to one event:

| Event       | Why                                                                                     |
| ----------- | --------------------------------------------------------------------------------------- |
| `check_run` | A press of the approve button arrives as `check_run` with the action `requested_action` |

The `installation` and `installation_repositories` events need no subscription and no permission: every App receives them.

## Sources

Read on 2026-11-09:

- AWS, [Sign](https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html): "If you provide a message digest, use the `DIGEST` value of `MessageType` to prevent the digest from being hashed again while signing." The signature, "When you use the HTTP API or the AWS CLI, the value is Base64-encoded."
- AWS, [Download the wrapping public key and import token](https://docs.aws.amazon.com/kms/latest/developerguide/importing-keys-get-public-key-and-token.html): "An `RSA_AES_KEY_WRAP_SHA_*` wrapping algorithm is required for wrapping RSA private key material, except in China Regions, where you must use the `SM2PKE` wrapping algorithm." "Each wrapping public key and import token set is valid for 24 hours."
- AWS, [Encrypt the key material](https://docs.aws.amazon.com/kms/latest/developerguide/importing-keys-encrypt-key-material.html): `RSA_AES_KEY_WRAP_SHA_256` encrypts the key material with a 256-bit AES key by `id-aes256-wrap-pad`, encrypts the AES key with the wrapping key by RSAES-OAEP with SHA-256, and concatenates the two, the encrypted AES key first.
- AWS, [Create a KMS key with imported key material](https://docs.aws.amazon.com/kms/latest/developerguide/importing-keys-conceptual.html): "Asymmetric key material must be BER-encoded or DER-encoded in Public-Key Cryptography Standards (PKCS) #8 format that complies with RFC 5208."
- AWS, [ImportKeyMaterial](https://docs.aws.amazon.com/kms/latest/APIReference/API_ImportKeyMaterial.html): "For asymmetric and HMAC keys, you cannot change the key material after the initial import."
- AWS, [Service-specific endpoints](https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html): `AWS_ENDPOINT_URL_<SERVICE>` overrides `AWS_ENDPOINT_URL` for one service. `costroid` reads `AWS_ENDPOINT_URL_KMS`, then `AWS_ENDPOINT_URL`.
- GitHub, [Registering a GitHub App from a manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest): "You must complete all three steps in the GitHub App Manifest flow within one hour." "GitHub creates a webhook secret for the app automatically."
- GitHub, [Managing private keys for GitHub Apps](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps): the key is "in `PKCS#1 RSAPrivateKey` format."
- GitHub, [Permissions required for GitHub Apps](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps): `POST /repos/{owner}/{repo}/check-runs` needs Checks write; `POST /repos/{owner}/{repo}/issues/{issue_number}/comments` and `GET /repos/{owner}/{repo}/pulls/{pull_number}` are under Pull requests; `GET /repos/{owner}/{repo}/collaborators/{username}/permission` under Metadata read; `GET /repos/{owner}/{repo}/contents/{path}` under Contents read.
- GitHub, [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads): of `installation` and `installation_repositories`, "All GitHub Apps receive this event by default. You cannot manually subscribe to this event." Of `requested_action`, "Only the GitHub App someone requests to perform an action will receive the requested_action payload."

GitHub's acceptance of a `127.0.0.1` address as the manifest's `redirect_url` is unverified until the first App is registered this way.

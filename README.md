# Terraform provider for Ignitify

Manage an existing Ignitify tenant as code with Terraform or OpenTofu.

Status: **v0.1, in development.** Not yet published to a registry. Install it
from a local build (below).

## What v0.1 manages

| Type | Name | Import id | Key needed |
| :-- | :-- | :-- | :-- |
| Resource | `ignitify_user` | the user name | owner key |
| Resource | `ignitify_group` | the group name | owner key |
| Resource | `ignitify_api_key` | the key's `resource_id` | owner key |
| Resource | `ignitify_secret` | the secret name | a key with `secrets.read` and `secrets.write` |
| Data source | `ignitify_whoami` | — | any key |

The provider never creates a tenant, a tier or a purchase, and never creates
an owner key. It takes an existing key.

## Requirements

- **Terraform 1.11 or later, or OpenTofu 1.11 or later.** `ignitify_secret`
  uses a write-only argument (`value_wo`), which older versions do not support.
- **An Ignitify API that serves `GET /keys/{id}` and `GET /secrets/{name}`.**
  The provider reads one key and one secret by these routes.
- To build: Docker only. Go, Terraform and OpenTofu run in pinned containers.

## Provider configuration

```hcl
provider "ignitify" {
  # api_key  = ...  # else the IGNITIFY_API_KEY environment variable
  # endpoint = ...  # else IGNITIFY_API_URL, else https://api.ignitify.dev
}
```

- Prefer the environment variable for the key; do not commit a key.
- The endpoint must be `https`. Plain `http` is accepted only to this machine
  (`localhost`, `127.0.0.0/8`, `::1`), so a key never crosses a network in clear.
- The provider does not read the Ignitify CLI's credentials file.

## Two root modules: least privilege

Users, groups and keys need an **owner key**. Secrets (and, later, tasks and
deployments) work with a **user key**. Keep them apart:

- `examples/access`: the owner applies it by hand, rarely. It makes a user, a
  group with permissions and members, and the user's API key.
- `examples/app`: applied by CI with the user key from `examples/access`.

With a user key, an access resource fails with "owner key required".

## Behaviour to know

- **State holds API keys.** `ignitify_api_key.raw_key` is shown once by the
  server and kept in the state as a sensitive value, so you can pass it to the
  next resource. Use an encrypted, access-controlled state backend. After an
  import, `raw_key` is empty.
- **Secret values never reach the plan or the state.** Terraform cannot see a
  change of `value_wo`: raise `value_wo_version` to send the value again.
  `updated_at` shows a write made outside Terraform. After an import the state
  has no `value_wo_version`: if the configuration sets one, the first apply
  sends the configured value once; if it sets none, nothing is sent.
- **A secret create refuses an existing name** (the API write would overwrite
  it). Import the secret to manage it.
- **Deleting a user revokes all its keys** and takes it out of every group.
  The next plan shows those keys as gone.
- **Changing a user name, a group name, a key's user or label, or a secret name
  replaces the object.** A replaced key has a new raw key.
- **`ignitify_group` owns its permissions and members.** A grant or a member
  added outside Terraform shows as drift. `members` defaults to none.
- **Retries.** Every write carries an `Idempotency-Key`, so a retry after a lost
  answer never acts twice. The provider retries network errors, a `429` or
  `503` with `Retry-After`, and the API rate limit (`429` with no reason), a few
  times with a capped wait. A quota refusal (such as the key or secret cap)
  fails at once. A redirect is never followed.
- **Only the API's own "not found" answer removes an object from the state.**
  Any other `404` (a wrong endpoint, a proxy page) is an error. If a key create is
  answered without the raw key (a replay), the provider revokes that key and
  creates another.

## Build and install locally

```sh
make build          # bin/terraform-provider-ignitify for this machine
```

Then point Terraform or OpenTofu at the build with `dev_overrides` in
`~/.terraformrc` (Terraform) or `~/.tofurc` (OpenTofu):

```hcl
provider_installation {
  dev_overrides {
    "ignitify-dev/ignitify" = "/absolute/path/to/terraform-provider-ignitify/bin"
  }
  direct {}
}
```

With `dev_overrides`, skip `terraform init` for this provider; `plan` and
`apply` print a warning that the override is active.

To install it like a released provider instead, use a local filesystem mirror:
copy the binary to
`<mirror>/registry.terraform.io/ignitify-dev/ignitify/0.1.0/<os>_<arch>/terraform-provider-ignitify_v0.1.0`
and set `filesystem_mirror { path = "<mirror>" }` in the same file.

## Development

| Command | What it does |
| :-- | :-- |
| `make build` | Build the provider binary for this machine into `bin/`. |
| `make test` | Unit tests against an in-memory fake API. No network, no Terraform binary. |
| `make test-tofu` | Real OpenTofu plans and applies against the in-memory fake API (create, empty plan after apply, replace, drift, a 404 removing an object). No network. |
| `make lint` | `go vet` and a `gofmt` check. |
| `make tidy` | `go mod tidy`. |
| `make testacc` | Acceptance tests with OpenTofu against a real tenant. Needs `IGNITIFY_API_URL` and `IGNITIFY_API_KEY` (an owner key). They create objects with random names and destroy them. |

The Go and OpenTofu images and every Go module are pinned to exact versions.

## Licence

Mozilla Public License 2.0, see `LICENSE`. "Ignitify" is a trademark of its
owner; the licence grants no right to use it (MPL-2.0 §2.3).

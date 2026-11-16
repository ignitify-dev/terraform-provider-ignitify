# Terraform provider for Ignitify

Manage an existing Ignitify tenant as code with Terraform or OpenTofu.

Status: **v0.2, in development.** Not yet published to a registry. Install it
from a local build (below).

## What v0.2 manages

| Type | Name | Import id | Key needed |
| :-- | :-- | :-- | :-- |
| Resource | `ignitify_user` | the user name | owner key |
| Resource | `ignitify_group` | the group name | owner key |
| Resource | `ignitify_api_key` | the key's `resource_id` | owner key |
| Resource | `ignitify_secret` | the secret name | a key with `secrets.read` and `secrets.write` |
| Resource | `ignitify_task` | the task slug | a key with `tasks.read` and `tasks.write` |
| Resource | `ignitify_deployment` | the task slug, or `<task slug>/<deployment id>` | a key with `tasks.read` and `deployments.*` (`hooks.write` for notify) |
| Resource | `ignitify_hook` | `<task slug>/<hook resource_id>` | a key with `hooks.read` and `hooks.write` |
| Data source | `ignitify_whoami` | — | any key |

The provider never creates a tenant, a tier or a purchase, and never creates
an owner key. It takes an existing key.

## Requirements

- **Terraform 1.11 or later, or OpenTofu 1.11 or later.** `ignitify_secret`
  uses a write-only argument (`value_wo`), which older versions do not support.
- **An Ignitify API that serves `GET /keys/{id}`, `GET /secrets/{name}`,
  `GET /tasks/{id}`, `GET /hooks/{id}` and task hooks (`/tasks/{id}/hooks`),
  and that reads the task config from the uploaded archive.
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

Users, groups and keys need an **owner key**. Secrets, tasks, deployments and
hooks work with a **user key**. Keep them apart:

- `examples/access`: the owner applies it by hand, rarely. It makes a user, a
  group with permissions and members, and the user's API key.
- `examples/app`: applied by CI with the user key from `examples/access`: a
  task, its deployment, a hook and a secret.

A user key with `deployments.write` can read secret values through the code it
deploys: grant it only to who may read them.

With a user key, an access resource fails with "owner key required".

## Behaviour to know

- **State holds API keys and hook secrets.** `ignitify_api_key.raw_key` is shown once by the
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
  fails at once, and so does a rate-window refusal that asks for a wait over
  30 seconds (an hour or a day window); the error states the wait. A redirect is never followed.
- **Tasks.** A destroy deletes the task with its deployments, runs and hooks.
  Creating the same slug again brings back the same task (same `task_id`),
  without them.
- **Deployments.** `source_dir` is a task folder. At plan time the provider
  selects its files as the CLI does (no `.env` files, `node_modules`, `.git`,
  virtualenvs, tool caches, root `target` or `dist`; not the paths
  `.ignitifyignore` leaves out), refuses a symlink (also as the task config or
  `.ignitifyignore`), a registry credential in
  `.npmrc` or `.yarnrc.yml`, a path the server refuses (a backslash, a tab or
  a newline in a name), a missing lockfile or entry file, and a folder above
  the upload size; it warns about excluded files and private-key names, and
  when it cannot read the runtime from the task config (those checks are then
  left to the server). It computes `source_hash`. A change of the files
  shows as an in-place update with an unknown `deployment_id`. The apply
  uploads, waits for the build (`timeouts`, 30 minutes by default), and, once
  the new deployment is READY, deletes the old one: that ends its running runs
  and deletes its run history. An unchanged task config and code reuse the
  same deployment. A failed or timed-out build keeps the old deployment (and
  its `source_hash`, so the next plan uploads again); the provider reports the
  build's failure reason and deletes the deployment that did not become ready
  (if that delete fails, the error names it for `ignitify deployment:delete`).
  A failed first build is kept as tainted, so the next apply deletes it.
- **If the old deployment cannot be deleted** after an update, the new one is
  live and in the state; the error names the old one. Delete it with
  `ignitify deployment:delete <deployment id>` (list a task's deployments with
  `ignitify deployment:list`).
- **Do not use `create_before_destroy` on `ignitify_deployment`.** A
  replacement with the same code gets the same deployment back from the
  server, and the destroy step would then delete the live deployment. An update
  needs one free deployment slot. A deployment that is not READY (for example
  archived) is uploaded again at the next apply.
- **Notify.** `notify_url` (https) with the write-only `notify_secret_wo` (at
  least 32 characters) sets the URL the deployment calls when a run ends; every
  new deployment gets it again. Raise `notify_secret_wo_version` to send a new
  secret. Unset `notify_url` clears it. The plan refuses `notify_url` when the
  tier has no webhooks. If the server refuses the notify target when a
  deployment is created, the deployment is kept, the apply warns, and the next
  plan shows `notify_url` as a change and sets it again.
- **Deployment import.** The API has no "current deployment" of a task, so the
  task slug imports only a task with exactly one live deployment (what an apply
  leaves); otherwise use `<task slug>/<deployment id>`. After an import the
  state has no `source_dir`: the first apply uploads the folder (the same code
  reuses the same deployment).
- **Hooks** belong to the task: a code update keeps the URL and the secret.
  `secret` is shown once by the server and kept in the state as a sensitive
  value; it is empty after an import. Replace the hook
  (`-replace=ignitify_hook.<name>`) to rotate the URL and the secret. A hook
  create answered without its secret (a replay) is deleted and created again.
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
| `make test-terraform` | The same plans and applies, run by a pinned Terraform binary (a test tool only; it is not shipped). No network. |
| `make lint` | `go vet` and a `gofmt` check. |
| `make tidy` | `go mod tidy`. |
| `make testacc` | Acceptance tests with OpenTofu against a real tenant. Needs `IGNITIFY_API_URL` and `IGNITIFY_API_KEY` (an owner key). They create objects with random names and destroy them. The deployment test builds twice and needs a tier with webhooks and two free deployment slots. |
| `make check-cases PLATFORM_DIR=<path>` | Checks that `internal/source/testdata/cases.json` (the shared file-selection cases) equals the platform's copy byte for byte. Skipped without `PLATFORM_DIR`. |
| `make docs` | Generate the registry docs in `docs/` with a pinned `tfplugindocs`. |
| `make docs-check` | Fail when `docs/` differs from what `make docs` makes. |
| `make notices` | Write `THIRD_PARTY_NOTICES` with a pinned `go-licenses`; fail on a licence outside MPL-2.0, MIT, BSD and Apache-2.0. |
| `make notices-check` | Fail when `THIRD_PARTY_NOTICES` is stale. |
| `make release-check` | `goreleaser check` and a local snapshot build (no publish, no signature) in a pinned container, into `dist/`. |
| `make secret-scan` | `gitleaks` over the whole git history in a pinned container. |

Every image, tool and Go module is pinned to an exact version. The tools
(`tfplugindocs`, `go-licenses`) are pinned in `tools/go.mod`.

## Documentation

The registry docs are in `docs/`, generated from the provider schema and the
examples in `examples/provider`, `examples/resources` and
`examples/data-sources` (`make docs`). `examples/access` and `examples/app`
are the two full root modules described above.

## Security

Do not report a security issue in a public issue or pull request. Report it
privately by email to admin@ignitify.dev.

## Contributing

Pull requests are welcome. Every commit needs a `Signed-off-by:` line
(`git commit -s`): the sign-off states that you may submit the change under
the [Developer Certificate of Origin](https://developercertificate.org/). No
bot checks it; a maintainer does before the merge. There is no contributor
licence agreement: a contribution comes in under MPL-2.0 and you keep the
copyright of your code. The issue tracker opens with v0.3.

## Licence

Copyright 2026 Ignitify and contributors.

Mozilla Public License 2.0, see `LICENSE`. Each release zip also carries
`THIRD_PARTY_NOTICES`, the licences of the Go modules built into the binary.

"Ignitify" is a trademark. The licence gives no right to use it (MPL-2.0
§2.3): a fork must not use the name in a way that suggests it is the
original or endorsed.

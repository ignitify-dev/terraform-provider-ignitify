# Access control: users, groups and the user key of a CI system.
# Apply with an OWNER key (IGNITIFY_API_KEY). Owner keys only manage access.
# The state holds the raw user key: keep it in an encrypted, access-controlled backend.

terraform {
  required_version = ">= 1.11"
  required_providers {
    ignitify = {
      source = "ignitify-dev/ignitify"
    }
  }
}

provider "ignitify" {}

data "ignitify_whoami" "me" {}

check "owner_key" {
  assert {
    condition     = data.ignitify_whoami.me.is_owner_key
    error_message = "This configuration manages access and needs an owner key."
  }
}

resource "ignitify_user" "ci" {
  name = "ci"
}

resource "ignitify_group" "deployers" {
  name = "deployers"
  permissions = [
    "tasks.read",
    "tasks.write",
    "deployments.read",
    "deployments.write",
    "secrets.read",
    "secrets.write",
    "hooks.read",
    "hooks.write",
    "runs.read",
    # Add "runs.write" when CI also starts runs (ignitify task:run).
  ]
  members = [ignitify_user.ci.name]
}

resource "ignitify_api_key" "ci" {
  user  = ignitify_user.ci.name
  label = "ci-pipeline"
}

# Pass the key to your CI system's secret store; never print it.
output "ci_key" {
  value     = ignitify_api_key.ci.raw_key
  sensitive = true
}

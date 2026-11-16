terraform {
  required_version = ">= 1.11"
  required_providers {
    ignitify = {
      source = "ignitify-dev/ignitify"
    }
  }
}

# The key comes from IGNITIFY_API_KEY; do not commit a key.
# Users, groups and API keys need an owner key. Secrets, tasks, deployments
# and hooks work with a user key whose groups grant their permissions.
provider "ignitify" {}

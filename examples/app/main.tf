# App resources: secrets the tasks read.
# Apply with a USER key (IGNITIFY_API_KEY) whose user is in a group with
# secrets.read and secrets.write, for example the key made by ../access.

terraform {
  required_version = ">= 1.11"
  required_providers {
    ignitify = {
      source = "ignitify-dev/ignitify"
    }
  }
}

provider "ignitify" {}

variable "db_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "db_password_version" {
  type        = number
  default     = 1
  description = "Raise it to send a new db_password to the API."
}

resource "ignitify_secret" "db_password" {
  name             = "DB_PASSWORD"
  value_wo         = var.db_password
  value_wo_version = var.db_password_version
}

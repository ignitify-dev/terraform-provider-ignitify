variable "db_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

# The value is write-only: it never reaches the plan or the state.
# Raise value_wo_version to send a new value.
resource "ignitify_secret" "db_password" {
  name             = "DB_PASSWORD"
  value_wo         = var.db_password
  value_wo_version = 1
}

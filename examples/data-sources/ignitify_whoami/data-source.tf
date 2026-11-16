data "ignitify_whoami" "me" {}

# Stop early when the configuration needs an owner key.
check "owner_key" {
  assert {
    condition     = data.ignitify_whoami.me.is_owner_key
    error_message = "This configuration manages access and needs an owner key."
  }
}

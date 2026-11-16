resource "ignitify_api_key" "ci" {
  user  = "ci"
  label = "ci-pipeline"
}

# The raw key is in the state as a sensitive value. Pass it to a secret store;
# never print it.
output "ci_key" {
  value     = ignitify_api_key.ci.raw_key
  sensitive = true
}

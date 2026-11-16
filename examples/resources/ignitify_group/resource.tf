resource "ignitify_group" "deployers" {
  name        = "deployers"
  permissions = ["tasks.read", "tasks.write", "deployments.read", "deployments.write"]
  members     = ["ci"]
}

# A change of the files in source_dir uploads them and waits for the build.
# The old deployment is deleted once the new one is READY.
resource "ignitify_deployment" "report" {
  task       = ignitify_task.report.slug
  source_dir = "${path.module}/report"

  timeouts = {
    create = "45m"
    update = "45m"
  }
}

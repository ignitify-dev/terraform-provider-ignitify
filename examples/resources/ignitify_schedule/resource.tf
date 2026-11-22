# Every night at 03:00 Berlin time, a run of the task's newest ready deployment.
# The task comes from the deployment, so the schedule is created after the
# first build is READY and its run settings are checked against it.
resource "ignitify_schedule" "nightly" {
  task     = ignitify_deployment.report.task
  cron     = "0 3 * * *"
  timezone = "Europe/Berlin"

  # Stored in the state in plain text: never put a secret here. The task
  # reads secrets through ignitify_secret and ctx.secrets.
  payload = jsonencode({ day = "yesterday" })

  timeout_sec = 600
  overlap     = "skip"
  paused      = false
}

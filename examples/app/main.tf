# App resources: a task, its deployed code, a hook and a nightly schedule that
# start it, and the secrets it reads. Apply with a USER key (IGNITIFY_API_KEY)
# whose user is in a group with tasks.*, deployments.*, hooks.*, runs.read,
# schedules.read, schedules.write, secrets.read and secrets.write, for example
# the key made by ../access. schedules.write also lets the key start runs later.

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

variable "task_dir" {
  type        = string
  description = "The task folder; its task config's slug must be \"report\"."
}

resource "ignitify_task" "report" {
  slug = "report"
}

# A change of the files in task_dir uploads them and waits for the build; the
# old deployment is deleted once the new one is READY.
resource "ignitify_deployment" "report" {
  task       = ignitify_task.report.slug
  source_dir = var.task_dir

  timeouts = {
    create = "45m"
    update = "45m"
  }
}

# The hook belongs to the task: a code update keeps its URL and secret.
resource "ignitify_hook" "report" {
  task = ignitify_task.report.slug
}

# A nightly run. The task comes from the deployment, so the schedule is made
# after the first build is READY. Destroying only the deployment leaves no
# ready one: the slots then miss, and the platform pauses the schedule.
# The payload is stored in the state in plain text: never a secret.
resource "ignitify_schedule" "nightly" {
  task     = ignitify_deployment.report.task
  cron     = "0 3 * * *"
  timezone = "Europe/Berlin"
  payload  = jsonencode({ day = "yesterday" })
}

output "hook_url" {
  value = ignitify_hook.report.url
}

output "hook_secret" {
  value     = ignitify_hook.report.secret
  sensitive = true
}

# The hook belongs to the task: a code update keeps its URL and secret.
resource "ignitify_hook" "report" {
  task = ignitify_task.report.slug
}

output "hook_url" {
  value = ignitify_hook.report.url
}

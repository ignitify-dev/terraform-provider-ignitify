# The task slug imports a task with exactly one live deployment.
terraform import ignitify_deployment.report report

# Otherwise name the deployment: <task slug>/<deployment id>.
terraform import ignitify_deployment.report report/<deployment_id>

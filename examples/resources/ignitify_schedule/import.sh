# The import id is <task slug>/<schedule_id>. After an import, payload is
# empty: the first apply sends the configured payload.
terraform import ignitify_schedule.nightly report/<schedule_id>

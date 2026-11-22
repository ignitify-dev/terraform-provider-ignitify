// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

var (
	_ resource.Resource                = (*scheduleResource)(nil)
	_ resource.ResourceWithConfigure   = (*scheduleResource)(nil)
	_ resource.ResourceWithImportState = (*scheduleResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*scheduleResource)(nil)
)

// scheduleResource is a recurring schedule of a task: the platform starts a
// run of the task's newest ready deployment at each slot of a cron expression.
// The server never answers the payload; its digest shows a payload changed
// outside Terraform.
type scheduleResource struct {
	client *client.Client
}

type scheduleModel struct {
	Task          types.String `tfsdk:"task"`
	Cron          types.String `tfsdk:"cron"`
	Timezone      types.String `tfsdk:"timezone"`
	Payload       types.String `tfsdk:"payload"`
	Preset        types.String `tfsdk:"preset"`
	TimeoutSec    types.Int64  `tfsdk:"timeout_sec"`
	DeadlineSec   types.Int64  `tfsdk:"deadline_sec"`
	Execution     types.String `tfsdk:"execution"`
	Overlap       types.String `tfsdk:"overlap"`
	Paused        types.Bool   `tfsdk:"paused"`
	ScheduleID    types.String `tfsdk:"schedule_id"`
	Status        types.String `tfsdk:"status"`
	PauseReason   types.String `tfsdk:"pause_reason"`
	PauseDetail   types.String `tfsdk:"pause_detail"`
	PayloadSHA256 types.String `tfsdk:"payload_sha256"`
	CreatedBy     types.String `tfsdk:"created_by"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

// The API's defaults for a schedule that names no zone or overlap rule.
const (
	defaultScheduleTimezone = "UTC"
	defaultScheduleOverlap  = "skip"
)

// scheduleOverlaps are what a slot may do while the previous run is live.
var scheduleOverlaps = []string{"skip", "allow"}

// The wire names of the schedule fields. A refusal's `field` names one of them.
const (
	fieldTask        = "task"
	fieldCron        = "cron"
	fieldTimezone    = "timezone"
	fieldPayload     = "payload"
	fieldPreset      = "preset"
	fieldTimeoutSec  = "timeout_sec"
	fieldDeadlineSec = "deadline_sec"
	fieldExecution   = "execution"
	fieldOverlap     = "overlap"
	fieldStatus      = "status"
)

// scheduleAttributeOfField maps a wire field to the attribute that sets it.
var scheduleAttributeOfField = map[string]string{
	fieldTask:        "task",
	fieldCron:        "cron",
	fieldTimezone:    "timezone",
	fieldPayload:     "payload",
	fieldPreset:      "preset",
	fieldTimeoutSec:  "timeout_sec",
	fieldDeadlineSec: "deadline_sec",
	fieldExecution:   "execution",
	fieldOverlap:     "overlap",
	fieldStatus:      "paused",
}

// autoPausedSummary is the plan warning of a schedule the platform paused.
const autoPausedSummary = "Schedule is auto-paused"

// missingDigestSummary is the warning of a server that sends no payload digest.
const missingDigestSummary = "Payload changes outside Terraform are not visible"

// The resource and payload descriptions; the two verbs mark the secrets
// warning, bold in the Markdown form.
const (
	scheduleDescription = "A recurring schedule of a task. The platform starts a run of the task's newest ready deployment at each slot of the cron expression, with the payload and run settings given here. " +
		"A code update (a new deployment) keeps the schedule. Needs runs.read and runs.write (runs.write also lets the key start runs). " +
		"%sThe payload is stored in the state in plain text: never put a secret in it.%s The task reads secrets through ignitify_secret. " +
		"An auto-paused schedule (paused by the platform: after repeated failures, or for a plan limit) is not changed by an apply: the plan warns, and `ignitify schedule:resume` resumes it. " +
		"If the plan does not include recurring schedules, the resume is refused: change plan first."
	payloadDescription = "The run payload as JSON (use `jsonencode`). %sStored in the state in plain text: never a secret.%s " +
		"A payload changed outside Terraform shows as a change. Empty after an import, so the first apply sends it."
)

func newScheduleResource() resource.Resource { return &scheduleResource{} }

func (r *scheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schedule"
}

func (r *scheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = schema.Schema{
		Description:         fmt.Sprintf(scheduleDescription, "", ""),
		MarkdownDescription: fmt.Sprintf(scheduleDescription, "**", "**"),
		Attributes: map[string]schema.Attribute{
			"task": schema.StringAttribute{
				Description:   "The slug of the task. A change replaces the schedule.",
				Required:      true,
				Validators:    []validator.String{taskSlugValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"cron": schema.StringAttribute{
				Description: "A 5-field cron expression, or a nickname such as `@daily`. The server checks it at apply.",
				Required:    true,
				Validators:  nonEmpty,
			},
			"timezone": schema.StringAttribute{
				Description: "The IANA time zone of the expression. Defaults to `UTC`. Write the canonical name (for example `Europe/Berlin`): after an import the state holds the server's form.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(defaultScheduleTimezone),
				Validators:  nonEmpty,
			},
			"payload": schema.StringAttribute{
				Description:         fmt.Sprintf(payloadDescription, "", ""),
				MarkdownDescription: fmt.Sprintf(payloadDescription, "**", "**"),
				Required:            true,
				Validators:          []validator.String{jsonTextRule()},
			},
			"preset": schema.StringAttribute{
				Description: "The run preset. Unset: the deployment's at each slot.",
				Optional:    true,
				Validators:  nonEmpty,
			},
			"timeout_sec": schema.Int64Attribute{
				Description: "The run timeout in seconds. Unset: the deployment's at each slot.",
				Optional:    true,
			},
			"deadline_sec": schema.Int64Attribute{
				Description: "A hard limit on the run's total time, in seconds (the timeout only limits silence). At least the run's timeout, at most a platform multiple of it; the server checks it at apply. Unset: no deadline.",
				Optional:    true,
			},
			"execution": schema.StringAttribute{
				Description: "The run's execution settings as JSON (use `jsonencode`). Unset: the deployment's at each slot.",
				Optional:    true,
				Validators:  []validator.String{jsonTextRule()},
			},
			"overlap": schema.StringAttribute{
				Description: "What a slot does while the previous run is live: `skip` (the default) or `allow`.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(defaultScheduleOverlap),
				Validators:  []validator.String{stringvalidator.OneOf(scheduleOverlaps...)},
			},
			"paused": schema.BoolAttribute{
				Description: "Whether the schedule is paused. Defaults to false. A pause made outside Terraform shows as a change, and the apply resumes it. " +
					"An auto-paused schedule stays `paused = false`: see `status`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"schedule_id": schema.StringAttribute{
				Description:   "The server id of the schedule (`sch_…`). The import id is `<task slug>/<schedule_id>`.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"status": schema.StringAttribute{
				Description: "`active`, `paused`, or `auto_paused` (paused by the platform; see `pause_reason`).",
				Computed:    true,
			},
			"pause_reason": schema.StringAttribute{
				Description: "Why the platform paused the schedule; null unless `status` is `auto_paused`.",
				Computed:    true,
			},
			"pause_detail": schema.StringAttribute{
				Description: "The plan field of a `plan-limit` pause (`max_schedules`: the plan does not include recurring schedules); otherwise null.",
				Computed:    true,
			},
			"payload_sha256": schema.StringAttribute{
				Description: "The server's sha256 of the stored payload. It changes only when the payload changes.",
				Computed:    true,
			},
			"created_by": schema.StringAttribute{
				Description:   "The id of the API key that created the schedule.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the schedule was created (ISO 8601).",
				Computed:      true,
				PlanModifiers: keep,
			},
		},
	}
}

func (r *scheduleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// privateStore is the resource's private state: values Terraform keeps with
// the resource but never shows or compares.
type privateStore interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// serverEchoKey is the private-state key of the server's last answer for the
// values it echoes in its own form.
const serverEchoKey = "server_echo"

// serverEcho is how the server answered the values it rewrites: the zone as
// its canonical name, the cron text trimmed, the execution JSON re-serialised
// ("" when none).
type serverEcho struct {
	Cron      string `json:"cron"`
	Timezone  string `json:"timezone"`
	Execution string `json:"execution"`
}

// echoOf is the server's form of a schedule's echoed values. Pure.
func echoOf(s client.Schedule) serverEcho {
	return serverEcho{Cron: s.Cron, Timezone: s.Timezone, Execution: compactJSON(s.RunSettings.Execution)}
}

// compactJSON is a JSON value without insignificant spaces, "" for none. Pure.
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return string(raw)
	}
	return out.String()
}

func loadEcho(ctx context.Context, store privateStore) (*serverEcho, diag.Diagnostics) {
	raw, diags := store.GetKey(ctx, serverEchoKey)
	if diags.HasError() || len(raw) == 0 {
		return nil, diags
	}
	var echo serverEcho
	if err := json.Unmarshal(raw, &echo); err != nil {
		// A copy that cannot be read only means the server's form wins once.
		return nil, diags
	}
	return &echo, diags
}

func saveEcho(ctx context.Context, store privateStore, echo serverEcho) diag.Diagnostics {
	raw, err := json.Marshal(echo)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Cannot record the schedule's server values", err.Error())
		return diags
	}
	return store.SetKey(ctx, serverEchoKey, raw)
}

// keepConfiguredEcho is the state value of an attribute the server echoes in
// its own form. While the server answers what it answered after the
// provider's last write (last), the state keeps the configured form, so the
// plan stays empty. A different answer (an outside change), or no recorded
// answer (after an import), puts the server's form in the state. Pure.
func keepConfiguredEcho(state types.String, server string, last *string) types.String {
	if last != nil && *last == server && !state.IsNull() && !state.IsUnknown() {
		return state
	}
	return stringOrNull(server)
}

// payloadAfterRead is the state payload after a read. While the server's
// digest equals the one recorded after the provider's last write, the
// configured payload stays. A different digest means the payload changed
// outside Terraform: the payload becomes null, so the plan sends the
// configured one again. With no recorded digest (after an import) or none from
// the server, there is nothing to compare and the payload stays. Pure.
func payloadAfterRead(payload, recorded types.String, server string) types.String {
	if recorded.IsNull() || recorded.IsUnknown() || server == "" || recorded.ValueString() == server {
		return payload
	}
	return types.StringNull()
}

// withServerValues sets the computed attributes from a server answer. After a
// create or an edit it is the state: the configuration as planned, with the
// server's computed values. Pure.
func withServerValues(m scheduleModel, s client.Schedule) scheduleModel {
	m.ScheduleID = types.StringValue(s.ScheduleID)
	m.Status = types.StringValue(s.Status)
	m.PauseReason = types.StringPointerValue(s.PauseReason)
	m.PauseDetail = types.StringPointerValue(s.PauseDetail)
	m.PayloadSHA256 = stringOrNull(s.PayloadSHA256)
	m.CreatedBy = stringOrNull(s.CreatedBy)
	m.CreatedAt = stringOrNull(s.CreatedAt)
	return m
}

// stateAfterEdit is the state and the recorded server echo after an edit (or
// the read that stands in for an empty one). The plan's values and the
// server's computed values go in the state, but a field the edit did not send
// keeps what the provider knew before: the prior payload digest and the
// previous echo (last). The edit's answer can carry a change made outside
// Terraform after the plan; recording it as ours would hide it from every
// later plan. With no previous echo (after an import) the server's form is
// recorded. Pure.
func stateAfterEdit(plan, prior scheduleModel, s client.Schedule, edit client.ScheduleEdit, last *serverEcho) (scheduleModel, serverEcho) {
	next := withServerValues(plan, s)
	sent := func(field string) bool { _, ok := edit[field]; return ok }
	if !sent(fieldPayload) {
		next.PayloadSHA256 = prior.PayloadSHA256
	}
	echo := echoOf(s)
	if last != nil {
		if !sent(fieldCron) {
			echo.Cron = last.Cron
		}
		if !sent(fieldTimezone) {
			echo.Timezone = last.Timezone
		}
		if !sent(fieldExecution) {
			echo.Execution = last.Execution
		}
	}
	return next, echo
}

// stateAfterRead is the state after a read of the schedule: the server's
// values, with the echo and payload rules over the prior state. An
// auto-paused schedule reads as not paused: the tenant did not pause it. Pure.
func stateAfterRead(prior scheduleModel, s client.Schedule, last *serverEcho) scheduleModel {
	var lastCron, lastZone, lastExecution *string
	if last != nil {
		lastCron, lastZone, lastExecution = &last.Cron, &last.Timezone, &last.Execution
	}
	settings := s.RunSettings
	next := scheduleModel{
		Task:        prior.Task,
		Cron:        keepConfiguredEcho(prior.Cron, s.Cron, lastCron),
		Timezone:    keepConfiguredEcho(prior.Timezone, s.Timezone, lastZone),
		Payload:     payloadAfterRead(prior.Payload, prior.PayloadSHA256, s.PayloadSHA256),
		Preset:      types.StringPointerValue(settings.Preset),
		TimeoutSec:  types.Int64PointerValue(settings.TimeoutSec),
		DeadlineSec: types.Int64PointerValue(settings.DeadlineSec),
		Execution:   keepConfiguredEcho(prior.Execution, compactJSON(settings.Execution), lastExecution),
		Overlap:     types.StringValue(s.Overlap),
		Paused:      types.BoolValue(s.Status == client.SchedulePaused),
	}
	return withServerValues(next, s)
}

// scheduleCreateBody is the create request of a planned schedule. Pure.
func scheduleCreateBody(plan scheduleModel) client.ScheduleCreate {
	body := client.ScheduleCreate{
		Task:        plan.Task.ValueString(),
		Cron:        plan.Cron.ValueString(),
		Timezone:    plan.Timezone.ValueString(),
		Payload:     json.RawMessage(plan.Payload.ValueString()),
		Overlap:     plan.Overlap.ValueString(),
		Preset:      plan.Preset.ValueStringPointer(),
		TimeoutSec:  plan.TimeoutSec.ValueInt64Pointer(),
		DeadlineSec: plan.DeadlineSec.ValueInt64Pointer(),
	}
	if !plan.Execution.IsNull() {
		body.Execution = json.RawMessage(plan.Execution.ValueString())
	}
	return body
}

// statusOf is the status an edit sets for a paused flag. Pure.
func statusOf(paused bool) string {
	if paused {
		return client.SchedulePaused
	}
	return client.ScheduleActive
}

// scheduleEdit is the edit that takes the schedule from the state to the
// plan: only the changed fields; a removed optional field is sent as null,
// which the server removes. Empty when nothing changed. Pure.
func scheduleEdit(plan, state scheduleModel) client.ScheduleEdit {
	edit := client.ScheduleEdit{}
	text := func(field string, planned, prior types.String) {
		if planned.Equal(prior) {
			return
		}
		if planned.IsNull() {
			edit[field] = nil
			return
		}
		edit[field] = planned.ValueString()
	}
	jsonText := func(field string, planned, prior types.String) {
		if planned.Equal(prior) {
			return
		}
		if planned.IsNull() {
			edit[field] = nil
			return
		}
		edit[field] = json.RawMessage(planned.ValueString())
	}
	number := func(field string, planned, prior types.Int64) {
		if planned.Equal(prior) {
			return
		}
		if planned.IsNull() {
			edit[field] = nil
			return
		}
		edit[field] = planned.ValueInt64()
	}
	text(fieldCron, plan.Cron, state.Cron)
	text(fieldTimezone, plan.Timezone, state.Timezone)
	text(fieldOverlap, plan.Overlap, state.Overlap)
	text(fieldPreset, plan.Preset, state.Preset)
	jsonText(fieldPayload, plan.Payload, state.Payload)
	jsonText(fieldExecution, plan.Execution, state.Execution)
	number(fieldTimeoutSec, plan.TimeoutSec, state.TimeoutSec)
	number(fieldDeadlineSec, plan.DeadlineSec, state.DeadlineSec)
	if !plan.Paused.Equal(state.Paused) {
		edit[fieldStatus] = statusOf(plan.Paused.ValueBool())
	}
	return edit
}

// autoPausedDetail is the plan warning's text for a schedule the platform
// paused, "" for any other. A plan without recurring schedules refuses the
// resume, so that warning names the plan change first. Pure.
func autoPausedDetail(state scheduleModel) string {
	if state.Status.ValueString() != client.ScheduleAutoPaused {
		return ""
	}
	id := state.ScheduleID.ValueString()
	if state.PauseReason.ValueString() == client.SchedulePauseReasonPlanLimit &&
		state.PauseDetail.ValueString() == client.SchedulePauseDetailNoSchedules {
		return fmt.Sprintf("Schedule %s is auto-paused (%s): recurring schedules are not included in your plan. An apply does not resume it, and a resume is refused: change plan first, then resume it with `ignitify schedule:resume %s`.",
			id, client.SchedulePauseReasonPlanLimit, id)
	}
	reason := state.PauseReason.ValueString()
	if reason == "" {
		reason = "no reason given"
	}
	return fmt.Sprintf("Schedule %s is auto-paused (%s). An apply does not resume it: fix the cause, then resume it with `ignitify schedule:resume %s`.",
		id, reason, id)
}

// scheduleErrorDiagnostic is an API error of a schedule call; a refusal that
// names a body field points at the attribute that sets it. Pure.
func scheduleErrorDiagnostic(action string, err error) diag.Diagnostic {
	base := apiErrorDiagnostic(action, err)
	if attr, ok := scheduleAttributeOfField[client.FieldOf(err)]; ok {
		return diag.NewAttributeErrorDiagnostic(path.Root(attr), base.Summary(), base.Detail())
	}
	return base
}

// ModifyPlan warns, on every plan, about a schedule the platform paused.
func (r *scheduleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if detail := autoPausedDetail(state); detail != "" {
		resp.Diagnostics.AddWarning(autoPausedSummary, detail)
	}
}

func (r *scheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.client.CreateSchedule(ctx, scheduleCreateBody(plan))
	if err != nil {
		resp.Diagnostics.Append(scheduleErrorDiagnostic("create schedule", err))
		return
	}
	current := created
	if plan.Paused.ValueBool() {
		// A create makes an active schedule; the pause is a second call.
		paused, err := r.client.PatchSchedule(ctx, created.ScheduleID, client.ScheduleEdit{fieldStatus: client.SchedulePaused})
		if err != nil {
			// The schedule exists: record it, tainted, so the next apply replaces it.
			resp.Diagnostics.Append(resp.State.Set(ctx, withServerValues(plan, created))...)
			resp.Diagnostics.Append(scheduleErrorDiagnostic("pause schedule "+created.ScheduleID, err))
			return
		}
		current = paused
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, withServerValues(plan, current))...)
	resp.Diagnostics.Append(saveEcho(ctx, resp.Private, echoOf(current))...)
}

func (r *scheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetSchedule(ctx, prior.ScheduleID.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(scheduleErrorDiagnostic("read schedule", err))
		return
	}
	last, diags := loadEcho(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	if found.PayloadSHA256 == "" {
		resp.Diagnostics.AddWarning(missingDigestSummary,
			"The Ignitify API answered schedule "+found.ScheduleID+" without payload_sha256, so a payload changed outside Terraform does not show in the plan. Upgrade the platform to a version that sends it.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, stateAfterRead(prior, found, last))...)
	resp.Diagnostics.Append(saveEcho(ctx, resp.Private, echoOf(found))...)
}

func (r *scheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ScheduleID.ValueString()
	edit := scheduleEdit(plan, state)
	var current client.Schedule
	var err error
	if len(edit) == 0 {
		// The server refuses an empty edit; a read gives the computed values.
		current, err = r.client.GetSchedule(ctx, id)
	} else {
		current, err = r.client.PatchSchedule(ctx, id, edit)
	}
	if err != nil {
		resp.Diagnostics.Append(scheduleErrorDiagnostic("update schedule "+id, err))
		return
	}
	last, diags := loadEcho(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	next, echo := stateAfterEdit(plan, state, current, edit, last)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
	resp.Diagnostics.Append(saveEcho(ctx, resp.Private, echo)...)
}

func (r *scheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSchedule(ctx, state.ScheduleID.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(scheduleErrorDiagnostic("delete schedule", err))
	}
}

// ImportState takes `<task slug>/<schedule_id>` and checks that the schedule
// belongs to that task. The payload cannot be read back: it stays empty, so
// the first apply sends the configured one.
func (r *scheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	task, id, ok := strings.Cut(req.ID, importIDSeparator)
	if !ok || !taskSlugPattern.MatchString(task) || id == "" {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("%q is not <task slug>/<schedule_id>.", req.ID))
		return
	}
	found, err := r.client.GetTask(ctx, task)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read task "+task, err))
		return
	}
	schedule, err := r.client.GetSchedule(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read schedule "+id, err))
		return
	}
	if schedule.TaskID != found.ID {
		resp.Diagnostics.AddError("Schedule of another task", fmt.Sprintf("Schedule %s does not belong to task %s.", id, task))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, scheduleModel{
		Task:          types.StringValue(task),
		Cron:          types.StringNull(),
		Timezone:      types.StringNull(),
		Payload:       types.StringNull(),
		Preset:        types.StringNull(),
		TimeoutSec:    types.Int64Null(),
		DeadlineSec:   types.Int64Null(),
		Execution:     types.StringNull(),
		Overlap:       types.StringNull(),
		Paused:        types.BoolNull(),
		ScheduleID:    types.StringValue(schedule.ScheduleID),
		Status:        types.StringNull(),
		PauseReason:   types.StringNull(),
		PauseDetail:   types.StringNull(),
		PayloadSHA256: types.StringNull(),
		CreatedBy:     types.StringNull(),
		CreatedAt:     types.StringNull(),
	})...)
}

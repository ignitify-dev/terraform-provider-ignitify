// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

var (
	_ resource.Resource                = (*taskResource)(nil)
	_ resource.ResourceWithConfigure   = (*taskResource)(nil)
	_ resource.ResourceWithImportState = (*taskResource)(nil)
)

// taskResource is a task. The slug is its id and import id. A destroy deletes
// the task with its deployments, runs and hooks; creating the slug again
// brings back the same task (same server id), without them.
type taskResource struct {
	client *client.Client
}

type taskModel struct {
	Slug       types.String `tfsdk:"slug"`
	TaskID     types.String `tfsdk:"task_id"`
	ResourceID types.String `tfsdk:"resource_id"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

func newTaskResource() resource.Resource { return &taskResource{} }

// taskModelFrom maps a task read to the state. Pure.
func taskModelFrom(t client.Task) taskModel {
	return taskModel{
		Slug:       types.StringValue(t.Slug),
		TaskID:     types.StringValue(t.ID),
		ResourceID: types.StringValue(t.ResourceID),
		CreatedAt:  stringOrNull(t.CreatedAt),
	}
}

func (r *taskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_task"
}

func (r *taskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A task: the name its code is deployed and run under. Needs tasks.read and tasks.write. " +
			"A destroy deletes the task with its deployments, runs and hooks.",
		Attributes: map[string]schema.Attribute{
			"slug": schema.StringAttribute{
				Description:   "The task slug (" + taskSlugRule + "). It must equal the `slug` in the task's config. A change replaces the task. The import id.",
				Required:      true,
				Validators:    []validator.String{taskSlugValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"task_id": schema.StringAttribute{
				Description:   "The server id of the task.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"resource_id": schema.StringAttribute{
				Description:   "The public resource id of the task.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the task was created (ISO 8601).",
				Computed:      true,
				PlanModifiers: keep,
			},
		},
	}
}

func (r *taskResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *taskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan taskModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug := plan.Slug.ValueString()
	created, err := r.client.CreateTask(ctx, slug)
	if err != nil {
		resp.Diagnostics.Append(taskCreateDiagnostic(slug, err))
		return
	}
	// The task exists from here on: record it before the read, so a failed
	// read leaves it in the state (tainted) instead of untracked.
	resp.Diagnostics.Append(resp.State.Set(ctx, taskModel{
		Slug:       types.StringValue(slug),
		TaskID:     types.StringValue(created.TaskID),
		ResourceID: types.StringValue(created.ResourceID),
		CreatedAt:  types.StringNull(),
	})...)
	found, err := r.client.GetTask(ctx, slug)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read task", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, taskModelFrom(found))...)
}

// taskCreateDiagnostic explains a refused task create. Pure.
func taskCreateDiagnostic(slug string, err error) diag.Diagnostic {
	if client.IsTaskSlugTaken(err) {
		return diag.NewErrorDiagnostic("Ignitify API error: create task: slug already used",
			fmt.Sprintf("A task with the slug %s already exists. Import it (terraform import <address> %s) to manage it.\n\n%s", slug, slug, err))
	}
	return apiErrorDiagnostic("create task", err)
}

func (r *taskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state taskModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetTask(ctx, state.Slug.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read task", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, taskModelFrom(found))...)
}

// Update is never called with a change: the slug forces a replace.
func (r *taskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan taskModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *taskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state taskModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteTask(ctx, state.Slug.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("delete task", err))
	}
}

func (r *taskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !taskSlugPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("%q is not a task slug (%s).", req.ID, taskSlugRule))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("slug"), req, resp)
}

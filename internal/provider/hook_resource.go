// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

var (
	_ resource.Resource                = (*hookResource)(nil)
	_ resource.ResourceWithConfigure   = (*hookResource)(nil)
	_ resource.ResourceWithImportState = (*hookResource)(nil)
)

// hookResource is an inbound hook of a task: a URL that starts a run of the
// task's newest ready deployment. It belongs to the task, so a code update
// never changes its URL or secret. The secret is shown once by the server, so
// it lives in the state as a sensitive value; it is empty after an import.
type hookResource struct {
	client *client.Client
}

type hookModel struct {
	Task       types.String `tfsdk:"task"`
	ResourceID types.String `tfsdk:"resource_id"`
	URL        types.String `tfsdk:"url"`
	Secret     types.String `tfsdk:"secret"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

func newHookResource() resource.Resource { return &hookResource{} }

func (r *hookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_hook"
}

func (r *hookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An inbound hook of a task. A POST to its URL, signed with its secret, starts a run of the task's newest ready deployment. " +
			"A code update keeps the URL and the secret. Needs hooks.read and hooks.write, and a tier with webhooks. " +
			"The secret is stored in the state as a sensitive value: encrypt and protect the state. " +
			"To rotate the URL and the secret, replace the resource.",
		Attributes: map[string]schema.Attribute{
			"task": schema.StringAttribute{
				Description:   "The slug of the task. A change replaces the hook.",
				Required:      true,
				Validators:    []validator.String{taskSlugValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_id": schema.StringAttribute{
				Description:   "The server id of the hook. The import id is `<task slug>/<resource_id>`.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"url": schema.StringAttribute{
				Description:   "The URL that triggers the hook.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"secret": schema.StringAttribute{
				Description:   "The key that signs a trigger, shown once at create. Empty after an import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the hook was created (ISO 8601).",
				Computed:      true,
				PlanModifiers: keep,
			},
		},
	}
}

func (r *hookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// hookStateFrom maps a hook read to the state, keeping the task and the
// secret the state holds (a read never answers the secret). Pure.
func hookStateFrom(task string, h client.Hook, url string, secret types.String) hookModel {
	return hookModel{
		Task:       types.StringValue(task),
		ResourceID: types.StringValue(h.ResourceID),
		URL:        types.StringValue(url),
		Secret:     secret,
		CreatedAt:  stringOrNull(h.CreatedAt),
	}
}

func (r *hookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan hookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	task := plan.Task.ValueString()
	created, err := r.createWithSecret(ctx, task)
	if created.ResourceID != "" {
		// A hook exists from here on: record it before anything else can
		// fail. With an error it is tainted, so the next apply deletes it.
		resp.Diagnostics.Append(resp.State.Set(ctx, hookJustCreated(task, created))...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("create hook", err))
		return
	}
	r.readInto(ctx, task, created.ResourceID, types.StringPointerValue(created.Secret), &resp.State, &resp.Diagnostics)
}

// hookJustCreated is the state of a hook the server has just made, before it
// is read back. A hook answered without its secret gets a null secret. Pure.
func hookJustCreated(task string, h client.HookCreated) hookModel {
	secret := types.StringNull()
	if hasHookSecret(h) {
		secret = types.StringPointerValue(h.Secret)
	}
	return hookModel{
		Task:       types.StringValue(task),
		ResourceID: types.StringValue(h.ResourceID),
		URL:        types.StringNull(),
		Secret:     secret,
		CreatedAt:  types.StringNull(),
	}
}

// createWithSecret creates a hook and returns it with its secret. A replayed
// answer has no secret (the server never stores it): that hook is deleted and
// one more is created with a fresh request. On an error the answer names the
// hook that still exists, if any (empty ResourceID when none does).
func (r *hookResource) createWithSecret(ctx context.Context, task string) (client.HookCreated, error) {
	created, err := r.client.CreateHook(ctx, task)
	if err != nil || hasHookSecret(created) {
		return created, err
	}
	if err := r.client.DeleteHook(ctx, created.ResourceID); err != nil && !client.IsGone(err) {
		return created, fmt.Errorf("the server answered hook %s without its secret, and deleting it failed; the hook is kept in the state as tainted and the next apply deletes it: %w", created.ResourceID, err)
	}
	again, err := r.client.CreateHook(ctx, task)
	if err != nil {
		return client.HookCreated{}, err
	}
	if !hasHookSecret(again) {
		return again, fmt.Errorf("the server answered hook %s without its secret twice; the hook is kept in the state as tainted and the next apply deletes it", again.ResourceID)
	}
	return again, nil
}

// hasHookSecret reports whether a create answer carries the secret. Pure.
func hasHookSecret(h client.HookCreated) bool {
	return !h.SecretRedacted && h.Secret != nil && *h.Secret != ""
}

func (r *hookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state hookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.readInto(ctx, state.Task.ValueString(), state.ResourceID.ValueString(), state.Secret, &resp.State, &resp.Diagnostics)
}

// readInto reads a hook into the state; the API's own 404 removes it.
func (r *hookResource) readInto(ctx context.Context, task, id string, secret types.String, state *tfsdk.State, diags *diag.Diagnostics) {
	found, err := r.client.GetHook(ctx, id)
	if client.IsGone(err) {
		state.RemoveResource(ctx)
		return
	}
	if err != nil {
		diags.Append(apiErrorDiagnostic("read hook", err))
		return
	}
	url, err := r.client.HookURL(found.HookID)
	if err != nil {
		diags.AddError("Invalid hook answer", err.Error())
		return
	}
	diags.Append(state.Set(ctx, hookStateFrom(task, found, url, secret))...)
}

// Update is never called with a change: the task forces a replace.
func (r *hookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan hookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *hookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state hookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteHook(ctx, state.ResourceID.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("delete hook", err))
	}
}

// ImportState takes `<task slug>/<hook resource_id>` and checks that the hook
// belongs to that task. The secret cannot be read back: it stays empty.
func (r *hookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	task, id, ok := strings.Cut(req.ID, importIDSeparator)
	if !ok || !taskSlugPattern.MatchString(task) || id == "" {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("%q is not <task slug>/<hook resource_id>.", req.ID))
		return
	}
	found, err := r.client.GetTask(ctx, task)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read task "+task, err))
		return
	}
	hook, err := r.client.GetHook(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read hook "+id, err))
		return
	}
	if hook.TaskID != found.ID {
		resp.Diagnostics.AddError("Hook of another task", fmt.Sprintf("Hook %s does not belong to task %s.", id, task))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, hookModel{
		Task:       types.StringValue(task),
		ResourceID: types.StringValue(id),
		URL:        types.StringNull(),
		Secret:     types.StringNull(),
		CreatedAt:  types.StringNull(),
	})...)
}

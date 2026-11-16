// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

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
	_ resource.Resource                = (*userResource)(nil)
	_ resource.ResourceWithConfigure   = (*userResource)(nil)
	_ resource.ResourceWithImportState = (*userResource)(nil)
)

// userResource is a tenant user. The name is its id and its import id.
type userResource struct {
	client *client.Client
}

type userModel struct {
	Name       types.String `tfsdk:"name"`
	ResourceID types.String `tfsdk:"resource_id"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

func newUserResource() resource.Resource { return &userResource{} }

// userModelFrom maps an API user to the state model. Pure.
func userModelFrom(u client.AccessUser) userModel {
	return userModel{
		Name:       types.StringValue(u.Name),
		ResourceID: types.StringValue(u.ResourceID),
		CreatedAt:  types.StringValue(u.CreatedAt),
	}
}

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A user of the tenant. A user holds API keys and gets permissions from its groups. " +
			"Needs an owner key. Deleting a user revokes all its keys.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description:   "The user name, unique in the tenant (" + accessNameRule + "). A change replaces the user. The import id.",
				Required:      true,
				Validators:    []validator.String{accessNameValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_id": schema.StringAttribute{
				Description:   "The server id of the user.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Description:   "When the user was created (ISO 8601).",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.client.CreateUser(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("create user", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userModelFrom(created))...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetUser(ctx, state.Name.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read user", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, userModelFrom(found))...)
}

// Update is never called with a change: every argument forces a replace.
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteUser(ctx, state.Name.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("delete user", err))
	}
}

func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

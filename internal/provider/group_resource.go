// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

var (
	_ resource.Resource                = (*groupResource)(nil)
	_ resource.ResourceWithConfigure   = (*groupResource)(nil)
	_ resource.ResourceWithImportState = (*groupResource)(nil)
)

// groupResource is a tenant group with its permissions and its members. The
// name is its id and its import id. This resource is the one writer of both
// sets: a grant or a membership made outside it shows as drift.
type groupResource struct {
	client *client.Client
}

type groupModel struct {
	Name        types.String `tfsdk:"name"`
	Permissions types.Set    `tfsdk:"permissions"`
	Members     types.Set    `tfsdk:"members"`
	ResourceID  types.String `tfsdk:"resource_id"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func newGroupResource() resource.Resource { return &groupResource{} }

// groupModelFrom maps an API group to the state model.
func groupModelFrom(ctx context.Context, g client.Group) (groupModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	permissions, d := types.SetValueFrom(ctx, types.StringType, sortedCopy(g.Permissions))
	diags.Append(d...)
	members, d := types.SetValueFrom(ctx, types.StringType, sortedCopy(g.Users))
	diags.Append(d...)
	return groupModel{
		Name:        types.StringValue(g.Name),
		Permissions: permissions,
		Members:     members,
		ResourceID:  types.StringValue(g.ResourceID),
		CreatedAt:   types.StringValue(g.CreatedAt),
	}, diags
}

// desiredOf reads the two sets of a model. Pure.
func desiredOf(ctx context.Context, m groupModel) (groupDesired, diag.Diagnostics) {
	var out groupDesired
	var diags diag.Diagnostics
	diags.Append(m.Permissions.ElementsAs(ctx, &out.permissions, false)...)
	if !m.Members.IsNull() && !m.Members.IsUnknown() {
		diags.Append(m.Members.ElementsAs(ctx, &out.members, false)...)
	}
	return out, diags
}

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A group of the tenant: a named set of permissions and the users that get them. Needs an owner key.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description:   "The group name, unique in the tenant (" + accessNameRule + "). A change replaces the group. The import id.",
				Required:      true,
				Validators:    []validator.String{accessNameValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"permissions": schema.SetAttribute{
				Description: "The permissions the group grants, at least one.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(permissionValidator()),
				},
			},
			"members": schema.SetAttribute{
				Description: "The user names in the group. Empty when not set: a member added outside Terraform shows as drift.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
				Validators:  []validator.Set{setvalidator.ValueStringsAre(accessNameValidator())},
			},
			"resource_id": schema.StringAttribute{
				Description:   "The server id of the group.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Description:   "When the group was created (ISO 8601).",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	want, d := desiredOf(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.client.CreateGroup(ctx, plan.Name.ValueString(), sortedCopy(want.permissions))
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("create group", err))
		return
	}
	// The group exists from here on: the state always records what the server
	// holds, so a failed membership write leaves a tainted group, not an orphan.
	have := groupDesired{permissions: created.Permissions, members: created.Users}
	last, err := r.applyOps(ctx, created, planGroupOps(have, want))
	r.saveGroup(ctx, last, &resp.State, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("set group members", err))
	}
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetGroup(ctx, state.Name.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read group", err))
		return
	}
	r.saveGroup(ctx, found, &resp.State, &resp.Diagnostics)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	want, d := desiredOf(ctx, plan)
	resp.Diagnostics.Append(d...)
	have, d := desiredOf(ctx, state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	before := client.Group{
		Name:        state.Name.ValueString(),
		ResourceID:  state.ResourceID.ValueString(),
		CreatedAt:   state.CreatedAt.ValueString(),
		Permissions: have.permissions,
		Users:       have.members,
	}
	last, err := r.applyOps(ctx, before, planGroupOps(have, want))
	r.saveGroup(ctx, last, &resp.State, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("update group", err))
	}
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteGroup(ctx, state.Name.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("delete group", err))
	}
}

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// applyOps sends the writes in order and returns the group as the last
// answer showed it. It stops at the first error. Removing a member whose user
// no longer exists is done: a removed user is in no group.
func (r *groupResource) applyOps(ctx context.Context, start client.Group, ops []groupOp) (client.Group, error) {
	last := start
	for _, op := range ops {
		next, err := r.sendOp(ctx, start.Name, op)
		if op.kind == opRemoveMember && client.HasReason(err, client.ReasonUserNotFound) {
			last = withoutMember(last, op.item)
			continue
		}
		if err != nil {
			return last, err
		}
		last = next
	}
	return last, nil
}

func (r *groupResource) sendOp(ctx context.Context, group string, op groupOp) (client.Group, error) {
	switch op.kind {
	case opGrantPermission:
		return r.client.GrantPermission(ctx, group, op.item)
	case opRevokePermission:
		return r.client.RevokePermission(ctx, group, op.item)
	case opAddMember:
		return r.client.AddGroupUser(ctx, group, op.item)
	case opRemoveMember:
		return r.client.RemoveGroupUser(ctx, group, op.item)
	default:
		return client.Group{}, fmt.Errorf("unknown group write %d; this is a bug in the provider", op.kind)
	}
}

// withoutMember returns g without the user, leaving g untouched. Pure.
func withoutMember(g client.Group, user string) client.Group {
	users := make([]string, 0, len(g.Users))
	for _, u := range g.Users {
		if u != user {
			users = append(users, u)
		}
	}
	g.Users = users
	return g
}

// saveGroup writes the group as the server answered it into the state.
func (r *groupResource) saveGroup(ctx context.Context, g client.Group, state *tfsdk.State, diags *diag.Diagnostics) {
	model, d := groupModelFrom(ctx, g)
	diags.Append(d...)
	diags.Append(state.Set(ctx, model)...)
}

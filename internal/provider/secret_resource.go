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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

var (
	_ resource.Resource                = (*secretResource)(nil)
	_ resource.ResourceWithConfigure   = (*secretResource)(nil)
	_ resource.ResourceWithImportState = (*secretResource)(nil)
)

// secretResource is a tenant secret. The value is a write-only argument: it is
// sent to the API and never stored in the plan or the state. A change of
// value_wo_version sends the value again. The name is its id and import id.
type secretResource struct {
	client *client.Client
}

// secretModel is the state. The value is not in it: it is read from the
// configuration only (secretValueOf).
type secretModel struct {
	Name           types.String `tfsdk:"name"`
	ValueWO        types.String `tfsdk:"value_wo"`
	ValueWOVersion types.Int64  `tfsdk:"value_wo_version"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

func newSecretResource() resource.Resource { return &secretResource{} }

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A tenant secret, read by tasks. The value is write-only: it never reaches the plan or the state. " +
			"Needs Terraform 1.11+ or OpenTofu 1.11+. Works with a user key that holds secrets.read and secrets.write.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description:   "The secret name (" + secretNameRule + "). A change replaces the secret. The import id.",
				Required:      true,
				Validators:    []validator.String{secretNameValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value_wo": schema.StringAttribute{
				Description: "The secret value (write-only). Terraform cannot see a change of it: change value_wo_version to send it again.",
				Required:    true,
				Sensitive:   true,
				WriteOnly:   true,
			},
			"value_wo_version": schema.Int64Attribute{
				Description: "Any number. A change sends value_wo to the API again.",
				Optional:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the value was last written (ISO 8601). A change made outside Terraform shows here.",
				Computed:    true,
			},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// secretValueOf reads the write-only value from the configuration: the plan
// never holds it.
func secretValueOf(ctx context.Context, config tfsdk.Config) (string, error) {
	var value types.String
	if diags := config.GetAttribute(ctx, path.Root("value_wo"), &value); diags.HasError() {
		return "", fmt.Errorf("read value_wo from the configuration: %v", diags)
	}
	if value.IsNull() || value.IsUnknown() {
		return "", fmt.Errorf("value_wo must be known at apply time")
	}
	return value.ValueString(), nil
}

// secretStateFrom is the state after a write or a read: the plan's arguments,
// no value, and the server's write time. Pure.
func secretStateFrom(m secretModel, s client.Secret) secretModel {
	return secretModel{
		Name:           types.StringValue(s.Name),
		ValueWO:        types.StringNull(),
		ValueWOVersion: m.ValueWOVersion,
		UpdatedAt:      stringOrNull(s.UpdatedAt),
	}
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	// Refuse a name that already exists, so an apply never overwrites a
	// secret it does not manage. The write itself is create-only; the read
	// first also covers a server that ignores the create-only header.
	_, err := r.client.GetSecret(ctx, name)
	switch {
	case err == nil:
		resp.Diagnostics.Append(secretExistsDiagnostic(name))
		return
	case !client.IsGone(err):
		resp.Diagnostics.Append(apiErrorDiagnostic("read secret", err))
		return
	}
	r.write(ctx, plan, req.Config, r.client.CreateSecret, &resp.State, &resp.Diagnostics)
}

// secretExistsDiagnostic is the refusal of a create on a name that exists. Pure.
func secretExistsDiagnostic(name string) diag.Diagnostic {
	return diag.NewErrorDiagnostic("Secret already exists",
		fmt.Sprintf("A secret named %s already exists. Import it (terraform import <address> %s) to manage it, or choose another name.", name, name))
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetSecret(ctx, state.Name.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read secret", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, secretStateFrom(state, found))...)
}

// Update runs when value_wo_version changes (a name change replaces): it sends
// the value again.
func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, req.Config, r.client.SetSecret, &resp.State, &resp.Diagnostics)
}

// secretStore is the API write of a secret value: create-only or replace.
type secretStore func(ctx context.Context, name, value string) error

// write sends the value from the configuration and records the server's write time.
func (r *secretResource) write(ctx context.Context, plan secretModel, config tfsdk.Config, store secretStore, state *tfsdk.State, diags *diag.Diagnostics) {
	value, err := secretValueOf(ctx, config)
	if err != nil {
		diags.AddError("Missing secret value", err.Error())
		return
	}
	name := plan.Name.ValueString()
	err = store(ctx, name, value)
	if client.IsSecretExists(err) {
		diags.Append(secretExistsDiagnostic(name))
		return
	}
	if err != nil {
		diags.Append(apiErrorDiagnostic("write secret", err))
		return
	}
	// The value is stored from here on: record the secret before the read, so
	// a failed read leaves it in the state (tainted on create) instead of lost.
	diags.Append(state.Set(ctx, secretStateFrom(plan, client.Secret{Name: name}))...)
	found, err := r.client.GetSecret(ctx, name)
	if err != nil {
		diags.Append(apiErrorDiagnostic("read secret", err))
		return
	}
	diags.Append(state.Set(ctx, secretStateFrom(plan, found))...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSecret(ctx, state.Name.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("delete secret", err))
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

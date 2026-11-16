// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

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
	_ resource.Resource                = (*apiKeyResource)(nil)
	_ resource.ResourceWithConfigure   = (*apiKeyResource)(nil)
	_ resource.ResourceWithImportState = (*apiKeyResource)(nil)
)

// apiKeyResource is an API key of a user. Owner keys are never made here. The
// raw key is shown once by the server, so it lives in the state as a
// sensitive value; it is empty after an import.
type apiKeyResource struct {
	client *client.Client
}

type apiKeyModel struct {
	User       types.String `tfsdk:"user"`
	Label      types.String `tfsdk:"label"`
	ResourceID types.String `tfsdk:"resource_id"`
	RawKey     types.String `tfsdk:"raw_key"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

func newAPIKeyResource() resource.Resource { return &apiKeyResource{} }

// apiKeyModelFrom maps a key read to the state model, keeping the raw key the
// state already holds (a read never answers it). Pure.
func apiKeyModelFrom(k client.AccessKey, rawKey types.String) apiKeyModel {
	return apiKeyModel{
		User:       types.StringPointerValue(k.User),
		Label:      types.StringPointerValue(k.Label),
		ResourceID: types.StringValue(k.ResourceID),
		RawKey:     rawKey,
		CreatedAt:  types.StringValue(k.CreatedAt),
	}
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An API key of a user. Needs an owner key. The raw key is stored in the state as a sensitive value: " +
			"encrypt and protect the state. A user key never expires; destroy the resource to revoke it.",
		Attributes: map[string]schema.Attribute{
			"user": schema.StringAttribute{
				Description:   "The name of the user that holds the key. A change replaces the key.",
				Required:      true,
				Validators:    []validator.String{accessNameValidator()},
				PlanModifiers: replace,
			},
			"label": schema.StringAttribute{
				Description:   "A label for the key (" + keyLabelRule + "). A change replaces the key.",
				Required:      true,
				Validators:    []validator.String{keyLabelValidator()},
				PlanModifiers: replace,
			},
			"resource_id": schema.StringAttribute{
				Description:   "The server id of the key. The import id.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"raw_key": schema.StringAttribute{
				Description:   "The key itself, shown once at create. Empty after an import.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the key was created (ISO 8601).",
				Computed:      true,
				PlanModifiers: keep,
			},
		},
	}
}

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	user, label := plan.User.ValueString(), plan.Label.ValueString()
	created, err := r.createWithRawKey(ctx, user, label)
	if created.ResourceID != "" {
		// A key exists from here on: record it before anything else can fail.
		// With an error the key is tainted, so the next apply revokes it; it is
		// never left as an orphan nobody tracks.
		resp.Diagnostics.Append(resp.State.Set(ctx, keyJustCreated(user, label, created))...)
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("create API key", err))
		return
	}
	rawKey := types.StringPointerValue(created.RawKey)
	found, err := r.client.GetKey(ctx, created.ResourceID)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read API key "+created.ResourceID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, apiKeyModelFrom(found, rawKey))...)
}

// keyJustCreated is the state of a key the server has just made, before it is
// read back. A key answered without its raw key gets a null raw_key. Pure.
func keyJustCreated(user, label string, k client.KeyCreated) apiKeyModel {
	rawKey := types.StringNull()
	if hasRawKey(k) {
		rawKey = types.StringPointerValue(k.RawKey)
	}
	return apiKeyModel{
		User:       types.StringValue(user),
		Label:      types.StringValue(label),
		ResourceID: types.StringValue(k.ResourceID),
		RawKey:     rawKey,
		CreatedAt:  types.StringNull(),
	}
}

// createWithRawKey creates a key and returns it with its raw key. A replayed
// answer has no raw key (the server never stores it): that key is revoked and
// one more is created with a fresh request. On an error the answer names the
// key that still exists, if any (empty ResourceID when none does).
func (r *apiKeyResource) createWithRawKey(ctx context.Context, user, label string) (client.KeyCreated, error) {
	created, err := r.client.CreateKey(ctx, user, label)
	if err != nil || hasRawKey(created) {
		return created, err
	}
	if err := r.client.RevokeKey(ctx, created.ResourceID); err != nil && !client.IsGone(err) {
		return created, fmt.Errorf("the server answered key %s without its raw key, and revoking it failed; the key is kept in the state as tainted and the next apply revokes it: %w", created.ResourceID, err)
	}
	again, err := r.client.CreateKey(ctx, user, label)
	if err != nil {
		return client.KeyCreated{}, err
	}
	if !hasRawKey(again) {
		return again, fmt.Errorf("the server answered key %s without its raw key twice; the key is kept in the state as tainted and the next apply revokes it", again.ResourceID)
	}
	return again, nil
}

// hasRawKey reports whether a create answer carries the raw key. Pure.
func hasRawKey(k client.KeyCreated) bool {
	return !k.SecretRedacted && k.RawKey != nil && *k.RawKey != ""
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetKey(ctx, state.ResourceID.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read API key", err))
		return
	}
	if found.User == nil {
		resp.Diagnostics.AddError("Not a user key",
			fmt.Sprintf("Key %s is an owner key. This provider manages user keys only; owner keys come from the sign-in flow.", found.ResourceID))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, apiKeyModelFrom(found, state.RawKey))...)
}

// Update is never called with a change: every argument forces a replace.
func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.RevokeKey(ctx, state.ResourceID.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("revoke API key", err))
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("resource_id"), req, resp)
}

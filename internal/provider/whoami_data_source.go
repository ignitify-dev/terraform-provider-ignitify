// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

var (
	_ datasource.DataSource              = (*whoamiDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*whoamiDataSource)(nil)
)

// whoamiDataSource reads the tenant, the calling key and the quota snapshot.
// Any key may read it, so a configuration can assert it runs against the
// intended tenant with the intended kind of key.
type whoamiDataSource struct {
	client *client.Client
}

type whoamiModel struct {
	TenantID    types.String      `tfsdk:"tenant_id"`
	TenantName  types.String      `tfsdk:"tenant_name"`
	Status      types.String      `tfsdk:"status"`
	TierName    types.String      `tfsdk:"tier_name"`
	TierVersion types.Int64       `tfsdk:"tier_version"`
	KeyID       types.String      `tfsdk:"key_id"`
	User        types.String      `tfsdk:"user"`
	IsOwnerKey  types.Bool        `tfsdk:"is_owner_key"`
	Groups      types.Set         `tfsdk:"groups"`
	Permissions types.Set         `tfsdk:"permissions"`
	Limits      whoamiLimitsModel `tfsdk:"limits"`
	Usage       whoamiUsageModel  `tfsdk:"usage"`
}

type whoamiLimitsModel struct {
	MaxTasks           types.Int64 `tfsdk:"max_tasks"`
	MaxRunningVMs      types.Int64 `tfsdk:"max_running_vms"`
	MaxUnits           types.Int64 `tfsdk:"max_units"`
	MaxPinnedVMs       types.Int64 `tfsdk:"max_pinned_vms"`
	MaxTotalImageBytes types.Int64 `tfsdk:"max_total_image_bytes"`
	MaxStorageMiB      types.Int64 `tfsdk:"max_storage_mib"`
	MaxPayloadBytes    types.Int64 `tfsdk:"max_payload_bytes"`
	MaxResultBytes     types.Int64 `tfsdk:"max_result_bytes"`
}

type whoamiUsageModel struct {
	TaskCount       types.Int64 `tfsdk:"task_count"`
	ActiveRuns      types.Int64 `tfsdk:"active_runs"`
	ActiveUnits     types.Int64 `tfsdk:"active_units"`
	DeploymentCount types.Int64 `tfsdk:"deployment_count"`
	TotalImageBytes types.Int64 `tfsdk:"total_image_bytes"`
	PinnedVMs       types.Int64 `tfsdk:"pinned_vms"`
}

func newWhoamiDataSource() datasource.DataSource { return &whoamiDataSource{} }

// whoamiModelFrom maps the identity answer to the model. A server that sends
// no `access` block leaves the key fields null.
func whoamiModelFrom(ctx context.Context, w client.Whoami) (whoamiModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	access := client.WhoamiAccess{}
	if w.Access != nil {
		access = *w.Access
	}
	groups, d := types.SetValueFrom(ctx, types.StringType, sortedCopy(access.Groups))
	diags.Append(d...)
	permissions, d := types.SetValueFrom(ctx, types.StringType, sortedCopy(access.Permissions))
	diags.Append(d...)
	isOwner := types.BoolNull()
	if w.Access != nil {
		isOwner = types.BoolValue(access.User == nil)
	}
	return whoamiModel{
		TenantID:    types.StringValue(w.TenantID),
		TenantName:  types.StringValue(w.Name),
		Status:      types.StringValue(w.Status),
		TierName:    types.StringValue(w.Tier.Name),
		TierVersion: types.Int64Value(w.Tier.Version),
		KeyID:       stringOrNull(access.Key),
		User:        types.StringPointerValue(access.User),
		IsOwnerKey:  isOwner,
		Groups:      groups,
		Permissions: permissions,
		Limits: whoamiLimitsModel{
			MaxTasks:           types.Int64PointerValue(w.Limits.MaxTasks),
			MaxRunningVMs:      types.Int64PointerValue(w.Limits.MaxRunningVMs),
			MaxUnits:           types.Int64PointerValue(w.Limits.MaxUnits),
			MaxPinnedVMs:       types.Int64PointerValue(w.Limits.MaxPinnedVMs),
			MaxTotalImageBytes: types.Int64PointerValue(w.Limits.MaxTotalImageBytes),
			MaxStorageMiB:      types.Int64Value(w.Limits.MaxStorageMiB),
			MaxPayloadBytes:    types.Int64Value(w.Limits.MaxPayloadBytes),
			MaxResultBytes:     types.Int64Value(w.Limits.MaxResultBytes),
		},
		Usage: whoamiUsageModel{
			TaskCount:       types.Int64Value(w.Usage.TaskCount),
			ActiveRuns:      types.Int64Value(w.Usage.ActiveRuns),
			ActiveUnits:     types.Int64Value(w.Usage.ActiveUnits),
			DeploymentCount: types.Int64Value(w.Usage.DeploymentCount),
			TotalImageBytes: types.Int64Value(w.Usage.TotalImageBytes),
			PinnedVMs:       types.Int64Value(w.Usage.PinnedVMs),
		},
	}, diags
}

func (d *whoamiDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_whoami"
}

func computedInt64s(descriptions map[string]string) map[string]schema.Attribute {
	out := make(map[string]schema.Attribute, len(descriptions))
	for name, description := range descriptions {
		out[name] = schema.Int64Attribute{Description: description, Computed: true}
	}
	return out
}

func (d *whoamiDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(description string) schema.Attribute {
		return schema.StringAttribute{Description: description, Computed: true}
	}
	resp.Schema = schema.Schema{
		Description: "The tenant, the calling API key and the quota snapshot. Works with any key.",
		Attributes: map[string]schema.Attribute{
			"tenant_id":    str("The tenant id."),
			"tenant_name":  str("The tenant name."),
			"status":       str("The tenant status."),
			"tier_name":    str("The tenant's tier."),
			"tier_version": schema.Int64Attribute{Description: "The tier version.", Computed: true},
			"key_id":       str("The resource id of the calling key."),
			"user":         str("The user that holds the calling key; null for an owner key."),
			"is_owner_key": schema.BoolAttribute{Description: "Whether the calling key is an owner key.", Computed: true},
			"groups":       schema.SetAttribute{Description: "The groups of the key's user.", ElementType: types.StringType, Computed: true},
			"permissions":  schema.SetAttribute{Description: "The permissions the key holds.", ElementType: types.StringType, Computed: true},
			"limits": schema.SingleNestedAttribute{
				Description: "The summary limits. A null limit is unlimited.",
				Computed:    true,
				Attributes: computedInt64s(map[string]string{
					"max_tasks":             "Most tasks.",
					"max_running_vms":       "Most VMs running at once.",
					"max_units":             "Most run units in use at once.",
					"max_pinned_vms":        "Most pinned VMs.",
					"max_total_image_bytes": "Most bytes in the image store.",
					"max_storage_mib":       "Scratch storage per run, in MiB.",
					"max_payload_bytes":     "Most bytes passed into a task (the same for every tenant).",
					"max_result_bytes":      "Most bytes a task sends back (the same for every tenant).",
				}),
			},
			"usage": schema.SingleNestedAttribute{
				Description: "What the tenant holds now.",
				Computed:    true,
				Attributes: computedInt64s(map[string]string{
					"task_count":        "Tasks.",
					"active_runs":       "Active runs.",
					"active_units":      "Run units in use.",
					"deployment_count":  "Deployments.",
					"total_image_bytes": "Bytes in the image store.",
					"pinned_vms":        "Pinned VMs.",
				}),
			},
		},
	}
}

func (d *whoamiDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *whoamiDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	found, err := d.client.Whoami(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read identity", err))
		return
	}
	model, diags := whoamiModelFrom(ctx, found)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

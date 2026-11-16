// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	_ resource.Resource                   = (*deploymentResource)(nil)
	_ resource.ResourceWithConfigure      = (*deploymentResource)(nil)
	_ resource.ResourceWithImportState    = (*deploymentResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*deploymentResource)(nil)
	_ resource.ResourceWithValidateConfig = (*deploymentResource)(nil)
)

// deploymentResource is the deployed code of a task. The task is its
// identity: one resource per task. A change of the selected files uploads them
// and waits for the build; the new deployment replaces the old one in place,
// and the old one is deleted once the new one is READY. A failed build keeps
// the old deployment.
type deploymentResource struct {
	client *client.Client
	// sleep waits between status polls; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

type deploymentModel struct {
	Task                  types.String   `tfsdk:"task"`
	SourceDir             types.String   `tfsdk:"source_dir"`
	SourceHash            types.String   `tfsdk:"source_hash"`
	DeploymentID          types.String   `tfsdk:"deployment_id"`
	Status                types.String   `tfsdk:"status"`
	ContentHash           types.String   `tfsdk:"content_hash"`
	Execution             types.String   `tfsdk:"execution"`
	Secrets               types.List     `tfsdk:"secrets"`
	NotifyURL             types.String   `tfsdk:"notify_url"`
	NotifySecretWO        types.String   `tfsdk:"notify_secret_wo"`
	NotifySecretWOVersion types.Int64    `tfsdk:"notify_secret_wo_version"`
	Timeouts              timeouts.Value `tfsdk:"timeouts"`
}

const (
	// defaultBuildTimeout bounds an upload and its build; a cold build is slow.
	defaultBuildTimeout = 30 * time.Minute
	// notifySecretMinLength is the shortest notify secret the server accepts,
	// counted as the server counts a string: in UTF-16 code units.
	notifySecretMinLength = 32
	// notifyURLScheme is the only scheme the server accepts for a notify URL.
	notifyURLScheme = "https"
)

func newDeploymentResource() resource.Resource {
	return &deploymentResource{sleep: sleepContext}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *deploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

func (r *deploymentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The deployed code of a task: the files of a task folder, uploaded and built by the server. " +
			"A change of the files uploads them again and waits for the build. Once the new deployment is READY, the old one is deleted, " +
			"which ends its running runs and deletes its run history; hooks belong to the task and are not changed. " +
			"A failed build keeps the old deployment. An update needs one free deployment slot. " +
			"Needs tasks.read, deployments.read and deployments.write; the notify fields also need hooks.write.",
		Attributes: map[string]schema.Attribute{
			"task": schema.StringAttribute{
				Description:   "The slug of the task. It must equal the `slug` in the folder's task config. A change replaces the deployment. The import id.",
				Required:      true,
				Validators:    []validator.String{taskSlugValidator()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source_dir": schema.StringAttribute{
				Description: "The task folder. The upload holds the files a deploy selects: no `.env` files, vendored dependencies or local state, " +
					"and not the paths `.ignitifyignore` leaves out. Symlinks and registry credentials are refused.",
				Required: true,
			},
			"source_hash": schema.StringAttribute{
				Description: "A digest of the selected files, computed at plan time. A change uploads the files again.",
				Computed:    true,
			},
			"deployment_id": schema.StringAttribute{
				Description: "The server id of the live deployment. Unknown in a plan that uploads.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The deployment status: READY after a successful apply. Any other status uploads again at the next apply.",
				Computed:    true,
			},
			"content_hash": schema.StringAttribute{
				Description: "The server's content key of the deployment.",
				Computed:    true,
			},
			"execution": schema.StringAttribute{
				Description: "The `execution` block the task config declares, as JSON; null when it declares none.",
				Computed:    true,
			},
			"secrets": schema.ListAttribute{
				Description: "The secret names the task config declares; null when it declares none.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"notify_url": schema.StringAttribute{
				Description: "A URL the deployment calls when a run ends. Unset clears it. Needs notify_secret_wo.",
				Optional:    true,
			},
			"notify_secret_wo": schema.StringAttribute{
				Description: fmt.Sprintf("The key that signs each notify call (write-only, at least %d characters). "+
					"Terraform cannot see a change of it: change notify_secret_wo_version to send it again.", notifySecretMinLength),
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
			},
			"notify_secret_wo_version": schema.Int64Attribute{
				Description: "Any number. A change sends notify_url and notify_secret_wo again.",
				Optional:    true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

func (r *deploymentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ValidateConfig checks the notify fields together: an https URL, a secret
// of the server's minimum length, and neither without the other.
func (r *deploymentResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var url, secret types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("notify_url"), &url)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("notify_secret_wo"), &secret)...)
	if resp.Diagnostics.HasError() || url.IsUnknown() || secret.IsUnknown() {
		return
	}
	if problem := notifyURLProblem(url); problem != "" {
		resp.Diagnostics.AddAttributeError(path.Root("notify_url"), "Invalid notify URL", problem)
	}
	switch {
	case !url.IsNull() && secret.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("notify_secret_wo"), "Missing notify secret", "notify_url needs notify_secret_wo.")
	case url.IsNull() && !secret.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("notify_url"), "Missing notify URL", "notify_secret_wo needs notify_url.")
	case !secret.IsNull() && utf16Length(secret.ValueString()) < notifySecretMinLength:
		resp.Diagnostics.AddAttributeError(path.Root("notify_secret_wo"), "Notify secret too short",
			fmt.Sprintf("notify_secret_wo must be at least %d characters.", notifySecretMinLength))
	}
}

// notifyURLProblem says why a notify URL is refused before any call, or "".
// The server also refuses a host that resolves to a private address; that
// needs a lookup and is left to it. Pure.
func notifyURLProblem(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	u, err := neturl.Parse(value.ValueString())
	if err != nil || u.Host == "" {
		return "notify_url must be an absolute URL."
	}
	if !strings.EqualFold(u.Scheme, notifyURLScheme) {
		return "notify_url must be an https URL."
	}
	return ""
}

// utf16Length is the length of s in UTF-16 code units. Pure.
func utf16Length(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// deploymentStateFrom is the state after a read: the server's facts about the
// deployment over the arguments the state already holds. Pure.
func deploymentStateFrom(base deploymentModel, d client.Deployment) (deploymentModel, diag.Diagnostics) {
	secrets, diags := secretNamesValue(d.Secrets)
	out := base
	out.DeploymentID = types.StringValue(d.ID)
	out.Status = types.StringValue(d.Status)
	out.ContentHash = stringOrNull(d.ContentHash)
	out.Execution = executionText(d.Execution)
	out.Secrets = secrets
	out.NotifyURL = types.StringPointerValue(d.NotifyURL)
	out.NotifySecretWO = types.StringNull()
	return out, diags
}

// executionText is the declared `execution` block as compact JSON with sorted
// keys, so the same block always reads the same; null when none. Pure.
func executionText(raw json.RawMessage) types.String {
	var parsed any
	if len(raw) == 0 || json.Unmarshal(raw, &parsed) != nil || parsed == nil {
		return types.StringNull()
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return types.StringNull()
	}
	return types.StringValue(string(canonical))
}

// secretNamesValue is the declared secret names as a list; null when none. Pure.
func secretNamesValue(names []string) (types.List, diag.Diagnostics) {
	if names == nil {
		return types.ListNull(types.StringType), nil
	}
	elems := make([]attr.Value, 0, len(names))
	for _, n := range names {
		elems = append(elems, types.StringValue(n))
	}
	return types.ListValue(types.StringType, elems)
}

func (r *deploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.client.GetDeployment(ctx, state.DeploymentID.ValueString())
	if client.IsGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("read deployment", err))
		return
	}
	if found.TaskSlug != state.Task.ValueString() {
		resp.Diagnostics.AddError("Deployment of another task",
			fmt.Sprintf("Deployment %s belongs to task %s, not %s.", found.ID, found.TaskSlug, state.Task.ValueString()))
		return
	}
	next, diags := deploymentStateFrom(state, found)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *deploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDeployment(ctx, state.DeploymentID.ValueString()); err != nil && !client.IsGone(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("delete deployment", err))
	}
}

// ImportState takes the task slug, or `<task slug>/<deployment id>`. The API
// has no "current deployment" of a task (a hook runs the newest READY one by
// build time, which a read does not show), so a slug alone imports only a task
// with exactly one live deployment, which is what this resource leaves after
// each apply. With several, the import names them and asks for one.
func (r *deploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	task, id, _ := strings.Cut(req.ID, importIDSeparator)
	if !taskSlugPattern.MatchString(task) {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("%q is not <task slug> or <task slug>/<deployment id>.", req.ID))
		return
	}
	if id == "" {
		live, err := r.client.ListTaskDeployments(ctx, task)
		if err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("list deployments of task "+task, err))
			return
		}
		chosen, problem := soleDeployment(task, live)
		if problem != "" {
			resp.Diagnostics.AddError("Cannot choose a deployment", problem)
			return
		}
		id = chosen
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("task"), task)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deployment_id"), id)...)
}

// soleDeployment is the id of a task's only live deployment, or why there is
// none to choose. Pure.
func soleDeployment(task string, live []client.Deployment) (string, string) {
	switch len(live) {
	case 0:
		return "", fmt.Sprintf("Task %s has no live deployment.", task)
	case 1:
		return live[0].ID, ""
	}
	ids := make([]string, 0, len(live))
	for _, d := range live {
		ids = append(ids, d.ID+" ("+d.Status+")")
	}
	return "", fmt.Sprintf("Task %s has %d live deployments: %s. Import one with the id <task slug>/<deployment id>.",
		task, len(live), strings.Join(ids, ", "))
}

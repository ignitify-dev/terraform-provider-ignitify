// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
	"github.com/ignitify-dev/terraform-provider-ignitify/internal/source"
)

const (
	// pollFirstWait is the wait after the first status poll; each later wait
	// doubles, up to pollMaxWait.
	pollFirstWait = 2 * time.Second
	pollMaxWait   = 15 * time.Second
)

// ModifyPlan selects the files of source_dir and computes their digest, so a
// plan shows a change when the code changes. It refuses at plan time what the
// upload would refuse (a registry credential, a symlink, a missing lockfile)
// and shows the selection warnings. When the digest changes, or the live
// deployment is not READY, the server facts become unknown: the apply uploads.
func (r *deploymentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hash := types.StringUnknown()
	if !plan.SourceDir.IsUnknown() {
		sel := source.Select(plan.SourceDir.ValueString())
		if len(sel.Warnings) > 0 {
			resp.Diagnostics.AddAttributeWarning(path.Root("source_dir"), "Source selection", strings.Join(sel.Warnings, "\n"))
		}
		if !sel.OK() {
			resp.Diagnostics.AddAttributeError(path.Root("source_dir"), "The task folder cannot be uploaded", strings.Join(sel.Errors, "\n"))
			return
		}
		digest, err := source.Digest(sel.Files)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("source_dir"), "The task folder cannot be archived", err.Error())
			return
		}
		hash = types.StringValue(digest)
	}
	r.checkWebhooksAllowed(ctx, plan.NotifyURL, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var prior *deploymentModel
	if !req.State.Raw.IsNull() {
		prior = &deploymentModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, prior)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, plannedDeployment(plan, prior, hash))...)
}

// samePlannedObject is the prior state when the plan updates it in place, nil
// when the plan creates a new deployment (no prior, or a task change, which
// replaces). Pure.
func samePlannedObject(plan deploymentModel, prior *deploymentModel) *deploymentModel {
	if prior == nil || !prior.Task.Equal(plan.Task) {
		return nil
	}
	return prior
}

// checkWebhooksAllowed refuses a notify_url at plan time when the tenant's
// tier has no webhooks: the server would refuse every notify write. A whoami
// that fails only warns; the apply then reports the server's answer.
func (r *deploymentResource) checkWebhooksAllowed(ctx context.Context, notifyURL types.String, diags *diag.Diagnostics) {
	if r.client == nil || notifyURL.IsNull() || notifyURL.IsUnknown() {
		return
	}
	who, err := r.client.Whoami(ctx)
	if err != nil {
		diags.AddAttributeWarning(path.Root("notify_url"), "Webhook entitlement not checked", err.Error())
		return
	}
	if problem := webhooksProblem(who.Policy.MayUseWebhooks); problem != "" {
		diags.AddAttributeError(path.Root("notify_url"), "Webhooks not in the tier", problem)
	}
}

// webhooksProblem says why a notify target cannot be set under this
// entitlement, or "" (also when the server did not answer the flag). Pure.
func webhooksProblem(mayUseWebhooks *bool) string {
	if mayUseWebhooks == nil || *mayUseWebhooks {
		return ""
	}
	return "The tenant's tier does not include webhooks, so the server refuses a notify target. Remove notify_url, or change the tier."
}

// plannedDeployment fills the computed values of a plan: the new source
// digest, and the server facts kept from the state, or unknown when the apply
// uploads. Pure.
func plannedDeployment(plan deploymentModel, prior *deploymentModel, hash types.String) deploymentModel {
	out := plan
	out.SourceHash = hash
	if same := samePlannedObject(plan, prior); same != nil && !needsUpload(*same, hash) {
		out.DeploymentID = same.DeploymentID
		out.Status = same.Status
		out.ContentHash = same.ContentHash
		out.Execution = same.Execution
		out.Secrets = same.Secrets
		return out
	}
	out.DeploymentID = types.StringUnknown()
	out.Status = types.StringUnknown()
	out.ContentHash = types.StringUnknown()
	out.Execution = types.StringUnknown()
	out.Secrets = types.ListUnknown(types.StringType)
	return out
}

// needsUpload reports whether an apply uploads: the files changed (or cannot
// be known before the apply), or the live deployment is not READY. Pure.
func needsUpload(prior deploymentModel, hash types.String) bool {
	return hash.IsUnknown() || !hash.Equal(prior.SourceHash) || prior.Status.ValueString() != client.DeploymentReady
}

// notifyAction is what an apply does to a deployment's notify target.
type notifyAction int

const (
	notifyKeep notifyAction = iota
	notifySet
	notifyClear
)

// decideNotify chooses the notify write. A secret cannot be read back, so it
// is sent again when the deployment is new, its version changed, or the URL
// differs from what the server holds. Pure.
func decideNotify(desired types.String, current *string, newDeployment, versionChanged bool) notifyAction {
	if desired.IsNull() {
		if current != nil {
			return notifyClear
		}
		return notifyKeep
	}
	if newDeployment || versionChanged || current == nil || *current != desired.ValueString() {
		return notifySet
	}
	return notifyKeep
}

// buildState is where a polled build stands.
type buildState int

const (
	buildRunning buildState = iota
	buildReady
	buildFailed
	buildUnexpected
)

// buildStateOf reads a deployment status. A status the provider does not know
// stops the wait instead of waiting for the whole timeout. Pure.
func buildStateOf(status string) buildState {
	switch status {
	case client.DeploymentBuilding:
		return buildRunning
	case client.DeploymentReady:
		return buildReady
	case client.DeploymentFailed:
		return buildFailed
	default:
		return buildUnexpected
	}
}

// pollWait is the wait after poll number attempt (from 0). Pure.
func pollWait(attempt int) time.Duration {
	wait := pollFirstWait << attempt
	if wait <= 0 || wait > pollMaxWait {
		return pollMaxWait
	}
	return wait
}

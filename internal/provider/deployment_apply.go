// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
	"github.com/ignitify-dev/terraform-provider-ignitify/internal/source"
)

// cleanupTimeout bounds the delete of a deployment that did not become ready.
// It runs after the build timeout may have expired, so it has its own deadline.
const cleanupTimeout = time.Minute

// buildError is an upload that did not end READY. Deployment names the
// deployment the server made, if any (a FAILED or still BUILDING one).
type buildError struct {
	Deployment client.Deployment
	err        error
}

func (e *buildError) Error() string { return e.err.Error() }
func (e *buildError) Unwrap() error { return e.err }

// uploadResult is what an upload left on the server.
type uploadResult struct {
	// Deployment is the READY deployment, or the one that did not get there.
	Deployment client.Deployment
	// Hash is the source digest of what was sent.
	Hash types.String
	// Reused is true when the server answered with a deployment that already
	// existed for the same content (ready, or a build already running).
	Reused bool
}

// notifyNotSetSummary is the warning of a create whose deployment is live but
// whose notify target the server refused.
const notifyNotSetSummary = "Notify target not set"

func (r *deploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, defaultBuildTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := r.upload(opCtx, plan, timeout)
	base := withSourceHash(plan, res.Hash)
	if res.Deployment.ID != "" {
		// A deployment exists from here on: record it before anything else can
		// fail. With an error it is tainted, so the next apply deletes it.
		r.setState(ctx, base, res.Deployment, &resp.State, &resp.Diagnostics)
	}
	if err != nil {
		resp.Diagnostics.Append(deployErrorDiagnostic(err))
		return
	}
	if err := r.applyNotify(opCtx, req.Config, plan, nil, res.Deployment, true); err != nil {
		// The deployment is READY and must not be tainted for a notify refusal:
		// a rebuild would not change the refusal. The state keeps the planned
		// notify_url (Terraform requires the planned value back); the next
		// refresh reads the server's value, so the next refreshed plan shows the change
		// and retries only the notify write.
		resp.Diagnostics.AddWarning(notifyNotSetSummary,
			fmt.Sprintf("Deployment %s is live, but its notify target was not set: %s\nThe next plan with refresh (the default) shows notify_url as a change and sets it again; with -refresh=false it stays unset.", res.Deployment.ID, err))
		pending := res.Deployment
		pending.NotifyURL = plan.NotifyURL.ValueStringPointer()
		r.setState(ctx, base, pending, &resp.State, &resp.Diagnostics)
		return
	}
	r.readBack(opCtx, base, res.Deployment.ID, &resp.State, &resp.Diagnostics)
}

func (r *deploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, defaultBuildTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	oldID := prior.DeploymentID.ValueString()
	// The plan made deployment_id unknown when the apply uploads.
	next := withSourceHash(plan, prior.SourceHash)
	var current client.Deployment
	var err error
	if plan.DeploymentID.IsUnknown() {
		var res uploadResult
		res, err = r.upload(opCtx, plan, timeout)
		current, next = res.Deployment, withSourceHash(plan, res.Hash)
		if err != nil {
			r.dropFailedUpload(ctx, err, res, oldID, &resp.Diagnostics)
		}
	} else {
		current, err = r.client.GetDeployment(opCtx, oldID)
	}
	if err != nil {
		// The old deployment stays live and stays in the state, with its old
		// source digest, so the next plan uploads again.
		resp.Diagnostics.Append(resp.State.Set(ctx, prior)...)
		resp.Diagnostics.Append(deployErrorDiagnostic(err))
		return
	}
	replaced := current.ID != oldID
	// From here the state names the live deployment, whatever fails next.
	r.setState(ctx, next, current, &resp.State, &resp.Diagnostics)
	notifyErr := r.applyNotify(opCtx, req.Config, plan, &prior, current, replaced)
	if notifyErr != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("set the notify target", notifyErr))
	}
	if replaced {
		if err := r.client.DeleteDeployment(opCtx, oldID); err != nil && !client.IsGone(err) {
			resp.Diagnostics.Append(apiErrorDiagnostic(
				fmt.Sprintf("delete the old deployment %s (the new one, %s, is live; delete the old one with `ignitify deployment:delete %s`)", oldID, current.ID, oldID), err))
		}
	}
	if notifyErr == nil {
		r.readBack(opCtx, next, current.ID, &resp.State, &resp.Diagnostics)
	}
}

// shouldDropFailedUpload reports whether a deployment that did not become
// ready is deleted after a failed update: only one this upload made (not the
// old one, not one that already existed for the same content). The old READY
// deployment keeps serving. Pure.
func shouldDropFailedUpload(failedID, oldID string, reused bool) bool {
	return failedID != "" && failedID != oldID && !reused
}

// dropFailedUpload deletes the deployment a failed update made, so no live
// deployment is left outside the state, and names it in the diagnostics.
func (r *deploymentResource) dropFailedUpload(ctx context.Context, err error, res uploadResult, oldID string, diags *diag.Diagnostics) {
	var be *buildError
	if !errors.As(err, &be) || !shouldDropFailedUpload(be.Deployment.ID, oldID, res.Reused) {
		return
	}
	failedID := be.Deployment.ID
	// The build deadline may have passed: the delete has its own.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if delErr := r.client.DeleteDeployment(cleanupCtx, failedID); delErr != nil && !client.IsGone(delErr) {
		diags.AddError("Deployment left behind",
			fmt.Sprintf("Deployment %s did not become ready, and deleting it failed: %s\nDelete it with `ignitify deployment:delete %s`. The old deployment %s keeps serving.", failedID, delErr, failedID, oldID))
		return
	}
	diags.AddWarning("Deployment deleted",
		fmt.Sprintf("Deployment %s did not become ready and was deleted. The old deployment %s keeps serving.", failedID, oldID))
}

// withSourceHash is the plan with the source digest the state records. Pure.
func withSourceHash(plan deploymentModel, hash types.String) deploymentModel {
	out := plan
	out.SourceHash = hash
	return out
}

// upload selects and packs source_dir, sends it, and waits until the build
// is READY. It refuses files that changed since the plan (their digest
// differs from the planned one). On a build that did not become READY, the
// error is a *buildError and the result names that deployment.
func (r *deploymentResource) upload(ctx context.Context, plan deploymentModel, timeout time.Duration) (uploadResult, error) {
	none := uploadResult{Hash: types.StringNull()}
	sel := source.Select(plan.SourceDir.ValueString())
	if !sel.OK() {
		return none, fmt.Errorf("the task folder cannot be uploaded:\n%s", strings.Join(sel.Errors, "\n"))
	}
	archive, err := source.Pack(sel.Files)
	if err != nil {
		return none, err
	}
	if len(archive) > source.MaxUploadBytes {
		return none, fmt.Errorf("the archive is %d bytes; the server accepts at most %d", len(archive), source.MaxUploadBytes)
	}
	hash := source.ArchiveDigest(archive)
	if planned := plan.SourceHash; !planned.IsUnknown() && !planned.IsNull() && planned.ValueString() != hash {
		return none, errors.New("the files in source_dir changed after the plan; run the plan again")
	}
	task, err := r.client.GetTask(ctx, plan.Task.ValueString())
	if err != nil {
		return none, fmt.Errorf("read task %s: %w", plan.Task.ValueString(), err)
	}
	created, err := r.client.CreateDeployment(ctx, task.ID, archive)
	if err != nil {
		return none, err
	}
	dep, err := r.waitForBuild(ctx, created.DeploymentID, timeout)
	return uploadResult{Deployment: dep, Hash: types.StringValue(hash), Reused: created.Reused}, err
}

// waitForBuild polls a deployment until its build is READY. A FAILED build,
// an unknown status or the timeout returns a *buildError that names the
// deployment.
func (r *deploymentResource) waitForBuild(ctx context.Context, id string, timeout time.Duration) (client.Deployment, error) {
	last := client.Deployment{ID: id, Status: client.DeploymentBuilding}
	for attempt := 0; ; attempt++ {
		dep, err := r.client.GetDeployment(ctx, id)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return last, &buildError{Deployment: last, err: fmt.Errorf(
				"deployment %s is still building after %s; raise the timeout, or apply again to wait for it", id, timeout)}
		}
		if err != nil {
			return last, &buildError{Deployment: last, err: fmt.Errorf("read deployment %s: %w", id, err)}
		}
		last = dep
		switch buildStateOf(dep.Status) {
		case buildReady:
			return dep, nil
		case buildFailed:
			reason := "the build failed without a recorded reason"
			if dep.FailureReason != nil && *dep.FailureReason != "" {
				reason = *dep.FailureReason
			}
			return dep, &buildError{Deployment: dep, err: fmt.Errorf(
				"deployment %s failed to build: %s. Read its build log with `ignitify deployment:logs %s`", id, reason, id)}
		case buildUnexpected:
			return dep, &buildError{Deployment: dep, err: fmt.Errorf("deployment %s has the unexpected status %s", id, dep.Status)}
		}
		// A wait cut short by the deadline is reported by the next poll.
		_ = r.sleep(ctx, pollWait(attempt))
	}
}

// deployErrorDiagnostic is the error of an upload that did not end READY. Pure.
func deployErrorDiagnostic(err error) diag.Diagnostic {
	var be *buildError
	if errors.As(err, &be) {
		return diag.NewErrorDiagnostic("Ignitify deployment did not become ready", err.Error())
	}
	return apiErrorDiagnostic("deploy", err)
}

// applyNotify brings the deployment's notify target to the configuration.
func (r *deploymentResource) applyNotify(ctx context.Context, config tfsdk.Config, plan deploymentModel, prior *deploymentModel,
	dep client.Deployment, newDeployment bool) error {
	versionChanged := prior != nil && !plan.NotifySecretWOVersion.Equal(prior.NotifySecretWOVersion)
	switch decideNotify(plan.NotifyURL, dep.NotifyURL, newDeployment, versionChanged) {
	case notifyClear:
		return r.client.ClearNotify(ctx, dep.ID)
	case notifySet:
		var secret types.String
		if diags := config.GetAttribute(ctx, path.Root("notify_secret_wo"), &secret); diags.HasError() || secret.IsNull() || secret.IsUnknown() {
			return errors.New("notify_url needs notify_secret_wo, known at apply time")
		}
		return r.client.SetNotify(ctx, dep.ID, plan.NotifyURL.ValueString(), secret.ValueString())
	}
	return nil
}

// readBack reads the deployment after the writes and records it.
func (r *deploymentResource) readBack(ctx context.Context, base deploymentModel, id string, state *tfsdk.State, diags *diag.Diagnostics) {
	found, err := r.client.GetDeployment(ctx, id)
	if err != nil {
		diags.Append(apiErrorDiagnostic("read deployment", err))
		return
	}
	r.setState(ctx, base, found, state, diags)
}

func (r *deploymentResource) setState(ctx context.Context, base deploymentModel, dep client.Deployment, state *tfsdk.State, diags *diag.Diagnostics) {
	next, d := deploymentStateFrom(base, dep)
	diags.Append(d...)
	diags.Append(state.Set(ctx, next)...)
}

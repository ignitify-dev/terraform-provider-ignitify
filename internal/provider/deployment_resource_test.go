// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// fakeNotifySecret is a notify secret of the server's minimum length.
const fakeNotifySecret = "fake-notify-secret-0123456789abcdef"

// writeTaskFolder writes a small python task into dir; code is main.py.
func writeTaskFolder(t *testing.T, dir, slug, code string) {
	t.Helper()
	files := map[string]string{
		"task.yaml":        "schema_version: 1\nslug: " + slug + "\nruntime: python\n",
		"main.py":          code,
		"requirements.txt": "",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newTestDeploymentResource never sleeps between polls.
func newTestDeploymentResource() resource.Resource {
	return &deploymentResource{sleep: func(context.Context, time.Duration) error { return nil }}
}

func deploymentConfig(task, dir string) map[string]tftypes.Value {
	return map[string]tftypes.Value{"task": tfString(task), "source_dir": tfString(dir)}
}

func withNotify(config map[string]tftypes.Value, url string, version int64) map[string]tftypes.Value {
	out := map[string]tftypes.Value{}
	for k, v := range config {
		out[k] = v
	}
	out["notify_url"] = tfString(url)
	out["notify_secret_wo"] = tfString(fakeNotifySecret)
	out["notify_secret_wo_version"] = tfInt(version)
	return out
}

// deploymentSetup is a fake API with one task and its folder.
func deploymentSetup(t *testing.T) (*fakeAPI, *harness, string) {
	t.Helper()
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	dir := t.TempDir()
	writeTaskFolder(t, dir, "report", "def run(p, c):\n    return 1\n")
	return fake, newHarness(t, newTestDeploymentResource, c), dir
}

func TestDeploymentLifecycle(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	fake.app.buildPolls = 2

	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	id := stringAt(t, state, "deployment_id").ValueString()
	if stringAt(t, state, "status").ValueString() != client.DeploymentReady {
		t.Fatal("status must be READY after create")
	}
	if !strings.HasPrefix(stringAt(t, state, "source_hash").ValueString(), "sha256:") || stringAt(t, state, "content_hash").IsNull() {
		t.Fatal("hashes not recorded")
	}
	if !stringAt(t, state, "execution").IsNull() {
		t.Fatal("a config with no execution block reads as null")
	}
	upload := fake.allRequests()[2]
	if upload.method != "POST" || upload.path != "/deployments" || !strings.HasPrefix(upload.contentType, "multipart/form-data") || upload.idempotencyKey == "" {
		t.Fatalf("upload request = %+v", upload)
	}
	members, err := readFakeArchive(fake.app.deployments[id].archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := members["main.py"]; !ok || len(members) != 3 {
		t.Fatalf("archive members = %v", members)
	}

	read, diags := h.read(state)
	mustOK(t, diags)
	if stringAt(t, read, "source_hash") != stringAt(t, state, "source_hash") {
		t.Fatal("a read must keep the source hash")
	}

	mustOK(t, h.delete(read))
	if len(fake.liveDeployments()) != 0 {
		t.Fatal("deployment not deleted")
	}
	mustOK(t, h.delete(read)) // 404 is done
	gone, diags := h.read(read)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the deployment")
	}
}

func TestDeploymentNoChangeNoUpload(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)

	_, plan, diags := h.planUpdate(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	if planString(t, plan, "deployment_id") != stringAt(t, state, "deployment_id") {
		t.Fatal("an unchanged folder must keep deployment_id known")
	}
	before := len(fake.calls())
	_, diags = h.update(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	for _, call := range fake.calls()[before:] {
		if call == "POST /deployments" {
			t.Fatal("an unchanged folder must not upload")
		}
	}
}

func TestDeploymentCodeChangeReplacesTheOldDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	oldID := stringAt(t, state, "deployment_id").ValueString()

	writeTaskFolder(t, dir, "report", "def run(p, c):\n    return 2\n")
	_, plan, diags := h.planUpdate(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	if !planString(t, plan, "deployment_id").IsUnknown() {
		t.Fatal("a code change must make deployment_id unknown in the plan")
	}
	updated, diags := h.update(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	newID := stringAt(t, updated, "deployment_id").ValueString()
	if newID == oldID {
		t.Fatal("a code change must give a new deployment")
	}
	live := fake.liveDeployments()
	if len(live) != 1 || live[0].dep.ID != newID {
		t.Fatalf("live deployments = %d, want only the new one", len(live))
	}
}

func TestDeploymentFailedBuildKeepsTheOldDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)

	fake.app.failBuilds = true
	writeTaskFolder(t, dir, "report", "broken")
	kept, diags := h.update(state, deploymentConfig("report", dir))
	mustFail(t, diags, fakeFailureReason)
	mustFail(t, diags, "deployment:logs")
	if stringAt(t, kept, "deployment_id") != stringAt(t, state, "deployment_id") || stringAt(t, kept, "source_hash") != stringAt(t, state, "source_hash") {
		t.Fatal("a failed build must keep the old deployment and its source hash")
	}
	if fake.deploymentNow(stringAt(t, state, "deployment_id").ValueString()) == nil {
		t.Fatal("the old deployment must stay live")
	}
	_, plan, diags := h.planUpdate(kept, deploymentConfig("report", dir))
	mustOK(t, diags)
	if !planString(t, plan, "deployment_id").IsUnknown() {
		t.Fatal("the next plan must upload again")
	}
}

func TestDeploymentFailedFirstBuildIsRecorded(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	fake.app.failBuilds = true
	state, diags := h.create(deploymentConfig("report", dir))
	mustFail(t, diags, fakeFailureReason)
	if isRemoved(state) || stringAt(t, state, "status").ValueString() != client.DeploymentFailed {
		t.Fatal("the failed deployment must be in the state (tainted), so a destroy deletes it")
	}
}

func TestDeploymentNotReadyUploadsAgain(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	id := stringAt(t, state, "deployment_id").ValueString()
	fake.app.deployments[id].dep.Status = "ARCHIVED"
	read, diags := h.read(state)
	mustOK(t, diags)

	updated, diags := h.update(read, deploymentConfig("report", dir))
	mustOK(t, diags)
	if stringAt(t, updated, "deployment_id").ValueString() != id || stringAt(t, updated, "status").ValueString() != client.DeploymentReady {
		t.Fatal("the same content must be built again on the same deployment")
	}
	if fake.deploymentNow(id) == nil {
		t.Fatal("a rebuild of the same deployment must not delete it")
	}
}

func TestDeploymentRefusedAtPlan(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("//registry.npmjs.org/:_authToken=npm_fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("S=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(fake.calls())
	_, diags := h.create(deploymentConfig("report", dir))
	mustFail(t, diags, ".npmrc holds a registry credential")
	if len(diags.Warnings()) != 1 || !strings.Contains(diags.Warnings()[0].Detail(), "excluded .env") {
		t.Fatalf("warnings = %v", diags.Warnings())
	}
	if len(fake.calls()) != before {
		t.Fatal("a refused folder must make no API call")
	}
}

func TestDeploymentFilesChangedAfterPlan(t *testing.T) {
	_, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	writeTaskFolder(t, dir, "report", "v2")
	cfg, plan, diags := h.planUpdate(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	writeTaskFolder(t, dir, "report", "v3")
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: h.schema, Raw: plan.Raw}}
	h.res.Update(h.ctx, resource.UpdateRequest{Config: cfg, Plan: plan, State: state}, &resp)
	mustFail(t, resp.Diagnostics, "changed after the plan")
}

func TestDeploymentBuildTimeout(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	dir := t.TempDir()
	writeTaskFolder(t, dir, "report", "x")
	fake.app.buildPolls = 1 << 30
	h := newHarness(t, func() resource.Resource {
		return &deploymentResource{sleep: sleepContext}
	}, c)
	config := deploymentConfig("report", dir)
	timeoutsType := h.typ.AttributeTypes["timeouts"].(tftypes.Object)
	config["timeouts"] = tftypes.NewValue(timeoutsType, map[string]tftypes.Value{
		"create": tfString("50ms"),
		"update": tftypes.NewValue(tftypes.String, nil),
	})
	state, diags := h.create(config)
	mustFail(t, diags, "still building after 50ms")
	if isRemoved(state) || stringAt(t, state, "status").ValueString() != client.DeploymentBuilding {
		t.Fatal("the building deployment must be in the state")
	}
}

func TestDeploymentOldDeleteFailureNamesTheOldDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	oldID := stringAt(t, state, "deployment_id").ValueString()
	fake.app.failDeploymentDelete = true
	writeTaskFolder(t, dir, "report", "v2")
	updated, diags := h.update(state, deploymentConfig("report", dir))
	mustFail(t, diags, "delete the old deployment "+oldID)
	if stringAt(t, updated, "deployment_id").ValueString() == oldID {
		t.Fatal("the state must name the new, live deployment")
	}
}

func TestDeploymentNotify(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	const url = "https://hooks.example.test/done"
	state, diags := h.create(withNotify(deploymentConfig("report", dir), url, 1))
	mustOK(t, diags)
	first := fake.deploymentNow(stringAt(t, state, "deployment_id").ValueString())
	if stringAt(t, state, "notify_url").ValueString() != url || first.notifySecret != fakeNotifySecret {
		t.Fatal("notify target not set")
	}
	if !stringAt(t, state, "notify_secret_wo").IsNull() {
		t.Fatal("the notify secret must never be in the state")
	}

	// A code change makes a new deployment: the target is set on it again.
	writeTaskFolder(t, dir, "report", "v2")
	updated, diags := h.update(state, withNotify(deploymentConfig("report", dir), url, 1))
	mustOK(t, diags)
	second := fake.deploymentNow(stringAt(t, updated, "deployment_id").ValueString())
	if second.notifySecret != fakeNotifySecret || second.dep.NotifyURL == nil {
		t.Fatal("the new deployment must get the notify target")
	}

	// A version change sends the secret again, with no upload.
	fake.mu.Lock()
	fake.app.deployments[second.dep.ID].notifySecret = "fake-changed-outside-terraform-0000000"
	fake.mu.Unlock()
	bumped, diags := h.update(updated, withNotify(deploymentConfig("report", dir), url, 2))
	mustOK(t, diags)
	if fake.deploymentNow(stringAt(t, bumped, "deployment_id").ValueString()).notifySecret != fakeNotifySecret {
		t.Fatal("a version change must send the secret again")
	}

	// No notify_url clears it.
	cleared, diags := h.update(bumped, deploymentConfig("report", dir))
	mustOK(t, diags)
	if !stringAt(t, cleared, "notify_url").IsNull() || fake.deploymentNow(stringAt(t, cleared, "deployment_id").ValueString()).dep.NotifyURL != nil {
		t.Fatal("notify target not cleared")
	}
}

func TestDeploymentNotifyValidation(t *testing.T) {
	_, h, dir := deploymentSetup(t)
	validate := func(config map[string]tftypes.Value) []string {
		resp := resource.ValidateConfigResponse{}
		h.res.(resource.ResourceWithValidateConfig).ValidateConfig(h.ctx,
			resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: h.schema, Raw: h.object(config, false)}}, &resp)
		var out []string
		for _, d := range resp.Diagnostics.Errors() {
			out = append(out, d.Summary())
		}
		return out
	}
	urlOnly := deploymentConfig("report", dir)
	urlOnly["notify_url"] = tfString("https://hooks.example.test/done")
	if got := validate(urlOnly); !slices.Equal(got, []string{"Missing notify secret"}) {
		t.Errorf("url only: %v", got)
	}
	short := withNotify(deploymentConfig("report", dir), "https://hooks.example.test/done", 1)
	short["notify_secret_wo"] = tfString("short")
	if got := validate(short); !slices.Equal(got, []string{"Notify secret too short"}) {
		t.Errorf("short secret: %v", got)
	}
	if got := validate(withNotify(deploymentConfig("report", dir), "https://hooks.example.test/done", 1)); got != nil {
		t.Errorf("valid: %v", got)
	}
}

func TestDeploymentImport(t *testing.T) {
	_, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	id := stringAt(t, state, "deployment_id").ValueString()

	imported, diags := h.importAndRead("report")
	mustOK(t, diags)
	if stringAt(t, imported, "deployment_id").ValueString() != id || stringAt(t, imported, "status").ValueString() != client.DeploymentReady {
		t.Fatal("import by slug must take the only live deployment")
	}
	if !stringAt(t, imported, "source_dir").IsNull() {
		t.Fatal("source_dir cannot be read back")
	}

	// A second live deployment (another resource's) makes the slug alone ambiguous.
	second := t.TempDir()
	writeTaskFolder(t, second, "report", "other")
	other := newHarness(t, newTestDeploymentResource, h.res.(*deploymentResource).client)
	if _, diags := other.create(deploymentConfig("report", second)); diags.HasError() {
		t.Fatal(diags)
	}
	_, diags = h.importAndRead("report")
	mustFail(t, diags, "2 live deployments")
	explicit, diags := h.importAndRead("report/" + id)
	mustOK(t, diags)
	if stringAt(t, explicit, "deployment_id").ValueString() != id {
		t.Fatal("import by <slug>/<id> must take that deployment")
	}

	seedTask(t, h.res.(*deploymentResource).client, "other")
	_, diags = h.importAndRead("other/" + id)
	mustFail(t, diags, "belongs to task report")
	_, diags = h.importAndRead("other")
	mustFail(t, diags, "no live deployment")
}

func TestHookSurvivesACodeUpdate(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	hooks := newHarness(t, newHookResource, h.res.(*deploymentResource).client)
	hook, diags := hooks.create(hookConfig("report"))
	mustOK(t, diags)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	writeTaskFolder(t, dir, "report", "v2")
	_, diags = h.update(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	after, diags := hooks.read(hook)
	mustOK(t, diags)
	if isRemoved(after) || stringAt(t, after, "url") != stringAt(t, hook, "url") || len(fake.liveHooks()) != 1 {
		t.Fatal("a code update must keep the hook and its url")
	}
}

func TestPlannedDeployment(t *testing.T) {
	ready := deploymentModel{
		Task: types.StringValue("report"), SourceHash: types.StringValue("sha256:a"),
		DeploymentID: types.StringValue("dep-1"), Status: types.StringValue(client.DeploymentReady),
	}
	same := types.StringValue("sha256:a")
	cases := []struct {
		name    string
		plan    deploymentModel
		prior   *deploymentModel
		hash    types.String
		uploads bool
	}{
		{"create", ready, nil, same, true},
		{"unchanged", ready, &ready, same, false},
		{"files changed", ready, &ready, types.StringValue("sha256:b"), true},
		{"files unknown", ready, &ready, types.StringUnknown(), true},
		{"not ready", ready, &deploymentModel{Task: ready.Task, SourceHash: same, Status: types.StringValue("ARCHIVED")}, same, true},
		{"task change replaces", deploymentModel{Task: types.StringValue("other")}, &ready, same, true},
	}
	for _, tc := range cases {
		got := plannedDeployment(tc.plan, tc.prior, tc.hash)
		if got.DeploymentID.IsUnknown() != tc.uploads || !got.SourceHash.Equal(tc.hash) {
			t.Errorf("%s: deployment_id %v, source_hash %v", tc.name, got.DeploymentID, got.SourceHash)
		}
	}
}

func TestDecideNotify(t *testing.T) {
	url := "https://x.test"
	other := "https://y.test"
	cases := []struct {
		name         string
		desired      types.String
		current      *string
		newDep, bump bool
		want         notifyAction
	}{
		{"none wanted, none set", types.StringNull(), nil, false, false, notifyKeep},
		{"none wanted, one set", types.StringNull(), &url, false, false, notifyClear},
		{"same url", types.StringValue(url), &url, false, false, notifyKeep},
		{"other url", types.StringValue(url), &other, false, false, notifySet},
		{"none set", types.StringValue(url), nil, false, false, notifySet},
		{"new deployment", types.StringValue(url), &url, true, false, notifySet},
		{"version bump", types.StringValue(url), &url, false, true, notifySet},
	}
	for _, tc := range cases {
		if got := decideNotify(tc.desired, tc.current, tc.newDep, tc.bump); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPollWait(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second}
	for attempt, w := range want {
		if got := pollWait(attempt); got != w {
			t.Errorf("attempt %d: %v, want %v", attempt, got, w)
		}
	}
	if pollWait(1000) != pollMaxWait {
		t.Error("a large attempt must cap")
	}
}

func TestExecutionText(t *testing.T) {
	if got := executionText([]byte(`{"duration":"short","cardinality":"one"}`)).ValueString(); got != `{"cardinality":"one","duration":"short"}` {
		t.Errorf("got %s", got)
	}
	for _, raw := range []string{"", "null"} {
		if !executionText([]byte(raw)).IsNull() {
			t.Errorf("%q must be null", raw)
		}
	}
}

func planString(t *testing.T, plan tfsdk.Plan, name string) types.String {
	t.Helper()
	var v types.String
	mustOK(t, plan.GetAttribute(context.Background(), path.Root(name), &v))
	return v
}

func TestDeploymentNotifyRefusedOnCreateDoesNotTaint(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	fake.app.refuseNotify = true
	const url = "https://hooks.example.test/done"
	state, diags := h.create(withNotify(deploymentConfig("report", dir), url, 1))
	mustOK(t, diags)
	if len(diags.Warnings()) != 1 || !strings.Contains(diags.Warnings()[0].Detail(), "may_use_webhooks") {
		t.Fatalf("want one warning naming the refusal, got %v", diags.Warnings())
	}
	id := stringAt(t, state, "deployment_id").ValueString()
	if stringAt(t, state, "status").ValueString() != client.DeploymentReady || fake.deploymentNow(id) == nil {
		t.Fatal("the READY deployment must be kept")
	}

	// The refresh reads the server's truth; the next plan sets only the notify target.
	read, diags := h.read(state)
	mustOK(t, diags)
	if !stringAt(t, read, "notify_url").IsNull() {
		t.Fatal("the refresh must read the unset notify target")
	}
	_, plan, diags := h.planUpdate(read, withNotify(deploymentConfig("report", dir), url, 1))
	mustOK(t, diags)
	if planString(t, plan, "deployment_id").ValueString() != id {
		t.Fatal("a notify retry must not upload")
	}
	// Still refused: an update error, the deployment kept.
	kept, diags := h.update(read, withNotify(deploymentConfig("report", dir), url, 1))
	mustFail(t, diags, "set the notify target")
	if stringAt(t, kept, "deployment_id").ValueString() != id {
		t.Fatal("a refused notify on update must keep the deployment")
	}
	fake.mu.Lock()
	fake.app.refuseNotify = false
	fake.mu.Unlock()
	fixed, diags := h.update(read, withNotify(deploymentConfig("report", dir), url, 1))
	mustOK(t, diags)
	if stringAt(t, fixed, "notify_url").ValueString() != url || fake.deploymentNow(id).notifySecret != fakeNotifySecret {
		t.Fatal("the retry must set the notify target")
	}
}

func TestDeploymentNotifyRefusedAtPlanWithoutWebhooks(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	no := false
	fake.whoami.Policy.MayUseWebhooks = &no
	before := len(fake.calls())
	_, diags := h.create(withNotify(deploymentConfig("report", dir), "https://hooks.example.test/done", 1))
	mustFail(t, diags, "Webhooks not in the tier")
	for _, call := range fake.calls()[before:] {
		if call != "GET /whoami" {
			t.Fatalf("a refused plan made the call %s", call)
		}
	}
	// Without notify the plan does not ask.
	_, diags = h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
}

func TestDeploymentNotifyURLAndSecretChecks(t *testing.T) {
	_, h, dir := deploymentSetup(t)
	validate := func(url, secret string) []string {
		config := withNotify(deploymentConfig("report", dir), url, 1)
		config["notify_secret_wo"] = tfString(secret)
		resp := resource.ValidateConfigResponse{}
		h.res.(resource.ResourceWithValidateConfig).ValidateConfig(h.ctx,
			resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: h.schema, Raw: h.object(config, false)}}, &resp)
		var out []string
		for _, d := range resp.Diagnostics.Errors() {
			out = append(out, d.Summary())
		}
		return out
	}
	if got := validate("http://hooks.example.test/done", fakeNotifySecret); !slices.Equal(got, []string{"Invalid notify URL"}) {
		t.Errorf("http: %v", got)
	}
	if got := validate("not a url", fakeNotifySecret); !slices.Equal(got, []string{"Invalid notify URL"}) {
		t.Errorf("not a url: %v", got)
	}
	// 31 two-byte letters: 62 bytes, but 31 UTF-16 units, as the server counts.
	if got := validate("https://hooks.example.test/done", strings.Repeat("é", 31)); !slices.Equal(got, []string{"Notify secret too short"}) {
		t.Errorf("31 units: %v", got)
	}
	// 16 emoji: 16 runes, but 32 UTF-16 units.
	if got := validate("https://hooks.example.test/done", strings.Repeat("\U0001F600", 16)); got != nil {
		t.Errorf("32 units: %v", got)
	}
}

func TestDeploymentFailedUpdateDeletesTheNewDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	oldID := stringAt(t, state, "deployment_id").ValueString()
	fake.app.failBuilds = true
	writeTaskFolder(t, dir, "report", "broken")
	_, diags = h.update(state, deploymentConfig("report", dir))
	mustFail(t, diags, fakeFailureReason)
	if len(diags.Warnings()) != 1 || !strings.Contains(diags.Warnings()[0].Detail(), "was deleted") {
		t.Fatalf("want a warning naming the deleted deployment, got %v", diags.Warnings())
	}
	live := fake.liveDeployments()
	if len(live) != 1 || live[0].dep.ID != oldID {
		t.Fatalf("live deployments = %d, want only the old one", len(live))
	}
}

func TestDeploymentTimedOutUpdateDeletesTheNewDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	oldID := stringAt(t, state, "deployment_id").ValueString()
	fake.app.buildPolls = 1 << 30
	writeTaskFolder(t, dir, "report", "slow")
	slow := newHarness(t, func() resource.Resource { return &deploymentResource{sleep: sleepContext} }, h.res.(*deploymentResource).client)
	config := deploymentConfig("report", dir)
	timeoutsType := slow.typ.AttributeTypes["timeouts"].(tftypes.Object)
	config["timeouts"] = tftypes.NewValue(timeoutsType, map[string]tftypes.Value{
		"create": tftypes.NewValue(tftypes.String, nil),
		"update": tfString("50ms"),
	})
	kept, diags := slow.update(state, config)
	mustFail(t, diags, "still building after 50ms")
	if stringAt(t, kept, "deployment_id").ValueString() != oldID {
		t.Fatal("the old deployment must stay in the state")
	}
	live := fake.liveDeployments()
	if len(live) != 1 || live[0].dep.ID != oldID {
		t.Fatalf("live deployments = %d, want only the old one", len(live))
	}
}

func TestDeploymentFailedUpdateCleanupFailureNamesTheDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	fake.app.failBuilds = true
	fake.app.failDeploymentDelete = true
	writeTaskFolder(t, dir, "report", "broken")
	_, diags = h.update(state, deploymentConfig("report", dir))
	mustFail(t, diags, "Delete it with `ignitify deployment:delete")
}

func TestShouldDropFailedUpload(t *testing.T) {
	cases := []struct {
		failed, old  string
		reused, want bool
	}{
		{"dep-2", "dep-1", false, true},
		{"dep-2", "dep-1", true, false},
		{"dep-1", "dep-1", false, false},
		{"", "dep-1", false, false},
	}
	for _, tc := range cases {
		if got := shouldDropFailedUpload(tc.failed, tc.old, tc.reused); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

func TestDeploymentExecutionChangeKeepsTheDeployment(t *testing.T) {
	fake, h, dir := deploymentSetup(t)
	withExecution := func(duration string) {
		text := "schema_version: 1\nslug: report\nruntime: python\nexecution:\n  duration: " + duration + "\n"
		if err := os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	withExecution("short")
	state, diags := h.create(deploymentConfig("report", dir))
	mustOK(t, diags)
	id := stringAt(t, state, "deployment_id").ValueString()

	withExecution("long")
	_, plan, diags := h.planUpdate(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	if !planString(t, plan, "deployment_id").IsUnknown() {
		t.Fatal("a changed task config uploads")
	}
	updated, diags := h.update(state, deploymentConfig("report", dir))
	mustOK(t, diags)
	if stringAt(t, updated, "deployment_id").ValueString() != id {
		t.Fatal("a run-settings change must keep the same deployment")
	}
	if !strings.Contains(stringAt(t, updated, "execution").ValueString(), "long") {
		t.Fatalf("execution = %s", stringAt(t, updated, "execution"))
	}
	if fake.deploymentNow(id) == nil {
		t.Fatal("the reused deployment must not be deleted")
	}
}

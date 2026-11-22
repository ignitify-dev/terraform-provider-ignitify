// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

func tfBool(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

// scheduleConfig is a full schedule configuration; over replaces or adds attributes.
func scheduleConfig(over map[string]tftypes.Value) map[string]tftypes.Value {
	config := map[string]tftypes.Value{
		"task":     tfString("report"),
		"cron":     tfString("0  3 * * *"),
		"timezone": tfString("europe/berlin"),
		"payload":  tfString(`{"day":"yesterday"}`),
		"overlap":  tfString("skip"),
		"paused":   tfBool(false),
	}
	for name, v := range over {
		config[name] = v
	}
	return config
}

func boolAt(t *testing.T, state tfsdk.State, name string) types.Bool {
	t.Helper()
	var v types.Bool
	mustOK(t, state.GetAttribute(context.Background(), path.Root(name), &v))
	return v
}

// sameAsConfig fails unless every configured attribute holds its configured
// value in the state: then a plan of the same configuration has no change.
func sameAsConfig(t *testing.T, state tfsdk.State, config map[string]tftypes.Value) {
	t.Helper()
	attrs := map[string]tftypes.Value{}
	if err := state.Raw.As(&attrs); err != nil {
		t.Fatal(err)
	}
	for name, want := range config {
		if !attrs[name].Equal(want) {
			t.Fatalf("%s = %v in the state, %v in the configuration: the plan shows a change", name, attrs[name], want)
		}
	}
}

func hasWarning(diags diag.Diagnostics, summary string) bool {
	for _, d := range diags.Warnings() {
		if d.Summary() == summary {
			return true
		}
	}
	return false
}

// newScheduleHarness is a fake API with the task `report` and a schedule harness.
func newScheduleHarness(t *testing.T) (*fakeAPI, *client.Client, *harness) {
	t.Helper()
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	return fake, c, newHarness(t, newScheduleResource, c)
}

func TestScheduleEchoFormsGiveNoChangeAndNoWrite(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	if live := fake.scheduleNow(id); live.timezone != "Europe/Berlin" || live.cron != "0  3 * * *" {
		t.Fatalf("the fake must store the server forms, got %q %q", live.timezone, live.cron)
	}
	read, diags := h.read(state)
	mustOK(t, diags)
	read, diags = h.read(read)
	mustOK(t, diags)
	sameAsConfig(t, read, config)
	if got := stringAt(t, read, "payload_sha256").ValueString(); got != fakePayloadDigest(map[string]any{"day": "yesterday"}) {
		t.Fatalf("payload_sha256 = %q", got)
	}
	if writes := fake.scheduleWrites(); len(writes) != 1 || writes[0].method != http.MethodPost {
		t.Fatalf("writes = %v, want only the create", writes)
	}
	// An apply with nothing to change sends no edit (the server refuses an empty one).
	updated, diags := h.update(read, config)
	mustOK(t, diags)
	sameAsConfig(t, updated, config)
	if n := len(fake.scheduleWrites()); n != 1 {
		t.Fatalf("an unchanged apply wrote %d times", n-1)
	}
}

func TestScheduleZoneChangedOutsideIsAChangeThatTheApplyUndoes(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	fake.editSchedule(id, func(s *fakeSchedule) { s.timezone = "America/New_York" })

	read, diags := h.read(state)
	mustOK(t, diags)
	if got := stringAt(t, read, "timezone").ValueString(); got != "America/New_York" {
		t.Fatalf("timezone = %q, want the server's new value", got)
	}
	if got := stringAt(t, read, "cron").ValueString(); got != "0  3 * * *" {
		t.Fatalf("cron = %q: an unchanged echo keeps the configured form", got)
	}
	updated, diags := h.update(read, config)
	mustOK(t, diags)
	writes := fake.scheduleWrites()
	if last := writes[len(writes)-1]; last.method != http.MethodPatch || len(last.body) != 1 || last.body["timezone"] != "europe/berlin" {
		t.Fatalf("edit = %v, want only the configured zone", last)
	}
	again, diags := h.read(updated)
	mustOK(t, diags)
	sameAsConfig(t, again, config)
}

func TestSchedulePayloadChangedOutsideIsAChange(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	// The same value read back in another key order is no change.
	fake.editSchedule(id, func(s *fakeSchedule) { s.template["payload"] = map[string]any{"day": "yesterday"} })
	read, diags := h.read(state)
	mustOK(t, diags)
	sameAsConfig(t, read, config)

	fake.editSchedule(id, func(s *fakeSchedule) { s.template["payload"] = map[string]any{"day": "today"} })
	read, diags = h.read(read)
	mustOK(t, diags)
	if !stringAt(t, read, "payload").IsNull() {
		t.Fatal("a payload changed outside Terraform must leave the state payload null, so the plan sends it")
	}
	updated, diags := h.update(read, config)
	mustOK(t, diags)
	if got := fake.scheduleNow(id).template["payload"]; !jsonEqual(t, got, map[string]any{"day": "yesterday"}) {
		t.Fatalf("payload = %v after the apply", got)
	}
	again, diags := h.read(updated)
	mustOK(t, diags)
	sameAsConfig(t, again, config)
}

// An edit made outside Terraform between the plan and the apply of a saved
// plan reaches the apply's answer. The apply must not record it as its own:
// the next plan still shows it.
func TestScheduleOutsideEditDuringASavedPlanStillShows(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	state, diags := h.create(scheduleConfig(nil))
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	read, diags := h.read(state)
	mustOK(t, diags)

	cronOnly := scheduleConfig(map[string]tftypes.Value{"cron": tfString("30 * * * *")})
	cfg, plan, diags := h.planUpdate(read, cronOnly)
	mustOK(t, diags)
	fake.editSchedule(id, func(s *fakeSchedule) {
		s.timezone = "America/New_York"
		s.template["payload"] = map[string]any{"day": "today"}
	})
	applied, diags := h.applyPlan(read, cfg, plan, diags)
	mustOK(t, diags)
	writes := fake.scheduleWrites()
	if last := writes[len(writes)-1]; len(last.body) != 1 || last.body["cron"] != "30 * * * *" {
		t.Fatalf("edit = %v, want only the cron", last.body)
	}

	next, diags := h.read(applied)
	mustOK(t, diags)
	if got := stringAt(t, next, "timezone").ValueString(); got != "America/New_York" {
		t.Fatalf("timezone = %q: the outside zone change must show in the next plan", got)
	}
	if !stringAt(t, next, "payload").IsNull() {
		t.Fatal("the outside payload change must show in the next plan")
	}
	if got := stringAt(t, next, "cron").ValueString(); got != "30 * * * *" {
		t.Fatalf("cron = %q: the field the apply sent keeps its configured form", got)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestScheduleAutoPausedIsNoChangeAndWarns(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	reason := "deployment_missing"
	fake.editSchedule(id, func(s *fakeSchedule) { s.status, s.pauseReason = client.ScheduleAutoPaused, &reason })

	read, diags := h.read(state)
	mustOK(t, diags)
	sameAsConfig(t, read, config)
	if stringAt(t, read, "status").ValueString() != client.ScheduleAutoPaused || stringAt(t, read, "pause_reason").ValueString() != reason {
		t.Fatal("status and pause_reason must show the platform's pause")
	}
	_, _, diags = h.planUpdate(read, config)
	mustOK(t, diags)
	if !hasWarning(diags, autoPausedSummary) {
		t.Fatalf("want the auto-pause warning, got %v", diags)
	}
	for _, d := range diags.Warnings() {
		if !strings.Contains(d.Detail(), id) || !strings.Contains(d.Detail(), reason) || !strings.Contains(d.Detail(), "schedule:resume") {
			t.Fatalf("warning detail %q", d.Detail())
		}
	}
	if fake.scheduleNow(id).status != client.ScheduleAutoPaused {
		t.Fatal("a plan must not resume the schedule")
	}
}

// A plan without recurring schedules refuses the resume, so its warning names
// the plan change first; any other pause keeps the resume hint.
func TestScheduleAutoPausedForNoSchedulesSaysChangePlan(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	reason, detail := client.SchedulePauseReasonPlanLimit, client.SchedulePauseDetailNoSchedules
	fake.editSchedule(id, func(s *fakeSchedule) {
		s.status, s.pauseReason, s.pauseDetail = client.ScheduleAutoPaused, &reason, &detail
	})

	read, diags := h.read(state)
	mustOK(t, diags)
	if stringAt(t, read, "pause_detail").ValueString() != detail {
		t.Fatal("pause_detail must show the plan field of the pause")
	}
	_, _, diags = h.planUpdate(read, config)
	mustOK(t, diags)
	warnings := diags.Warnings()
	if len(warnings) != 1 || warnings[0].Summary() != autoPausedSummary {
		t.Fatalf("want one auto-pause warning, got %v", diags)
	}
	text := warnings[0].Detail()
	for _, want := range []string{id, "not included in your plan", "change plan first", "schedule:resume"} {
		if !strings.Contains(text, want) {
			t.Fatalf("warning detail %q must contain %q", text, want)
		}
	}

	other := "max_storage_mib"
	for _, d := range []*string{nil, &other} {
		m := scheduleModel{
			ScheduleID: types.StringValue(id), Status: types.StringValue(client.ScheduleAutoPaused),
			PauseReason: types.StringValue(reason), PauseDetail: types.StringPointerValue(d),
		}
		if got := autoPausedDetail(m); strings.Contains(got, "change plan") || !strings.Contains(got, "fix the cause") {
			t.Fatalf("a plan-limit pause for another field keeps the resume hint, got %q", got)
		}
	}
}

// The fake answers as the server does where the provider's rules depend on it.
func TestFakeScheduleAnswersAsTheServer(t *testing.T) {
	if got := string(jsonbOrder(map[string]any{"a_long_key": 2, "zz": map[string]any{"bb": 1, "a": 2}})); got != `{"zz":{"a":2,"bb":1},"a_long_key":2}` {
		t.Fatalf("jsonb order = %s", got)
	}
	_, c, h := newScheduleHarness(t)
	state, diags := h.create(scheduleConfig(nil))
	mustOK(t, diags)
	_, err := c.PatchSchedule(t.Context(), stringAt(t, state, "schedule_id").ValueString(), client.ScheduleEdit{"overlap": nil})
	if client.FieldOf(err) != "overlap" {
		t.Fatalf("overlap: null must be a 400 on overlap, got %v", err)
	}
}

func TestSchedulePausedOutsideIsAChangeAndTheApplyResumes(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	id := stringAt(t, state, "schedule_id").ValueString()
	fake.editSchedule(id, func(s *fakeSchedule) { s.status = client.SchedulePaused })

	read, diags := h.read(state)
	mustOK(t, diags)
	if !boolAt(t, read, "paused").ValueBool() {
		t.Fatal("an outside pause must read as paused = true")
	}
	updated, diags := h.update(read, config)
	mustOK(t, diags)
	if fake.scheduleNow(id).status != client.ScheduleActive || stringAt(t, updated, "status").ValueString() != client.ScheduleActive {
		t.Fatal("the apply must resume the schedule")
	}
}

func TestScheduleCreatedPausedSendsOnePause(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	state, diags := h.create(scheduleConfig(map[string]tftypes.Value{"paused": tfBool(true)}))
	mustOK(t, diags)
	writes := fake.scheduleWrites()
	if len(writes) != 2 || writes[0].method != http.MethodPost || writes[1].method != http.MethodPatch || writes[1].body["status"] != client.SchedulePaused {
		t.Fatalf("writes = %v", writes)
	}
	if _, ok := writes[0].body["status"]; ok {
		t.Fatal("a create never sends status")
	}
	if stringAt(t, state, "status").ValueString() != client.SchedulePaused {
		t.Fatal("status must be paused")
	}
}

func TestScheduleRemovedRunFieldIsSentAsNull(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	withPreset := scheduleConfig(map[string]tftypes.Value{
		"preset": tfString("small"), "timeout_sec": tfInt(60), "execution": tfString(`{"a_long_key": 2, "zz": 1}`),
	})
	state, diags := h.create(withPreset)
	mustOK(t, diags)
	read, diags := h.read(state)
	mustOK(t, diags)
	sameAsConfig(t, read, withPreset)

	without := scheduleConfig(map[string]tftypes.Value{"timeout_sec": tfInt(60), "execution": tfString(`{"a_long_key": 2, "zz": 1}`)})
	_, diags = h.update(read, without)
	mustOK(t, diags)
	writes := fake.scheduleWrites()
	last := writes[len(writes)-1]
	if v, ok := last.body["preset"]; !ok || v != nil || len(last.body) != 1 {
		t.Fatalf("edit = %v, want only preset: null", last.body)
	}
	if _, ok := fake.scheduleNow(stringAt(t, state, "schedule_id").ValueString()).template["preset"]; ok {
		t.Fatal("the server must have removed the preset")
	}
}

func TestScheduleImport(t *testing.T) {
	fake, c, h := newScheduleHarness(t)
	seedTask(t, c, "other")
	created, err := c.CreateSchedule(t.Context(), client.ScheduleCreate{
		Task: "report", Cron: "@daily", Timezone: "UTC", Payload: json.RawMessage(`{"a":1}`), Overlap: "allow",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, diags := h.importAndRead("report/" + created.ScheduleID)
	mustOK(t, diags)
	if !stringAt(t, state, "payload").IsNull() || stringAt(t, state, "cron").ValueString() != "@daily" ||
		stringAt(t, state, "overlap").ValueString() != "allow" || stringAt(t, state, "payload_sha256").IsNull() {
		t.Fatal("an import takes the server's values and leaves the payload empty")
	}
	config := scheduleConfig(map[string]tftypes.Value{"cron": tfString("@daily"), "timezone": tfString("UTC"), "overlap": tfString("allow"), "payload": tfString(`{"a":1}`)})
	updated, diags := h.update(state, config)
	mustOK(t, diags)
	writes := fake.scheduleWrites()
	if last := writes[len(writes)-1]; len(last.body) != 1 || last.body["payload"] == nil {
		t.Fatalf("the first apply after an import sends only the payload, got %v", last.body)
	}
	again, diags := h.read(updated)
	mustOK(t, diags)
	sameAsConfig(t, again, config)

	_, diags = h.importAndRead("other/" + created.ScheduleID)
	mustFail(t, diags, "does not belong to task other")
	_, diags = h.importAndRead(created.ScheduleID)
	mustFail(t, diags, "Invalid import id")
}

func TestScheduleRefusalNamesTheAttribute(t *testing.T) {
	_, _, h := newScheduleHarness(t)
	_, diags := h.create(scheduleConfig(map[string]tftypes.Value{"timezone": tfString("Mars/Olympus")}))
	mustFail(t, diags, "unknown time zone")
	for _, d := range diags.Errors() {
		withPath, ok := d.(diag.DiagnosticWithPath)
		if !ok || !withPath.Path().Equal(path.Root("timezone")) {
			t.Fatalf("diagnostic %v does not point at timezone", d)
		}
	}
}

func TestScheduleQuotaFailsWithTheServerText(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	fake.app.scheduleQuota = 1
	_, diags := h.create(scheduleConfig(nil))
	mustOK(t, diags)
	_, diags = h.create(scheduleConfig(nil))
	mustFail(t, diags, "schedule quota exceeded")
	mustFail(t, diags, "schedule-quota")
}

func TestScheduleOfADeletedTaskLeavesTheState(t *testing.T) {
	fake, c, h := newScheduleHarness(t)
	state, diags := h.create(scheduleConfig(nil))
	mustOK(t, diags)
	if err := c.DeleteTask(t.Context(), "report"); err != nil {
		t.Fatal(err)
	}
	gone, diags := h.read(state)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a schedule deleted with its task must leave the state")
	}
	mustOK(t, h.delete(state)) // 404 is done
	if fake.scheduleNow(stringAt(t, state, "schedule_id").ValueString()) != nil {
		t.Fatal("schedule still live")
	}
}

func TestScheduleWithoutADigestKeepsThePayloadAndWarns(t *testing.T) {
	fake, _, h := newScheduleHarness(t)
	fake.app.omitScheduleDigest = true
	config := scheduleConfig(nil)
	state, diags := h.create(config)
	mustOK(t, diags)
	read, diags := h.read(state)
	mustOK(t, diags)
	sameAsConfig(t, read, config)
	if !hasWarning(diags, missingDigestSummary) {
		t.Fatalf("want the missing-digest warning, got %v", diags)
	}
}

func TestKeepConfiguredEcho(t *testing.T) {
	berlin := "Europe/Berlin"
	cases := []struct {
		name   string
		state  types.String
		server string
		last   *string
		want   types.String
	}{
		{"server unchanged keeps the configured form", types.StringValue("europe/berlin"), berlin, &berlin, types.StringValue("europe/berlin")},
		{"server changed takes the server form", types.StringValue("europe/berlin"), "America/New_York", &berlin, types.StringValue("America/New_York")},
		{"no last answer (import) takes the server form", types.StringNull(), berlin, nil, types.StringValue(berlin)},
		{"no last answer with a state value takes the server form", types.StringValue("europe/berlin"), berlin, nil, types.StringValue(berlin)},
		{"none on the server is null", types.StringNull(), "", nil, types.StringNull()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepConfiguredEcho(tc.state, tc.server, tc.last); !got.Equal(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScheduleEditSendsOnlyChanges(t *testing.T) {
	base := scheduleModel{
		Cron: types.StringValue("0 3 * * *"), Timezone: types.StringValue("UTC"), Payload: types.StringValue(`{}`),
		Preset: types.StringValue("small"), TimeoutSec: types.Int64Value(60), DeadlineSec: types.Int64Null(),
		Execution: types.StringNull(), Overlap: types.StringValue("skip"), Paused: types.BoolValue(false),
	}
	if edit := scheduleEdit(base, base); len(edit) != 0 {
		t.Fatalf("no change gives an empty edit, got %v", edit)
	}
	plan := base
	plan.Preset, plan.DeadlineSec, plan.Paused, plan.Payload = types.StringNull(), types.Int64Value(30), types.BoolValue(true), types.StringValue(`{"a":1}`)
	edit := scheduleEdit(plan, base)
	keys := make([]string, 0, len(edit))
	for k := range edit {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"deadline_sec", "payload", "preset", "status"}) || edit["preset"] != nil ||
		edit["status"] != client.SchedulePaused || edit["deadline_sec"] != int64(30) {
		t.Fatalf("edit = %v", edit)
	}
	if base.Preset.ValueString() != "small" {
		t.Fatal("scheduleEdit must not change its input")
	}
}

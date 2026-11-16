// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

func groupConfig(name string, permissions, members []string) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name":        tfString(name),
		"permissions": tfStringSet(permissions...),
		"members":     tfStringSet(members...),
	}
}

func seedUsers(t *testing.T, c *client.Client, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := c.CreateUser(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGroupCreateWithMembers(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newGroupResource, c)
	seedUsers(t, c, "alice", "bob")

	state, diags := h.create(groupConfig("deployers", []string{"tasks.write", "tasks.read"}, []string{"bob", "alice"}))
	mustOK(t, diags)
	if got := setAt(t, state, "permissions"); !slices.Equal(got, []string{"tasks.read", "tasks.write"}) {
		t.Fatalf("permissions = %v", got)
	}
	if got := setAt(t, state, "members"); !slices.Equal(got, []string{"alice", "bob"}) {
		t.Fatalf("members = %v", got)
	}
	want := []string{"POST /groups", "PUT /groups/deployers/users/alice", "PUT /groups/deployers/users/bob"}
	if got := fake.calls()[2:]; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestGroupCreateUnknownMemberLeavesTaintedState(t *testing.T) {
	_, c := newFakeAPI(t)
	h := newHarness(t, newGroupResource, c)
	state, diags := h.create(groupConfig("ops", []string{"runs.read"}, []string{"ghost"}))
	mustFail(t, diags, "user-not-found")
	if isRemoved(state) || stringAt(t, state, "name").ValueString() != "ops" {
		t.Fatal("the created group must stay in the state when a member write fails")
	}
	if got := setAt(t, state, "members"); len(got) != 0 {
		t.Fatalf("members = %v, want none", got)
	}
}

func TestGroupUpdateDiffsBothSets(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newGroupResource, c)
	seedUsers(t, c, "alice", "bob", "carol")
	state, diags := h.create(groupConfig("ops", []string{"runs.read", "tasks.read"}, []string{"alice", "bob"}))
	mustOK(t, diags)
	before := len(fake.calls())

	state, diags = h.update(state, groupConfig("ops", []string{"tasks.read", "usage.read"}, []string{"bob", "carol"}))
	mustOK(t, diags)
	want := []string{
		"PUT /groups/ops/permissions/usage.read",
		"DELETE /groups/ops/permissions/runs.read",
		"PUT /groups/ops/users/carol",
		"DELETE /groups/ops/users/alice",
	}
	if got := fake.calls()[before:]; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got := setAt(t, state, "permissions"); !slices.Equal(got, []string{"tasks.read", "usage.read"}) {
		t.Fatalf("permissions = %v", got)
	}
	if got := setAt(t, state, "members"); !slices.Equal(got, []string{"bob", "carol"}) {
		t.Fatalf("members = %v", got)
	}
}

func TestGroupUpdateRemovedUserIsDone(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newGroupResource, c)
	seedUsers(t, c, "alice")
	state, diags := h.create(groupConfig("ops", []string{"runs.read"}, []string{"alice"}))
	mustOK(t, diags)
	delete(fake.users, "alice") // the user is gone, the stale state still lists it
	state, diags = h.update(state, groupConfig("ops", []string{"runs.read"}, nil))
	mustOK(t, diags)
	if got := setAt(t, state, "members"); len(got) != 0 {
		t.Fatalf("members = %v", got)
	}
}

func TestGroupReadShowsDriftAndGone(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newGroupResource, c)
	seedUsers(t, c, "alice")
	state, diags := h.create(groupConfig("ops", []string{"runs.read"}, nil))
	mustOK(t, diags)
	if _, err := c.AddGroupUser(h.ctx, "ops", "alice"); err != nil {
		t.Fatal(err)
	}
	state, diags = h.read(state)
	mustOK(t, diags)
	if got := setAt(t, state, "members"); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("drift not read: members = %v", got)
	}
	delete(fake.groups, "ops")
	gone, diags := h.read(state)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the group")
	}
	mustOK(t, h.delete(state)) // 404 on delete is done
}

func TestGroupImport(t *testing.T) {
	_, c := newFakeAPI(t)
	h := newHarness(t, newGroupResource, c)
	if _, err := c.CreateGroup(h.ctx, "readers", []string{"tasks.read"}); err != nil {
		t.Fatal(err)
	}
	state, diags := h.importAndRead("readers")
	mustOK(t, diags)
	if got := setAt(t, state, "permissions"); !slices.Equal(got, []string{"tasks.read"}) {
		t.Fatalf("permissions = %v", got)
	}
}

func TestGroupOwnerRequired(t *testing.T) {
	fake, c := newFakeAPI(t)
	fake.userKey = true
	h := newHarness(t, newGroupResource, c)
	_, diags := h.create(groupConfig("ops", []string{"runs.read"}, nil))
	mustFail(t, diags, "owner key required")
}

func TestPlanGroupOpsGrantsBeforeRevokes(t *testing.T) {
	have := groupDesired{permissions: []string{"a"}, members: []string{"x"}}
	want := groupDesired{permissions: []string{"b"}, members: []string{"x"}}
	ops := planGroupOps(have, want)
	if len(ops) != 2 || ops[0] != (groupOp{kind: opGrantPermission, item: "b"}) || ops[1] != (groupOp{kind: opRevokePermission, item: "a"}) {
		t.Fatalf("ops = %+v", ops)
	}
	if len(planGroupOps(have, have)) != 0 {
		t.Fatal("no change must give no writes")
	}
	if !slices.Equal(have.permissions, []string{"a"}) {
		t.Fatal("input mutated")
	}
}

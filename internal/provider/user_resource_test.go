// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

func userConfig(name string) map[string]tftypes.Value {
	return map[string]tftypes.Value{"name": tfString(name)}
}

func TestUserLifecycle(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newUserResource, c)

	state, diags := h.create(userConfig("alice"))
	mustOK(t, diags)
	if got := stringAt(t, state, "name").ValueString(); got != "alice" {
		t.Fatalf("name = %q", got)
	}
	if stringAt(t, state, "resource_id").ValueString() == "" || stringAt(t, state, "created_at").ValueString() != fakeTime {
		t.Fatal("computed values missing after create")
	}
	create := fake.lastRequest()
	if create.method != "POST" || create.path != "/users" || create.body["name"] != "alice" {
		t.Fatalf("create request = %+v", create)
	}
	if !strings.HasPrefix(create.idempotencyKey, "tf-") {
		t.Fatalf("create sent no Idempotency-Key: %q", create.idempotencyKey)
	}
	if create.userAgent != userAgentPrefix+"test" {
		t.Fatalf("User-Agent = %q", create.userAgent)
	}

	read, diags := h.read(state)
	mustOK(t, diags)
	if stringAt(t, read, "resource_id") != stringAt(t, state, "resource_id") {
		t.Fatal("read changed the resource id")
	}

	mustOK(t, h.delete(read))
	if _, ok := fake.users["alice"]; ok {
		t.Fatal("user not deleted")
	}
	// A second delete meets a 404 and is done.
	mustOK(t, h.delete(read))

	gone, diags := h.read(read)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the user from the state")
	}
}

func TestUserImport(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newUserResource, c)
	if _, err := c.CreateUser(h.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	state, diags := h.importAndRead("bob")
	mustOK(t, diags)
	if stringAt(t, state, "resource_id").ValueString() != fake.users["bob"].ResourceID {
		t.Fatal("import did not read the user")
	}
	missing, diags := h.importAndRead("nobody")
	mustOK(t, diags)
	if !isRemoved(missing) {
		t.Fatal("importing a missing user must find nothing")
	}
}

func TestUserNameTaken(t *testing.T) {
	_, c := newFakeAPI(t)
	h := newHarness(t, newUserResource, c)
	_, diags := h.create(userConfig("carol"))
	mustOK(t, diags)
	_, diags = h.create(userConfig("carol"))
	mustFail(t, diags, "Import it")
}

func TestUserOwnerRequired(t *testing.T) {
	fake, c := newFakeAPI(t)
	fake.userKey = true
	h := newHarness(t, newUserResource, c)
	_, diags := h.create(userConfig("dave"))
	mustFail(t, diags, "owner key required")
	// A 403 is not retried.
	if n := len(fake.calls()); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestUserDeleteRevokesFromGroups(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newUserResource, c)
	state, diags := h.create(userConfig("erin"))
	mustOK(t, diags)
	if _, err := c.CreateGroup(h.ctx, "ops", []string{"tasks.read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddGroupUser(h.ctx, "ops", "erin"); err != nil {
		t.Fatal(err)
	}
	mustOK(t, h.delete(state))
	if slices.Contains(fake.groups["ops"].Users, "erin") {
		t.Fatal("fake did not drop the member")
	}
}

func TestUserReadWrong404KeepsState(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newUserResource, c)
	state, diags := h.create(userConfig("frank"))
	mustOK(t, diags)

	fake.override = htmlNotFound
	_, diags = h.read(state)
	mustFail(t, diags, "HTTP 404")

	// A JSON 404 with another route's reason is not "this user is gone" either.
	fake.override = func(w http.ResponseWriter, _ *http.Request) {
		refuse(w, http.StatusNotFound, client.ReasonGroupNotFound, "no group")
	}
	_, diags = h.read(state)
	mustFail(t, diags, "group-not-found")
	mustFail(t, h.delete(state), "group-not-found")
}

// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

func keyConfig(user, label string) map[string]tftypes.Value {
	return map[string]tftypes.Value{"user": tfString(user), "label": tfString(label)}
}

func TestAPIKeyLifecycle(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedUsers(t, c, "ci")
	h := newHarness(t, newAPIKeyResource, c)

	state, diags := h.create(keyConfig("ci", "deploy-bot"))
	mustOK(t, diags)
	id := stringAt(t, state, "resource_id").ValueString()
	if raw := stringAt(t, state, "raw_key").ValueString(); raw != "fake-raw-"+id {
		t.Fatalf("raw_key = %q", raw)
	}
	if stringAt(t, state, "created_at").ValueString() != fakeTime || stringAt(t, state, "label").ValueString() != "deploy-bot" {
		t.Fatal("key fields not read back")
	}
	for _, r := range fake.allRequests() {
		if r.method == "POST" && r.path == "/keys" && r.idempotencyKey == "" {
			t.Fatal("key create sent no Idempotency-Key")
		}
	}

	read, diags := h.read(state)
	mustOK(t, diags)
	if stringAt(t, read, "raw_key").ValueString() != "fake-raw-"+id {
		t.Fatal("a read must keep the raw key in the state")
	}

	mustOK(t, h.delete(read))
	if _, ok := fake.keys[id]; ok {
		t.Fatal("key not revoked")
	}
	mustOK(t, h.delete(read)) // 404 is done
	gone, diags := h.read(read)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the key")
	}
}

func TestAPIKeyReplayWithoutRawKeyRevokesAndCreatesAgain(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedUsers(t, c, "ci")
	fake.redactKeyCreates = 1
	h := newHarness(t, newAPIKeyResource, c)

	state, diags := h.create(keyConfig("ci", "bot"))
	mustOK(t, diags)
	want := []string{"POST /keys", "DELETE /keys/key_0002", "POST /keys", "GET /keys/key_0003"}
	if got := fake.calls()[1:]; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if stringAt(t, state, "resource_id").ValueString() != "key_0003" || stringAt(t, state, "raw_key").ValueString() != "fake-raw-key_0003" {
		t.Fatal("state must hold the second key and its raw key")
	}
	if _, ok := fake.keys["key_0002"]; ok {
		t.Fatal("the key without a raw key must be revoked")
	}
	reqs := fake.allRequests()
	if reqs[1].idempotencyKey == reqs[3].idempotencyKey {
		t.Fatal("the second create must use a fresh Idempotency-Key")
	}
}

func TestAPIKeyReplayTwiceKeepsTheKeyTainted(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedUsers(t, c, "ci")
	fake.redactKeyCreates = 2
	h := newHarness(t, newAPIKeyResource, c)
	state, diags := h.create(keyConfig("ci", "bot"))
	mustFail(t, diags, "without its raw key twice")
	assertLeftoverKey(t, state, "key_0003")
	// The next apply destroys the tainted key: it is revoked.
	mustOK(t, h.delete(state))
	if _, ok := fake.keys["key_0003"]; ok {
		t.Fatal("destroying the tainted key must revoke it")
	}
}

func TestAPIKeyRevokeFailureKeepsTheKeyTainted(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedUsers(t, c, "ci")
	fake.redactKeyCreates = 1
	fake.failRevoke = true
	h := newHarness(t, newAPIKeyResource, c)
	state, diags := h.create(keyConfig("ci", "bot"))
	mustFail(t, diags, "revoking it failed")
	assertLeftoverKey(t, state, "key_0002")
	if n := len(fake.keys); n != 1 {
		t.Fatalf("keys = %d, want only the leftover one (no second create)", n)
	}
}

// assertLeftoverKey checks the state a failed create leaves: the key that still
// exists, with no raw key.
func assertLeftoverKey(t *testing.T, state tfsdk.State, id string) {
	t.Helper()
	if isRemoved(state) {
		t.Fatal("the leftover key must be in the state")
	}
	if got := stringAt(t, state, "resource_id").ValueString(); got != id {
		t.Fatalf("resource_id = %q, want %q", got, id)
	}
	if !stringAt(t, state, "raw_key").IsNull() {
		t.Fatal("raw_key must be null")
	}
}

func TestAPIKeyReadForeign404KeepsState(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedUsers(t, c, "ci")
	h := newHarness(t, newAPIKeyResource, c)
	state, diags := h.create(keyConfig("ci", "bot"))
	mustOK(t, diags)
	fake.override = htmlNotFound
	_, diags = h.read(state)
	mustFail(t, diags, "HTTP 404")
	mustFail(t, h.delete(state), "HTTP 404")
}

func TestAPIKeyImport(t *testing.T) {
	_, c := newFakeAPI(t)
	seedUsers(t, c, "ci")
	created, err := c.CreateKey(t.Context(), "ci", "imported")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newAPIKeyResource, c)
	state, diags := h.importAndRead(created.ResourceID)
	mustOK(t, diags)
	if stringAt(t, state, "user").ValueString() != "ci" || stringAt(t, state, "label").ValueString() != "imported" {
		t.Fatal("import did not read user and label")
	}
	if !stringAt(t, state, "raw_key").IsNull() {
		t.Fatal("raw_key must be empty after an import")
	}
}

func TestAPIKeyImportOwnerKeyRefused(t *testing.T) {
	fake, c := newFakeAPI(t)
	fake.keys["key_owner"] = client.AccessKey{ResourceID: "key_owner", CreatedAt: fakeTime}
	h := newHarness(t, newAPIKeyResource, c)
	_, diags := h.importAndRead("key_owner")
	mustFail(t, diags, "owner key")
}

func TestAPIKeyOwnerRequired(t *testing.T) {
	fake, c := newFakeAPI(t)
	fake.userKey = true
	h := newHarness(t, newAPIKeyResource, c)
	_, diags := h.create(keyConfig("ci", "bot"))
	mustFail(t, diags, "owner key required")
}

func TestAPIKeyUnknownUser(t *testing.T) {
	_, c := newFakeAPI(t)
	h := newHarness(t, newAPIKeyResource, c)
	_, diags := h.create(keyConfig("ghost", "bot"))
	mustFail(t, diags, "user-not-found")
}

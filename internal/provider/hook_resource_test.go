// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

func hookConfig(task string) map[string]tftypes.Value {
	return map[string]tftypes.Value{"task": tfString(task)}
}

// seedTask creates a task through the API, outside the resource under test.
func seedTask(t *testing.T, c *client.Client, slug string) client.TaskCreated {
	t.Helper()
	created, err := c.CreateTask(t.Context(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestHookLifecycle(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	h := newHarness(t, newHookResource, c)

	state, diags := h.create(hookConfig("report"))
	mustOK(t, diags)
	id := stringAt(t, state, "resource_id").ValueString()
	hook := fake.app.hooks[id].hook
	if got := stringAt(t, state, "url").ValueString(); got != fake.baseURL+"/hooks/trigger/"+hook.HookID {
		t.Fatalf("url = %q", got)
	}
	if got := stringAt(t, state, "secret").ValueString(); got != "fake-secret-"+hook.HookID {
		t.Fatalf("secret = %q", got)
	}

	read, diags := h.read(state)
	mustOK(t, diags)
	if stringAt(t, read, "secret") != stringAt(t, state, "secret") {
		t.Fatal("a read must keep the secret in the state")
	}

	mustOK(t, h.delete(read))
	if len(fake.liveHooks()) != 0 {
		t.Fatal("hook not deleted")
	}
	mustOK(t, h.delete(read)) // 404 is done
	gone, diags := h.read(read)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the hook")
	}
}

func TestHookReplayWithoutSecretDeletesAndCreatesAgain(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	fake.app.redactHookCreates = 1
	h := newHarness(t, newHookResource, c)

	state, diags := h.create(hookConfig("report"))
	mustOK(t, diags)
	calls := fake.calls()[1:]
	want := []string{"POST /tasks/report/hooks", "DELETE /hooks/hook_0003", "POST /tasks/report/hooks", "GET /hooks/hook_0005"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if stringAt(t, state, "resource_id").ValueString() != "hook_0005" || stringAt(t, state, "secret").IsNull() {
		t.Fatal("state must hold the second hook and its secret")
	}
	if n := len(fake.liveHooks()); n != 1 {
		t.Fatalf("live hooks = %d, want 1", n)
	}
}

func TestHookReplayTwiceKeepsTheHookTainted(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	fake.app.redactHookCreates = 2
	h := newHarness(t, newHookResource, c)
	state, diags := h.create(hookConfig("report"))
	mustFail(t, diags, "without its secret twice")
	if isRemoved(state) || !stringAt(t, state, "secret").IsNull() {
		t.Fatal("the leftover hook must be in the state with no secret")
	}
	mustOK(t, h.delete(state))
	if n := len(fake.liveHooks()); n != 0 {
		t.Fatalf("live hooks = %d, want 0", n)
	}
}

func TestHookImport(t *testing.T) {
	fake, c := newFakeAPI(t)
	seedTask(t, c, "report")
	seedTask(t, c, "other")
	created, err := c.CreateHook(t.Context(), "report")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newHookResource, c)

	state, diags := h.importAndRead("report/" + created.ResourceID)
	mustOK(t, diags)
	if !stringAt(t, state, "secret").IsNull() {
		t.Fatal("secret must be empty after an import")
	}
	if stringAt(t, state, "url").ValueString() != fake.baseURL+"/hooks/trigger/"+created.HookID {
		t.Fatal("import did not set the url")
	}

	_, diags = h.importAndRead("other/" + created.ResourceID)
	mustFail(t, diags, "does not belong to task other")
	_, diags = h.importAndRead(created.ResourceID)
	mustFail(t, diags, "Invalid import id")
}

func TestHookOnUnknownTask(t *testing.T) {
	_, c := newFakeAPI(t)
	h := newHarness(t, newHookResource, c)
	_, diags := h.create(hookConfig("ghost"))
	mustFail(t, diags, taskNotFoundMessage)
}

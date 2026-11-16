// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func taskConfig(slug string) map[string]tftypes.Value {
	return map[string]tftypes.Value{"slug": tfString(slug)}
}

func TestTaskLifecycle(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newTaskResource, c)

	state, diags := h.create(taskConfig("report"))
	mustOK(t, diags)
	id := stringAt(t, state, "task_id").ValueString()
	if id == "" || stringAt(t, state, "resource_id").ValueString() == "" || stringAt(t, state, "created_at").ValueString() != fakeTime {
		t.Fatal("task fields not read back")
	}
	for _, r := range fake.allRequests() {
		if r.method == "POST" && r.idempotencyKey == "" {
			t.Fatal("task create sent no Idempotency-Key")
		}
	}

	read, diags := h.read(state)
	mustOK(t, diags)
	if stringAt(t, read, "task_id").ValueString() != id {
		t.Fatal("read changed the id")
	}

	mustOK(t, h.delete(read))
	if fake.liveTask("report") != nil {
		t.Fatal("task not deleted")
	}
	mustOK(t, h.delete(read)) // 404 is done
	gone, diags := h.read(read)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the task")
	}
}

func TestTaskCreateAfterDestroyIsTheSameTask(t *testing.T) {
	_, c := newFakeAPI(t)
	h := newHarness(t, newTaskResource, c)
	first, diags := h.create(taskConfig("report"))
	mustOK(t, diags)
	mustOK(t, h.delete(first))
	again, diags := h.create(taskConfig("report"))
	mustOK(t, diags)
	if stringAt(t, again, "task_id") != stringAt(t, first, "task_id") {
		t.Fatal("a revived task must keep its id")
	}
}

func TestTaskSlugTaken(t *testing.T) {
	_, c := newFakeAPI(t)
	if _, err := c.CreateTask(t.Context(), "report"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newTaskResource, c)
	state, diags := h.create(taskConfig("report"))
	mustFail(t, diags, "slug already used")
	if !isRemoved(state) {
		t.Fatal("a refused create must leave no state")
	}
}

func TestTaskImport(t *testing.T) {
	_, c := newFakeAPI(t)
	created, err := c.CreateTask(t.Context(), "report")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newTaskResource, c)
	state, diags := h.importAndRead("report")
	mustOK(t, diags)
	if stringAt(t, state, "task_id").ValueString() != created.TaskID {
		t.Fatal("import did not read the task")
	}
	_, diags = h.importAndRead("Not A Slug")
	mustFail(t, diags, "Invalid import id")
}

func TestTaskReadForeign404KeepsState(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newTaskResource, c)
	state, diags := h.create(taskConfig("report"))
	mustOK(t, diags)
	fake.override = htmlNotFound
	_, diags = h.read(state)
	mustFail(t, diags, "HTTP 404")
	mustFail(t, h.delete(state), "HTTP 404")
}

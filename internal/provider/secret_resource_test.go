// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const fakeSecretValue = "fake-secret-value"

func secretConfig(name, value string, version int64) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name":             tfString(name),
		"value_wo":         tfString(value),
		"value_wo_version": tfInt(version),
	}
}

// stateHolds reports whether the raw state holds the text anywhere.
func stateHolds(raw tftypes.Value, text string) bool {
	return strings.Contains(raw.String(), text)
}

func TestSecretLifecycle(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newSecretResource, c)

	state, diags := h.create(secretConfig("DB_PASSWORD", fakeSecretValue, 1))
	mustOK(t, diags)
	if fake.values["DB_PASSWORD"] != fakeSecretValue {
		t.Fatal("value not sent")
	}
	if !stringAt(t, state, "value_wo").IsNull() || stateHolds(state.Raw, fakeSecretValue) {
		t.Fatal("the value must never be in the state")
	}
	if stringAt(t, state, "updated_at").ValueString() == "" {
		t.Fatal("updated_at not read")
	}
	want := []string{"GET /secrets/DB_PASSWORD", "POST /secrets", "GET /secrets/DB_PASSWORD"}
	if got := fake.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if fake.allRequests()[1].idempotencyKey == "" {
		t.Fatal("secret write sent no Idempotency-Key")
	}
	if fake.allRequests()[1].ifNoneMatch != "*" {
		t.Fatal("the create write must be create-only (If-None-Match: *)")
	}

	// A new version sends the value from the configuration again.
	updated, diags := h.update(state, secretConfig("DB_PASSWORD", "fake-rotated", 2))
	mustOK(t, diags)
	if fake.values["DB_PASSWORD"] != "fake-rotated" {
		t.Fatal("update did not send the new value")
	}
	if write := fake.allRequests()[3]; write.method != "POST" || write.ifNoneMatch != "" {
		t.Fatalf("an update write replaces the value and must not be create-only: %+v", write)
	}
	if stateHolds(updated.Raw, "fake-rotated") {
		t.Fatal("the value must never be in the state")
	}
	if stringAt(t, updated, "updated_at") == stringAt(t, state, "updated_at") {
		t.Fatal("updated_at must follow the write")
	}

	mustOK(t, h.delete(updated))
	if _, ok := fake.secrets["DB_PASSWORD"]; ok {
		t.Fatal("secret not deleted")
	}
	mustOK(t, h.delete(updated)) // 404 is done
	gone, diags := h.read(updated)
	mustOK(t, diags)
	if !isRemoved(gone) {
		t.Fatal("a 404 read must remove the secret")
	}
}

func TestSecretCreateRefusesExistingName(t *testing.T) {
	fake, c := newFakeAPI(t)
	if err := c.SetSecret(t.Context(), "API_TOKEN", "fake-old"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newSecretResource, c)
	_, diags := h.create(secretConfig("API_TOKEN", "fake-new", 1))
	mustFail(t, diags, "already exists")
	if fake.values["API_TOKEN"] != "fake-old" {
		t.Fatal("an existing secret must not be overwritten")
	}
}

func TestSecretImport(t *testing.T) {
	_, c := newFakeAPI(t)
	if err := c.SetSecret(t.Context(), "API_TOKEN", "fake-value"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, newSecretResource, c)
	state, diags := h.importAndRead("API_TOKEN")
	mustOK(t, diags)
	if stringAt(t, state, "name").ValueString() != "API_TOKEN" || stringAt(t, state, "updated_at").ValueString() == "" {
		t.Fatal("import did not read the secret")
	}
}

func TestSecretVaultOff(t *testing.T) {
	fake, c := newFakeAPI(t)
	fake.vaultOff = true
	h := newHarness(t, newSecretResource, c)
	_, diags := h.create(secretConfig("API_TOKEN", fakeSecretValue, 1))
	mustFail(t, diags, "secret vault is not enabled")
	// A 503 without Retry-After is not retried.
	if n := len(fake.calls()); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestSecretWrong404IsAnError(t *testing.T) {
	fake, c := newFakeAPI(t)
	h := newHarness(t, newSecretResource, c)
	state, diags := h.create(secretConfig("API_TOKEN", fakeSecretValue, 1))
	mustOK(t, diags)
	fake.override = htmlNotFound
	_, diags = h.read(state)
	mustFail(t, diags, "HTTP 404")
	// The create pre-check must not read a foreign 404 as "free name" either.
	_, diags = h.create(secretConfig("OTHER", fakeSecretValue, 1))
	mustFail(t, diags, "HTTP 404")
	if _, ok := fake.values["OTHER"]; ok {
		t.Fatal("no write after a foreign 404")
	}
}

// TestSecretCreateRaceIsRefused covers a secret created by another writer
// between the read before the create and the write: the create-only write is
// refused and the value is kept.
func TestSecretCreateRaceIsRefused(t *testing.T) {
	fake, c := newFakeAPI(t)
	if err := c.SetSecret(t.Context(), "API_TOKEN", "fake-old"); err != nil {
		t.Fatal(err)
	}
	fake.secretReadMisses = true
	h := newHarness(t, newSecretResource, c)
	_, diags := h.create(secretConfig("API_TOKEN", "fake-new", 1))
	mustFail(t, diags, "already exists")
	mustFail(t, diags, "Import it")
	if fake.values["API_TOKEN"] != "fake-old" {
		t.Fatal("an existing secret must not be overwritten")
	}
}

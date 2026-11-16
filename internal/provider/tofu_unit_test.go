// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// envTerraformPath names the Terraform or OpenTofu binary the testing library runs.
const envTerraformPath = "TF_ACC_TERRAFORM_PATH"

// TestTofuAgainstFakeAPI runs real plans and applies (`make test-tofu`, OpenTofu
// in the test image) against the in-memory fake API. It proves what calling the
// resource methods directly cannot: plan modifiers, defaults, the write-only
// value, an empty plan after apply, replace on a label change, drift, and a
// 404 removing an object. It is skipped when no binary is named, so `make test`
// never downloads one.
func TestTofuAgainstFakeAPI(t *testing.T) {
	if os.Getenv(envTerraformPath) == "" {
		t.Skipf("%s is not set; run `make test-tofu`", envTerraformPath)
	}
	fake, c := newFakeAPI(t)
	config := func(label string) string {
		return fmt.Sprintf(`
provider "ignitify" {
  endpoint = %q
  api_key  = "fake-test-key"
}

resource "ignitify_user" "u" {
  name = "alice"
}

resource "ignitify_group" "g" {
  name        = "ops"
  permissions = ["tasks.read"]
  members     = [ignitify_user.u.name]
}

resource "ignitify_api_key" "k" {
  user  = ignitify_user.u.name
  label = %q
}

resource "ignitify_secret" "s" {
  name             = "DB_PASSWORD"
  value_wo         = %q
  value_wo_version = 1
}
`, fake.baseURL, label, fakeSecretValue)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: accProviders,
		Steps: []resource.TestStep{
			{
				// Create; the library then fails the step if a new plan is not empty.
				Config: config("first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ignitify_group.g", "members.#", "1"),
					resource.TestCheckResourceAttrSet("ignitify_api_key.k", "raw_key"),
					resource.TestCheckNoResourceAttr("ignitify_secret.s", "value_wo"),
					resource.TestCheckResourceAttrSet("ignitify_secret.s", "updated_at"),
					func(_ *terraform.State) error {
						fake.mu.Lock()
						defer fake.mu.Unlock()
						if fake.values["DB_PASSWORD"] != fakeSecretValue {
							return fmt.Errorf("the write-only value did not reach the API")
						}
						return nil
					},
				),
			},
			{
				// A label change replaces the key.
				Config: config("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ignitify_api_key.k", plancheck.ResourceActionReplace)},
				},
				Check: resource.TestCheckResourceAttr("ignitify_api_key.k", "label", "second"),
			},
			{
				// A member added outside Terraform is drift that the apply removes.
				PreConfig: func() {
					if _, err := c.CreateUser(context.Background(), "bob"); err != nil {
						t.Fatal(err)
					}
					if _, err := c.AddGroupUser(context.Background(), "ops", "bob"); err != nil {
						t.Fatal(err)
					}
				},
				Config: config("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ignitify_group.g", plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr("ignitify_group.g", "members.#", "1"),
			},
			{
				// A key revoked outside Terraform reads as a 404: it leaves the state and is created again.
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					for id := range fake.keys {
						delete(fake.keys, id)
					}
				},
				Config: config("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ignitify_api_key.k", plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}

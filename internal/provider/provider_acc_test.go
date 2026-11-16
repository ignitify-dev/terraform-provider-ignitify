// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// Acceptance tests run only with TF_ACC=1 (`make testacc`). They create real
// objects in the tenant of IGNITIFY_API_KEY (an owner key) at IGNITIFY_API_URL,
// with random names, and destroy them at the end.

var accProviders = map[string]func() (tfprotov6.ProviderServer, error){
	providerTypeName: providerserver.NewProtocol6WithError(New("acc")()),
}

// accName is a random user or group name (the charset is lower case).
func accName() string {
	return "tfacc-" + acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)
}

func TestAccAccessControl(t *testing.T) {
	user, group := accName(), accName()
	config := func(permissions string) string {
		return fmt.Sprintf(`
resource "ignitify_user" "u" {
  name = %q
}

resource "ignitify_group" "g" {
  name        = %q
  permissions = [%s]
  members     = [ignitify_user.u.name]
}

resource "ignitify_api_key" "k" {
  user  = ignitify_user.u.name
  label = "tfacc"
}

data "ignitify_whoami" "me" {}
`, user, group, permissions)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accProviders,
		Steps: []resource.TestStep{
			{
				Config: config(`"tasks.read"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ignitify_group.g", "members.#", "1"),
					resource.TestCheckResourceAttrSet("ignitify_api_key.k", "raw_key"),
					resource.TestCheckResourceAttr("data.ignitify_whoami.me", "is_owner_key", "true"),
				),
			},
			{
				Config: config(`"tasks.read", "tasks.write"`),
				Check:  resource.TestCheckResourceAttr("ignitify_group.g", "permissions.#", "2"),
			},
			{
				ResourceName:                         "ignitify_user.u",
				ImportState:                          true,
				ImportStateId:                        user,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				ResourceName:                         "ignitify_group.g",
				ImportState:                          true,
				ImportStateId:                        group,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				ResourceName:                         "ignitify_api_key.k",
				ImportState:                          true,
				ImportStateIdFunc:                    resourceIDOf("ignitify_api_key.k"),
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "resource_id",
				ImportStateVerifyIgnore:              []string{"raw_key"},
			},
		},
	})
}

func TestAccSecret(t *testing.T) {
	name := "TFACC_" + strings.ToUpper(acctest.RandStringFromCharSet(8, acctest.CharSetAlpha))
	config := func(value string, version int) string {
		return fmt.Sprintf(`
resource "ignitify_secret" "s" {
  name             = %q
  value_wo         = %q
  value_wo_version = %d
}
`, name, value, version)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: config("tfacc-value-1", 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("ignitify_secret.s", "value_wo"),
					resource.TestCheckResourceAttrSet("ignitify_secret.s", "updated_at"),
				),
			},
			{
				Config: config("tfacc-value-2", 2),
				Check:  resource.TestCheckResourceAttr("ignitify_secret.s", "value_wo_version", "2"),
			},
			{
				ResourceName:                         "ignitify_secret.s",
				ImportState:                          true,
				ImportStateId:                        name,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"value_wo_version"},
			},
		},
	})
}

func resourceIDOf(address string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return "", fmt.Errorf("%s not in the state", address)
		}
		return rs.Primary.Attributes["resource_id"], nil
	}
}

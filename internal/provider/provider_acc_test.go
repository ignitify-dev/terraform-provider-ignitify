// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"path/filepath"
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
// with random names, and destroy them at the end. The deployment test needs a
// tier with webhooks and two free deployment slots.

// accVersion is the provider version the tests serve and name in the User-Agent.
const accVersion = "acc"

var accProviders = map[string]func() (tfprotov6.ProviderServer, error){
	providerTypeName: providerserver.NewProtocol6WithError(New(accVersion)()),
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

// accTaskDir is the tiny task the deployment test uploads. Its config names
// the slug accTaskSlug, which the test rewrites to a random one.
const (
	accTaskDir  = "testdata/acc-task"
	accTaskSlug = "tfacc-task"
)

// copyAccTask copies the test task into a new folder with slug as its slug,
// and version as the VERSION in its code.
func copyAccTask(t *testing.T, dir, slug string, version int) {
	t.Helper()
	entries, err := os.ReadDir(accTaskDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		text, err := os.ReadFile(filepath.Join(accTaskDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out := strings.Replace(string(text), "slug: "+accTaskSlug, "slug: "+slug, 1)
		out = strings.Replace(out, "VERSION = 1", fmt.Sprintf("VERSION = %d", version), 1)
		if err := os.WriteFile(filepath.Join(dir, e.Name()), []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAccTaskDeploymentHook deploys a tiny task, changes its code, and imports
// all three resources. It builds on the platform twice.
func TestAccTaskDeploymentHook(t *testing.T) {
	slug := accName()
	dir := t.TempDir()
	copyAccTask(t, dir, slug, 1)
	var firstDeployment, hookURL string
	config := fmt.Sprintf(`
resource "ignitify_task" "t" {
  slug = %q
}

resource "ignitify_deployment" "d" {
  task       = ignitify_task.t.slug
  source_dir = %q
}

resource "ignitify_hook" "h" {
  task = ignitify_task.t.slug
}
`, slug, dir)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accProviders,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ignitify_deployment.d", "status", "READY"),
					resource.TestCheckResourceAttrSet("ignitify_hook.h", "secret"),
					func(s *terraform.State) error {
						firstDeployment = s.RootModule().Resources["ignitify_deployment.d"].Primary.Attributes["deployment_id"]
						hookURL = s.RootModule().Resources["ignitify_hook.h"].Primary.Attributes["url"]
						return nil
					},
				),
			},
			{
				PreConfig: func() { copyAccTask(t, dir, slug, 2) },
				Config:    config,
				Check: func(s *terraform.State) error {
					attrs := s.RootModule().Resources["ignitify_deployment.d"].Primary.Attributes
					if attrs["deployment_id"] == firstDeployment || attrs["status"] != "READY" {
						return fmt.Errorf("a code change must give a new READY deployment, got %s %s", attrs["deployment_id"], attrs["status"])
					}
					if got := s.RootModule().Resources["ignitify_hook.h"].Primary.Attributes["url"]; got != hookURL {
						return fmt.Errorf("the hook url changed on a code update")
					}
					return nil
				},
			},
			{
				ResourceName:                         "ignitify_task.t",
				ImportState:                          true,
				ImportStateId:                        slug,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "slug",
			},
			{
				ResourceName:                         "ignitify_deployment.d",
				ImportState:                          true,
				ImportStateId:                        slug,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "task",
				ImportStateVerifyIgnore:              []string{"source_dir", "source_hash"},
			},
			{
				ResourceName:                         "ignitify_hook.h",
				ImportState:                          true,
				ImportStateIdFunc:                    hookImportID(slug),
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "resource_id",
				ImportStateVerifyIgnore:              []string{"secret"},
			},
		},
	})
}

func hookImportID(slug string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		id, err := resourceIDOf("ignitify_hook.h")(s)
		return slug + "/" + id, err
	}
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

// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// envTerraformPath names the Terraform or OpenTofu binary the testing library runs.
const envTerraformPath = "TF_ACC_TERRAFORM_PATH"

// TestTofuAgainstFakeAPI runs real plans and applies (`make test-tofu`, OpenTofu
// in the test image) against the in-memory fake API. It proves what calling the
// resource methods directly cannot: plan modifiers, defaults, the write-only
// values, an empty plan after apply, a refused notify target that warns and
// does not taint, replace on a label change, drift, a 404 removing an object,
// and a code change that updates the deployment in place
// (a new deployment, the old one deleted, the hook kept). It is skipped when no
// binary is named, so `make test` never downloads one.
func TestTofuAgainstFakeAPI(t *testing.T) {
	if os.Getenv(envTerraformPath) == "" {
		t.Skipf("%s is not set; run `make test-tofu`", envTerraformPath)
	}
	fake, c := newFakeAPI(t)
	warnings := &warningLog{}
	taskDir := t.TempDir()
	writeTaskFolder(t, taskDir, "report", "def run(p, c):\n    return 1\n")
	var firstDeployment, hookURL string
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

resource "ignitify_task" "t" {
  slug = "report"
}

resource "ignitify_deployment" "d" {
  task                     = ignitify_task.t.slug
  source_dir               = %q
  notify_url               = "https://hooks.example.test/done"
  notify_secret_wo         = %q
  notify_secret_wo_version = 1
}

resource "ignitify_hook" "h" {
  task = ignitify_task.t.slug
}
`, fake.baseURL, label, fakeSecretValue, taskDir, fakeNotifySecret)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: warnings.providers(),
		Steps: []resource.TestStep{
			{
				// Create while the tier refuses the notify target: the
				// deployment is live, the apply only warns, and nothing is
				// tainted. The refreshed plan then shows the notify change.
				PreConfig:          func() { fake.setRefuseNotify(true) },
				Config:             config("first"),
				ExpectNonEmptyPlan: true,
				Check: func(s *terraform.State) error {
					if !warnings.has(notifyNotSetSummary) {
						return fmt.Errorf("no %q warning; got %v", notifyNotSetSummary, warnings.all())
					}
					rs := s.RootModule().Resources["ignitify_deployment.d"]
					if rs == nil || rs.Primary.Tainted {
						return fmt.Errorf("the deployment is missing or tainted after a notify refusal")
					}
					if live := fake.deploymentNow(rs.Primary.Attributes["deployment_id"]); live == nil || live.dep.NotifyURL != nil {
						return fmt.Errorf("the deployment must be live with no notify target")
					}
					return nil
				},
			},
			{
				// The next plan retries only the notify write.
				Config:             config("first"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// The refusal is gone: the apply sets the notify target; the
				// library then fails the step if a new plan is not empty.
				PreConfig: func() { fake.setRefuseNotify(false) },
				Config:    config("first"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ignitify_deployment.d", plancheck.ResourceActionUpdate)},
				},
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
					resource.TestCheckResourceAttr("ignitify_deployment.d", "status", "READY"),
					resource.TestCheckResourceAttr("ignitify_deployment.d", "notify_url", "https://hooks.example.test/done"),
					resource.TestCheckNoResourceAttr("ignitify_deployment.d", "notify_secret_wo"),
					resource.TestCheckResourceAttrSet("ignitify_hook.h", "secret"),
					func(s *terraform.State) error {
						firstDeployment = attrOf(s, "ignitify_deployment.d", "deployment_id")
						hookURL = attrOf(s, "ignitify_hook.h", "url")
						if !strings.HasPrefix(hookURL, fake.baseURL+"/hooks/trigger/") {
							return fmt.Errorf("hook url %q", hookURL)
						}
						if live := fake.deploymentNow(firstDeployment); live == nil || live.notifySecret != fakeNotifySecret {
							return fmt.Errorf("the write-only notify secret did not reach the API")
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
			{
				// A code change updates the deployment in place: a new deployment,
				// the old one deleted once the new one is READY, the hook kept.
				PreConfig: func() { writeTaskFolder(t, taskDir, "report", "def run(p, c):\n    return 2\n") },
				Config:    config("second"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ignitify_deployment.d", plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue("ignitify_deployment.d", tfjsonpath.New("deployment_id")),
						plancheck.ExpectResourceAction("ignitify_hook.h", plancheck.ResourceActionNoop),
					},
				},
				Check: func(s *terraform.State) error {
					next := attrOf(s, "ignitify_deployment.d", "deployment_id")
					if next == firstDeployment {
						return fmt.Errorf("a code change kept deployment %s", next)
					}
					if fake.deploymentNow(firstDeployment) != nil {
						return fmt.Errorf("the old deployment %s is still live", firstDeployment)
					}
					if live := fake.deploymentNow(next); live == nil || live.notifySecret != fakeNotifySecret {
						return fmt.Errorf("the new deployment has no notify target")
					}
					if got := attrOf(s, "ignitify_hook.h", "url"); got != hookURL {
						return fmt.Errorf("hook url changed: %s, was %s", got, hookURL)
					}
					return nil
				},
			},
		},
	})
}

// attrOf is one attribute of a resource in the state, "" when absent.
func attrOf(s *terraform.State, address, name string) string {
	rs, ok := s.RootModule().Resources[address]
	if !ok {
		return ""
	}
	return rs.Primary.Attributes[name]
}

// warningLog records the summary of every warning the provider answers a
// plan or an apply with: the testing library does not expose warnings.
type warningLog struct {
	mu       sync.Mutex
	warnings []string
}

func (l *warningLog) add(diags []*tfprotov6.Diagnostic) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, d := range diags {
		if d != nil && d.Severity == tfprotov6.DiagnosticSeverityWarning {
			l.warnings = append(l.warnings, d.Summary)
		}
	}
}

func (l *warningLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.warnings)
}

func (l *warningLog) has(summary string) bool {
	return slices.Contains(l.all(), summary)
}

// providers are the provider factories with every plan and apply recorded in l.
func (l *warningLog) providers() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		providerTypeName: func() (tfprotov6.ProviderServer, error) {
			inner, err := providerserver.NewProtocol6WithError(New(accVersion)())()
			if err != nil {
				return nil, err
			}
			return &recordingServer{ProviderServer: inner, log: l}, nil
		},
	}
}

// recordingServer is a provider server that records the warnings of each plan and apply.
type recordingServer struct {
	tfprotov6.ProviderServer
	log *warningLog
}

func (s *recordingServer) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	resp, err := s.ProviderServer.ApplyResourceChange(ctx, req)
	if resp != nil {
		s.log.add(resp.Diagnostics)
	}
	return resp, err
}

func (s *recordingServer) PlanResourceChange(ctx context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	resp, err := s.ProviderServer.PlanResourceChange(ctx, req)
	if resp != nil {
		s.log.add(resp.Diagnostics)
	}
	return resp, err
}

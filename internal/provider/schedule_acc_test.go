// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// The schedule the acceptance test declares. The zone is written in a
// non-canonical case, and the execution keys in an order the server's jsonb
// column does not keep (it sorts shorter keys first), so both come back in
// another form and must still give an empty plan.
const (
	accScheduleCron          = "0 3 * * *"
	accScheduleZone          = "europe/berlin"
	accScheduleCanonicalZone = "Europe/Berlin"
)

// accOutsidePayload is the payload an edit outside Terraform sets.
var accOutsidePayload = map[string]any{"day": "today"}

// The schedule quota test needs a tenant on the tier whose schedule limit it
// knows. envQuotaAPIKey, when set, is a key of such a tenant; else the test
// uses IGNITIFY_API_KEY and skips unless that tenant is on the tier.
const (
	envQuotaAPIKey        = "IGNITIFY_ACC_QUOTA_API_KEY"
	quotaTierName         = "devtrial"
	quotaTierMaxSchedules = 2
)

// accClient is an API client with the provider's own settings from the
// environment, for the edits a test makes outside Terraform.
func accClient(t *testing.T) *client.Client {
	t.Helper()
	s, err := resolveSettings(configInput{
		apiKey:          types.StringNull(),
		endpoint:        types.StringNull(),
		envAPIKey:       os.Getenv(envAPIKey),
		envEndpoint:     os.Getenv(envAPIURL),
		userAgentSuffix: accVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(client.Config{Endpoint: s.endpoint, APIKey: s.apiKey, UserAgent: userAgentPrefix + s.version})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// expectAttributeChange is a plan check: the resource's planned change sets
// the attribute to another value than the prior state holds.
type expectAttributeChange struct {
	address   string
	attribute string
}

func (e expectAttributeChange) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != e.address || rc.Change == nil {
			continue
		}
		before, _ := rc.Change.Before.(map[string]any)
		after, _ := rc.Change.After.(map[string]any)
		if reflect.DeepEqual(before[e.attribute], after[e.attribute]) {
			resp.Error = fmt.Errorf("%s: the plan does not change %s (%v)", e.address, e.attribute, after[e.attribute])
		}
		return
	}
	resp.Error = fmt.Errorf("%s has no change in the plan", e.address)
}

// TestAccSchedule deploys a tiny task with a schedule, then checks against
// the live API: the server's forms of the zone and the execution JSON give an
// empty plan; a pause and a payload edit made outside Terraform each show in
// the next plan and the apply undoes them; import; and a task delete removes
// its schedule. It builds on the platform once.
func TestAccSchedule(t *testing.T) {
	slug := accName()
	dir := t.TempDir()
	copyAccTask(t, dir, slug, 1)
	var api *client.Client
	var scheduleID, firstDigest, outsideDigest string
	full := fmt.Sprintf(`
resource "ignitify_task" "t" {
  slug = %q
}

resource "ignitify_deployment" "d" {
  task       = ignitify_task.t.slug
  source_dir = %q
}

resource "ignitify_schedule" "s" {
  task      = ignitify_deployment.d.task
  cron      = %q
  timezone  = %q
  payload   = jsonencode({ day = "yesterday", rows = 10 })
  execution = jsonencode({ duration = "short", state = "independent" })
}
`, slug, dir, accScheduleCron, accScheduleZone)
	live := func() client.Schedule {
		found, err := api.GetSchedule(t.Context(), scheduleID)
		if err != nil {
			t.Fatalf("read schedule %s: %v", scheduleID, err)
		}
		return found
	}
	outsideEdit := func(edit client.ScheduleEdit) client.Schedule {
		edited, err := api.PatchSchedule(t.Context(), scheduleID, edit)
		if err != nil {
			t.Fatalf("edit schedule %s outside Terraform: %v", scheduleID, err)
		}
		return edited
	}
	changes := func(attribute string) resource.ConfigPlanChecks {
		return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction(scheduleAddress, plancheck.ResourceActionUpdate),
			expectAttributeChange{address: scheduleAddress, attribute: attribute},
		}}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accProviders,
		PreCheck:                 func() { api = accClient(t) },
		Steps: []resource.TestStep{
			{
				Config: full,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ignitify_deployment.d", "status", "READY"),
					resource.TestCheckResourceAttr(scheduleAddress, "timezone", accScheduleZone),
					resource.TestCheckResourceAttr(scheduleAddress, "status", client.ScheduleActive),
					resource.TestCheckResourceAttr(scheduleAddress, "paused", "false"),
					resource.TestCheckResourceAttrSet(scheduleAddress, "payload_sha256"),
					func(s *terraform.State) error {
						scheduleID = attrOf(s, scheduleAddress, "schedule_id")
						firstDigest = attrOf(s, scheduleAddress, "payload_sha256")
						found := live()
						// The empty plan proves the echo rule only while the
						// server really answers other forms than the configuration.
						if found.Timezone != accScheduleCanonicalZone {
							return fmt.Errorf("server zone %q, want %q", found.Timezone, accScheduleCanonicalZone)
						}
						if got, sent := compactJSON(found.RunSettings.Execution), attrOf(s, scheduleAddress, "execution"); got == sent {
							return fmt.Errorf("the server answered the execution JSON as sent (%s); the test no longer covers a re-ordered echo", got)
						}
						return nil
					},
				),
			},
			{
				// A pause made outside Terraform is a change; the apply resumes.
				PreConfig: func() {
					if got := outsideEdit(client.ScheduleEdit{fieldStatus: client.SchedulePaused}).Status; got != client.SchedulePaused {
						t.Fatalf("status %q after the outside pause", got)
					}
				},
				Config:           full,
				ConfigPlanChecks: changes("paused"),
				Check: func(*terraform.State) error {
					if got := live().Status; got != client.ScheduleActive {
						return fmt.Errorf("status %q after the apply, want %q", got, client.ScheduleActive)
					}
					return nil
				},
			},
			{
				// A payload edited outside Terraform is a change; the apply restores it.
				PreConfig: func() {
					outsideDigest = outsideEdit(client.ScheduleEdit{fieldPayload: accOutsidePayload}).PayloadSHA256
					if outsideDigest == "" || outsideDigest == firstDigest {
						t.Fatalf("payload digest %q after the outside edit, first %q", outsideDigest, firstDigest)
					}
				},
				Config:           full,
				ConfigPlanChecks: changes("payload"),
				Check: func(*terraform.State) error {
					if got := live().PayloadSHA256; got != firstDigest {
						return fmt.Errorf("payload digest %q after the apply, want the configured payload's %q", got, firstDigest)
					}
					return nil
				},
			},
			{
				// An import takes the server's forms and has no payload.
				ResourceName:                         scheduleAddress,
				ImportState:                          true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return slug + importIDSeparator + scheduleID, nil },
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "schedule_id",
				ImportStateVerifyIgnore:              []string{"payload", "timezone", "cron", "execution"},
			},
			{
				// A task deleted outside Terraform takes its schedule with it:
				// the refresh drops all three objects and nothing is left to plan.
				PreConfig: func() {
					if err := api.DeleteTask(t.Context(), slug); err != nil {
						t.Fatalf("delete task %s: %v", slug, err)
					}
				},
				Config: "# The task was deleted outside Terraform; nothing is declared.\n",
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(*terraform.State) error {
					_, err := api.GetSchedule(t.Context(), scheduleID)
					if !client.IsGone(err) {
						return fmt.Errorf("schedule %s after its task's delete: want 404 %s, got %v", scheduleID, client.ReasonScheduleNotFound, err)
					}
					return nil
				},
			},
		},
	})
}

// useQuotaTenant points the provider at the quota tenant's key, when one is
// given, and skips unless that tenant's tier is the one with the known limit.
func useQuotaTenant(t *testing.T) {
	t.Helper()
	if key := os.Getenv(envQuotaAPIKey); key != "" {
		t.Setenv(envAPIKey, key)
	}
	me, err := accClient(t).Whoami(t.Context())
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if me.Tier.Name != quotaTierName {
		t.Skipf("the key's tenant is on tier %q, not %q (max_schedules %d); set %s to a key of a %s tenant with no live schedules",
			me.Tier.Name, quotaTierName, quotaTierMaxSchedules, envQuotaAPIKey, quotaTierName)
	}
}

// TestAccScheduleQuota fills the tier's schedule limit and checks that one
// more schedule is refused with the quota reason. The tenant needs a free task
// slot and no live schedules. No build: a schedule needs no deployment.
func TestAccScheduleQuota(t *testing.T) {
	slug := accName()
	config := func(count int) string {
		return fmt.Sprintf(`
resource "ignitify_task" "t" {
  slug = %q
}

resource "ignitify_schedule" "s" {
  count   = %d
  task    = ignitify_task.t.slug
  cron    = %q
  payload = jsonencode({ n = count.index })
}
`, slug, count, accScheduleCron)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accProviders,
		PreCheck:                 func() { useQuotaTenant(t) },
		Steps: []resource.TestStep{
			{
				Config: config(quotaTierMaxSchedules),
				Check:  resource.TestCheckResourceAttr(fmt.Sprintf("ignitify_schedule.s.%d", quotaTierMaxSchedules-1), "status", client.ScheduleActive),
			},
			{
				Config:      config(quotaTierMaxSchedules + 1),
				ExpectError: regexp.MustCompile(regexp.QuoteMeta(fakeScheduleQuota)),
			},
		},
	})
}

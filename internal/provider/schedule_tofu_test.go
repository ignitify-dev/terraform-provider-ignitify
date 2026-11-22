// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

const scheduleAddress = "ignitify_schedule.s"

// TestTofuScheduleAgainstFakeAPI runs real plans and applies of a schedule
// (`make test-tofu`, `make test-terraform`) against the in-memory fake API:
// the server's forms of the zone, the cron text and the execution JSON give an
// empty plan; an outside change of the zone, the payload or the pause is a
// change the apply undoes; an auto-paused schedule is no change and warns; a
// removed preset is removed; a task change replaces; import.
func TestTofuScheduleAgainstFakeAPI(t *testing.T) {
	if os.Getenv(envTerraformPath) == "" {
		t.Skipf("%s is not set; run `make test-tofu`", envTerraformPath)
	}
	fake, _ := newFakeAPI(t)
	warnings := &warningLog{}
	var scheduleID string
	config := func(task, preset string) string {
		presetLine := ""
		if preset != "" {
			presetLine = fmt.Sprintf("preset = %q", preset)
		}
		return fmt.Sprintf(`
provider "ignitify" {
  endpoint = %q
  api_key  = "fake-test-key"
}

resource "ignitify_task" "report" {
  slug = "report"
}

resource "ignitify_task" "other" {
  slug = "other"
}

resource "ignitify_schedule" "s" {
  task      = ignitify_task.%s.slug
  cron      = "0  3 * * *"
  timezone  = "europe/berlin"
  payload   = jsonencode({ day = "yesterday", rows = 10 })
  execution = jsonencode({ mode = "pinned", cpu_policy = "spread" })
  %s
}
`, fake.baseURL, task, presetLine)
	}
	edit := func(change func(s *fakeSchedule)) func() {
		return func() {
			if !fake.editSchedule(scheduleID, change) {
				t.Fatalf("schedule %s is not live", scheduleID)
			}
		}
	}
	expectUpdate := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(scheduleAddress, plancheck.ResourceActionUpdate)},
	}
	reason := "deployment_missing"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: warnings.providers(),
		Steps: []resource.TestStep{
			{
				// The library fails the step if the plan after the apply is not
				// empty: the configured forms stay while the server's echo is unchanged.
				Config: config("report", "small"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(scheduleAddress, "timezone", "europe/berlin"),
					resource.TestCheckResourceAttr(scheduleAddress, "status", client.ScheduleActive),
					resource.TestCheckResourceAttrSet(scheduleAddress, "payload_sha256"),
					func(s *terraform.State) error {
						scheduleID = attrOf(s, scheduleAddress, "schedule_id")
						live := fake.scheduleNow(scheduleID)
						if live == nil || live.timezone != "Europe/Berlin" {
							return fmt.Errorf("schedule %s not live with the canonical zone", scheduleID)
						}
						return nil
					},
				),
			},
			{
				PreConfig:        edit(func(s *fakeSchedule) { s.timezone = "America/New_York" }),
				Config:           config("report", "small"),
				ConfigPlanChecks: expectUpdate,
				Check: func(*terraform.State) error {
					if got := fake.scheduleNow(scheduleID).timezone; got != "Europe/Berlin" {
						return fmt.Errorf("timezone %q after the apply", got)
					}
					return nil
				},
			},
			{
				PreConfig:        edit(func(s *fakeSchedule) { s.template["payload"] = map[string]any{"day": "today"} }),
				Config:           config("report", "small"),
				ConfigPlanChecks: expectUpdate,
				Check: func(*terraform.State) error {
					if got := fakePayloadDigest(fake.scheduleNow(scheduleID).template["payload"]); got != fakePayloadDigest(map[string]any{"day": "yesterday", "rows": 10}) {
						return fmt.Errorf("payload not restored")
					}
					return nil
				},
			},
			{
				// The platform's pause is no change: the apply leaves it, the plan warns.
				PreConfig: edit(func(s *fakeSchedule) { s.status, s.pauseReason = client.ScheduleAutoPaused, &reason }),
				Config:    config("report", "small"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(scheduleAddress, plancheck.ResourceActionNoop)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(scheduleAddress, "status", client.ScheduleAutoPaused),
					resource.TestCheckResourceAttr(scheduleAddress, "paused", "false"),
					func(*terraform.State) error {
						if !warnings.has(autoPausedSummary) {
							return fmt.Errorf("no %q warning; got %v", autoPausedSummary, warnings.all())
						}
						if fake.scheduleNow(scheduleID).status != client.ScheduleAutoPaused {
							return fmt.Errorf("the apply resumed an auto-paused schedule")
						}
						return nil
					},
				),
			},
			{
				// A tenant's pause made outside Terraform is a change; the apply resumes.
				PreConfig:        edit(func(s *fakeSchedule) { s.status, s.pauseReason = client.SchedulePaused, nil }),
				Config:           config("report", "small"),
				ConfigPlanChecks: expectUpdate,
				Check:            resource.TestCheckResourceAttr(scheduleAddress, "status", client.ScheduleActive),
			},
			{
				Config:           config("report", ""),
				ConfigPlanChecks: expectUpdate,
				Check: func(*terraform.State) error {
					if _, ok := fake.scheduleNow(scheduleID).template["preset"]; ok {
						return fmt.Errorf("the preset is still set")
					}
					return nil
				},
			},
			{
				Config: config("other", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(scheduleAddress, plancheck.ResourceActionReplace)},
				},
				Check: func(s *terraform.State) error {
					next := attrOf(s, scheduleAddress, "schedule_id")
					if next == scheduleID || fake.scheduleNow(scheduleID) != nil {
						return fmt.Errorf("a task change must replace schedule %s, got %s", scheduleID, next)
					}
					scheduleID = next
					return nil
				},
			},
			{
				// An import takes the server's forms and has no payload.
				ResourceName:                         scheduleAddress,
				ImportState:                          true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return "other/" + scheduleID, nil },
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "schedule_id",
				ImportStateVerifyIgnore:              []string{"payload", "timezone", "cron", "execution"},
			},
		},
	})
}

// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

const scheduleDetail = `{"kind":"cron","schedule_id":"sch_1","task_id":"uuid-1","cron":"0 3 * * *","timezone":"Europe/Berlin",
"overlap":"skip","status":"active","pause_reason":null,"pause_detail":null,"next_fire_at":"x","last_fire_at":null,
"last_outcome":null,"consecutive_misses":0,"created_by":"key_1","created_at":"t","updated_at":"t",
"run_settings":{"preset":"small","timeout_sec":60,"execution":{"mode":"pinned"}},"next_fire_times":["x"],
"payload_sha256":"abc"}`

func TestCreateScheduleSendsTheBodyAndReadsThe201(t *testing.T) {
	var method, path string
	var sent map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(scheduleDetail))
	})
	got, err := c.CreateSchedule(context.Background(), ScheduleCreate{
		Task: "report", Cron: "0 3 * * *", Timezone: "Europe/Berlin", Payload: json.RawMessage(`{"a":1}`), Overlap: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/schedules" {
		t.Fatalf("%s %s", method, path)
	}
	for _, absent := range []string{"preset", "timeout_sec", "deadline_sec", "execution", "status"} {
		if _, ok := sent[absent]; ok {
			t.Fatalf("an unset %s must not be sent: %v", absent, sent)
		}
	}
	if got.ScheduleID != "sch_1" || got.PayloadSHA256 != "abc" || *got.RunSettings.Preset != "small" ||
		*got.RunSettings.TimeoutSec != 60 || got.RunSettings.DeadlineSec != nil || string(got.RunSettings.Execution) != `{"mode":"pinned"}` {
		t.Fatalf("decoded %+v", got)
	}
}

func TestPatchScheduleSendsNullToRemoveAField(t *testing.T) {
	var raw []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/schedules/sch_1" || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(scheduleDetail))
	})
	if _, err := c.PatchSchedule(context.Background(), "sch_1", ScheduleEdit{"preset": nil, "cron": "5 * * * *"}); err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"cron":"5 * * * *","preset":null}` {
		t.Fatalf("body = %s", raw)
	}
}

func TestScheduleGoneOnlyOnItsOwn404(t *testing.T) {
	cases := []struct {
		name string
		body string
		gone bool
	}{
		{"its reason", `{"error":"schedule not found","reason":"schedule-not-found"}`, true},
		{"another route's reason", `{"error":"no such hook","reason":"hook-not-found"}`, false},
		{"no reason", `{"error":"not found"}`, false},
		{"html page", `<html>Cannot GET</html>`, false},
	}
	calls := map[string]func(c *Client) error{
		"get": func(c *Client) error { _, err := c.GetSchedule(context.Background(), "sch_1"); return err },
		"patch": func(c *Client) error {
			_, err := c.PatchSchedule(context.Background(), "sch_1", ScheduleEdit{"cron": "x"})
			return err
		},
		"delete": func(c *Client) error { return c.DeleteSchedule(context.Background(), "sch_1") },
	}
	for _, tc := range cases {
		for name, call := range calls {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(tc.body))
				})
				err := call(c)
				if err == nil || IsGone(err) != tc.gone {
					t.Fatalf("err = %v, gone = %v, want %v", err, IsGone(err), tc.gone)
				}
			})
		}
	}
}

func TestScheduleQuotaFailsAtOnce(t *testing.T) {
	tries := 0
	c, waits := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		tries++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"schedule quota exceeded (tier devtrial v1: 2)","reason":"schedule-quota","limit":2,"observed":2}`))
	})
	_, err := c.CreateSchedule(context.Background(), ScheduleCreate{Task: "t", Cron: "0 3 * * *", Timezone: "UTC", Payload: json.RawMessage(`{}`), Overlap: "skip"})
	if err == nil || !HasReason(err, "schedule-quota") {
		t.Fatalf("err = %v", err)
	}
	if tries != 1 || len(*waits) != 0 {
		t.Fatalf("tries = %d, waits = %v; a quota refusal is never retried", tries, *waits)
	}
}

func TestFieldOfABadRequest(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown time zone Mars/Olympus","reason":"bad-request","field":"timezone"}`))
	})
	_, err := c.PatchSchedule(context.Background(), "sch_1", ScheduleEdit{"timezone": "Mars/Olympus"})
	if FieldOf(err) != "timezone" {
		t.Fatalf("field = %q from %v", FieldOf(err), err)
	}
	if FieldOf(nil) != "" {
		t.Fatal("no error names no field")
	}
}

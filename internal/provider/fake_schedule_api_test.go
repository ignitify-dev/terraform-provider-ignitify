// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// The fake's recurring-schedule routes, with the server's rules: the body
// checked before the task is looked up, the zone in its canonical form, the
// cron text only trimmed, `overlap: null` refused, a null run field removed, an
// empty edit refused, the payload never answered but its digest, and a stored
// JSON object answered in the database's key order (shorter keys first).
//
// It does NOT check: the cron syntax or an expression that never fires, the
// bounds of preset, timeout_sec and deadline_sec, the payload size, the run
// settings against the served deployment, or the re-check of a resume. Those
// refusals come from the server; the provider only shows them.

// fakeSchedule is a schedule row. template holds the run fields it was given,
// the payload among them.
type fakeSchedule struct {
	id          string
	taskID      string
	cron        string
	timezone    string
	overlap     string
	status      string
	pauseReason *string
	pauseDetail *string
	template    map[string]any
	deleted     bool
}

// fakeZones are the zones the fake knows, by lower-case name, in their
// canonical form, as the server's zone lookup gives them.
var fakeZones = map[string]string{
	"utc":              "UTC",
	"europe/berlin":    "Europe/Berlin",
	"america/new_york": "America/New_York",
}

// fakeTemplateFields are the run fields of a schedule body.
var fakeTemplateFields = []string{"payload", "preset", "timeout_sec", "deadline_sec", "execution"}

const (
	scheduleNotFoundMessage = "schedule not found"
	fakeBadRequest          = "bad-request"
	fakeScheduleQuota       = "schedule-quota"
)

func (f *fakeAPI) scheduleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /schedules", f.handle(false, f.createSchedule))
	mux.HandleFunc("GET /schedules/{id}", f.handle(false, f.getSchedule))
	mux.HandleFunc("PATCH /schedules/{id}", f.handle(false, f.patchSchedule))
	mux.HandleFunc("DELETE /schedules/{id}", f.handle(false, f.deleteSchedule))
}

func badField(w http.ResponseWriter, field, message string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": message, "reason": fakeBadRequest, "field": field})
}

// fakeCron is the server's cron rule as far as the fake needs it: trimmed, not empty.
func fakeCron(value any) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

// fakeZone is a zone's canonical form; absent or null is UTC.
func fakeZone(value any) (string, bool) {
	if value == nil {
		return fakeZones["utc"], true
	}
	text, ok := value.(string)
	zone, known := fakeZones[strings.ToLower(text)]
	return zone, ok && known
}

// fakeOverlap is the overlap rule; absent is the default, null is refused.
func fakeOverlap(value any, present bool) (string, bool) {
	if !present {
		return defaultScheduleOverlap, true
	}
	text, _ := value.(string)
	return text, text == "skip" || text == "allow"
}

func (s *fakeSchedule) answer(omitDigest bool) map[string]any {
	settings := map[string]any{}
	for _, field := range fakeTemplateFields {
		if v, ok := s.template[field]; ok && field != "payload" {
			settings[field] = jsonbOrder(v)
		}
	}
	out := map[string]any{
		"kind": "cron", "schedule_id": s.id, "task_id": s.taskID, "cron": s.cron, "timezone": s.timezone,
		"overlap": s.overlap, "status": s.status, "pause_reason": s.pauseReason, "pause_detail": s.pauseDetail,
		"next_fire_at": nil, "last_fire_at": nil, "last_outcome": nil, "consecutive_misses": 0,
		"created_by": "key_fake", "created_at": fakeTime, "updated_at": fakeTime,
		"run_settings": settings, "next_fire_times": []string{},
	}
	if !omitDigest {
		out["payload_sha256"] = fakePayloadDigest(s.template["payload"])
	}
	return out
}

// jsonbOrder encodes a value the way the database answers a stored JSON
// object: keys ordered shorter first, then bytewise (Go and jsonencode sort
// them bytewise only).
func jsonbOrder(v any) json.RawMessage {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		var out bytes.Buffer
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			name, _ := json.Marshal(k)
			out.Write(name)
			out.WriteByte(':')
			out.Write(jsonbOrder(x[k]))
		}
		out.WriteByte('}')
		return out.Bytes()
	case []any:
		parts := make([][]byte, 0, len(x))
		for _, e := range x {
			parts = append(parts, jsonbOrder(e))
		}
		return append(append([]byte{'['}, bytes.Join(parts, []byte{','})...), ']')
	default:
		text, _ := json.Marshal(x)
		return text
	}
}

// fakePayloadDigest is the digest of a stored payload: Go writes a map with
// sorted keys, so the same value always gives the same text, as the server's
// stored JSON does.
func fakePayloadDigest(payload any) string {
	text, _ := json.Marshal(payload)
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

func (f *fakeAPI) liveSchedule(id string) *fakeSchedule {
	s, ok := f.app.schedules[id]
	if !ok || s.deleted {
		return nil
	}
	return s
}

func (f *fakeAPI) countLiveSchedules() int {
	n := 0
	for _, s := range f.app.schedules {
		if !s.deleted {
			n++
		}
	}
	return n
}

func (f *fakeAPI) createSchedule(w http.ResponseWriter, _ *http.Request, body map[string]any) {
	ref, _ := body["task"].(string)
	if ref == "" {
		badField(w, "task", "task is required")
		return
	}
	cron, ok := fakeCron(body["cron"])
	if !ok {
		badField(w, "cron", "cron is required")
		return
	}
	zone, ok := fakeZone(body["timezone"])
	if !ok {
		badField(w, "timezone", "unknown time zone")
		return
	}
	rawOverlap, present := body["overlap"]
	overlap, ok := fakeOverlap(rawOverlap, present)
	if !ok {
		badField(w, "overlap", "overlap must be skip or allow")
		return
	}
	if body["payload"] == nil {
		plainError(w, http.StatusBadRequest, "payload is required")
		return
	}
	t := f.liveTask(ref)
	if t == nil {
		refuse(w, http.StatusNotFound, client.ReasonTaskNotFound, taskNotFoundMessage)
		return
	}
	if f.app.scheduleQuota > 0 && f.countLiveSchedules() >= f.app.scheduleQuota {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "schedule quota exceeded", "reason": fakeScheduleQuota, "limit": f.app.scheduleQuota, "observed": f.countLiveSchedules(),
		})
		return
	}
	template := map[string]any{}
	for _, field := range fakeTemplateFields {
		if v := body[field]; v != nil {
			template[field] = v
		}
	}
	s := &fakeSchedule{id: f.id("sch"), taskID: t.task.ID, cron: cron, timezone: zone, overlap: overlap, status: client.ScheduleActive, template: template}
	f.app.schedules[s.id] = s
	writeJSON(w, http.StatusCreated, s.answer(f.app.omitScheduleDigest))
}

func (f *fakeAPI) getSchedule(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	s := f.liveSchedule(r.PathValue("id"))
	if s == nil {
		refuse(w, http.StatusNotFound, client.ReasonScheduleNotFound, scheduleNotFoundMessage)
		return
	}
	writeJSON(w, http.StatusOK, s.answer(f.app.omitScheduleDigest))
}

// patchSchedule applies an edit whole or not at all, as the server's
// transaction does.
func (f *fakeAPI) patchSchedule(w http.ResponseWriter, r *http.Request, body map[string]any) {
	s := f.liveSchedule(r.PathValue("id"))
	if s == nil {
		refuse(w, http.StatusNotFound, client.ReasonScheduleNotFound, scheduleNotFoundMessage)
		return
	}
	if len(body) == 0 {
		plainError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if _, ok := body["task"]; ok {
		badField(w, "task", "task cannot be changed")
		return
	}
	next := *s
	next.template = map[string]any{}
	for k, v := range s.template {
		next.template[k] = v
	}
	if v, ok := body["cron"]; ok {
		cron, valid := fakeCron(v)
		if !valid {
			badField(w, "cron", "cron is required")
			return
		}
		next.cron = cron
	}
	if v, ok := body["timezone"]; ok {
		zone, valid := fakeZone(v)
		if !valid {
			badField(w, "timezone", "unknown time zone")
			return
		}
		next.timezone = zone
	}
	if v, ok := body["overlap"]; ok {
		overlap, valid := fakeOverlap(v, true)
		if !valid {
			badField(w, "overlap", "overlap must be skip or allow")
			return
		}
		next.overlap = overlap
	}
	if v, ok := body["status"]; ok {
		status, _ := v.(string)
		if status != client.ScheduleActive && status != client.SchedulePaused {
			badField(w, "status", "status must be active or paused")
			return
		}
		if status != next.status {
			next.status, next.pauseReason, next.pauseDetail = status, nil, nil
		}
	}
	for _, field := range fakeTemplateFields {
		v, ok := body[field]
		switch {
		case !ok:
		case v == nil:
			delete(next.template, field)
		default:
			next.template[field] = v
		}
	}
	if next.template["payload"] == nil {
		plainError(w, http.StatusBadRequest, "payload is required")
		return
	}
	*s = next
	writeJSON(w, http.StatusOK, s.answer(f.app.omitScheduleDigest))
}

func (f *fakeAPI) deleteSchedule(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	s := f.liveSchedule(r.PathValue("id"))
	if s == nil {
		refuse(w, http.StatusNotFound, client.ReasonScheduleNotFound, scheduleNotFoundMessage)
		return
	}
	s.deleted = true
	w.WriteHeader(http.StatusNoContent)
}

// editSchedule changes a live schedule as the platform or another client
// would, under the lock; it reports whether the schedule is live.
func (f *fakeAPI) editSchedule(id string, change func(s *fakeSchedule)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.liveSchedule(id)
	if s == nil {
		return false
	}
	change(s)
	return true
}

// scheduleNow is a copy of a live schedule, read under the lock; nil when gone.
func (f *fakeAPI) scheduleNow(id string) *fakeSchedule {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.liveSchedule(id)
	if s == nil {
		return nil
	}
	snapshot := *s
	return &snapshot
}

// scheduleWrites lists the schedule writes the fake saw (method and path).
func (f *fakeAPI) scheduleWrites() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, r := range f.requests {
		if r.method != http.MethodGet && strings.HasPrefix(r.path, "/schedules") {
			out = append(out, r)
		}
	}
	return out
}

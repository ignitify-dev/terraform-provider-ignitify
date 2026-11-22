// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// The fake's task, deployment and hook routes (the schedule routes are in
// fake_schedule_api_test.go). A task is addressed by its slug or its id; a
// deploy takes the task id, as the real route does.

// fakeTask is a task row; a deleted task keeps its row, and a create on its
// slug brings the same row back.
type fakeTask struct {
	task    client.Task
	deleted bool
}

// fakeDeployment is a deployment row with its build: a new build stays
// BUILDING for pollsLeft reads, then turns READY (or FAILED when the fake
// fails builds).
type fakeDeployment struct {
	dep          client.Deployment
	pollsLeft    int
	deleted      bool
	notifySecret string
	archive      []byte
}

type fakeHook struct {
	hook    client.Hook
	deleted bool
}

// fakeApp is the fake's app state and switches.
type fakeApp struct {
	tasks       map[string]*fakeTask // by slug
	deployments map[string]*fakeDeployment
	depOrder    []string
	hooks       map[string]*fakeHook
	schedules   map[string]*fakeSchedule
	// scheduleQuota, when above zero, is the live schedule cap; a create at it answers 429.
	scheduleQuota int
	// omitScheduleDigest answers schedules without payload_sha256, as a server before it did.
	omitScheduleDigest bool
	// buildPolls is how many reads a new build stays BUILDING.
	buildPolls int
	// failBuilds makes every new build end FAILED.
	failBuilds bool
	// redactHookCreates answers this many hook creates without the secret, as an idempotent replay does.
	redactHookCreates int
	// failDeploymentDelete makes every deployment delete answer 500.
	failDeploymentDelete bool
	// refuseNotify makes every notify write answer 403, as a tier without webhooks does.
	refuseNotify bool
}

const fakeFailureReason = "npm install failed: the lockfile names a missing package"

func newFakeApp() fakeApp {
	return fakeApp{tasks: map[string]*fakeTask{}, deployments: map[string]*fakeDeployment{}, hooks: map[string]*fakeHook{}, schedules: map[string]*fakeSchedule{}}
}

func (f *fakeAPI) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /tasks", f.handle(false, f.createTask))
	mux.HandleFunc("GET /tasks/{id}", f.handle(false, f.getTask))
	mux.HandleFunc("DELETE /tasks/{id}", f.handle(false, f.deleteTask))
	mux.HandleFunc("POST /tasks/{id}/hooks", f.handle(false, f.createHook))
	mux.HandleFunc("GET /hooks/{id}", f.handle(false, f.getHook))
	mux.HandleFunc("DELETE /hooks/{id}", f.handle(false, f.deleteHook))
	mux.HandleFunc("POST /deployments", f.handle(false, f.createDeployment))
	mux.HandleFunc("GET /deployments", f.handle(false, f.listDeployments))
	mux.HandleFunc("GET /deployments/{id}", f.handle(false, f.getDeployment))
	mux.HandleFunc("DELETE /deployments/{id}", f.handle(false, f.deleteDeployment))
	mux.HandleFunc("PUT /deployments/{id}/notify", f.handle(false, f.setNotify))
	f.scheduleRoutes(mux)
}

// liveTask finds a live task by slug or id.
func (f *fakeAPI) liveTask(ref string) *fakeTask {
	for _, t := range f.app.tasks {
		if !t.deleted && (t.task.Slug == ref || t.task.ID == ref) {
			return t
		}
	}
	return nil
}

func (f *fakeAPI) createTask(w http.ResponseWriter, _ *http.Request, body map[string]any) {
	slug, _ := body["slug"].(string)
	if t, ok := f.app.tasks[slug]; ok {
		if !t.deleted {
			refuse(w, http.StatusConflict, client.ReasonTaskSlugTaken, taskSlugTakenMessage)
			return
		}
		t.deleted = false
		writeJSON(w, http.StatusCreated, map[string]any{"task_id": t.task.ID, "resource_id": t.task.ResourceID, "revived": true})
		return
	}
	t := &fakeTask{task: client.Task{ID: f.id("uuid-task"), ResourceID: f.id("tsk"), Slug: slug, CreatedAt: fakeTime}}
	f.app.tasks[slug] = t
	writeJSON(w, http.StatusCreated, map[string]any{"task_id": t.task.ID, "resource_id": t.task.ResourceID})
}

func (f *fakeAPI) getTask(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	t := f.liveTask(r.PathValue("id"))
	if t == nil {
		refuse(w, http.StatusNotFound, client.ReasonTaskNotFound, taskNotFoundMessage)
		return
	}
	writeJSON(w, http.StatusOK, t.task)
}

// deleteTask deletes the task with its deployments, hooks and schedules.
func (f *fakeAPI) deleteTask(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	t := f.liveTask(r.PathValue("id"))
	if t == nil {
		refuse(w, http.StatusNotFound, client.ReasonTaskNotFound, taskNotFoundMessage)
		return
	}
	t.deleted = true
	for _, d := range f.app.deployments {
		if d.dep.TaskID == t.task.ID {
			d.deleted = true
		}
	}
	for _, h := range f.app.hooks {
		if h.hook.TaskID == t.task.ID {
			h.deleted = true
		}
	}
	for _, s := range f.app.schedules {
		if s.taskID == t.task.ID {
			s.deleted = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": map[string]any{"task_id": t.task.ID}})
}

func (f *fakeAPI) createHook(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	t := f.liveTask(r.PathValue("id"))
	if t == nil {
		refuse(w, http.StatusNotFound, client.ReasonTaskNotFound, taskNotFoundMessage)
		return
	}
	h := client.Hook{TaskID: t.task.ID, ResourceID: f.id("hook"), HookID: f.id("trigger"), CreatedAt: fakeTime}
	f.app.hooks[h.ResourceID] = &fakeHook{hook: h}
	answer := map[string]any{"resource_id": h.ResourceID, "hook_id": h.HookID, "secret": "fake-secret-" + h.HookID, "url": "/hooks/trigger/" + h.HookID}
	if f.app.redactHookCreates > 0 {
		f.app.redactHookCreates--
		answer["secret"] = nil
		answer["secret_redacted"] = true
	}
	writeJSON(w, http.StatusCreated, answer)
}

func (f *fakeAPI) getHook(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	h, ok := f.app.hooks[r.PathValue("id")]
	if !ok || h.deleted {
		refuse(w, http.StatusNotFound, client.ReasonHookNotFound, hookNotFoundMessage)
		return
	}
	writeJSON(w, http.StatusOK, h.hook)
}

func (f *fakeAPI) deleteHook(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	h, ok := f.app.hooks[r.PathValue("id")]
	if !ok || h.deleted {
		refuse(w, http.StatusNotFound, client.ReasonHookNotFound, hookNotFoundMessage)
		return
	}
	h.deleted = true
	writeJSON(w, http.StatusOK, map[string]any{"deleted": map[string]any{"resource_id": h.hook.ResourceID}})
}

// liveHooks counts the live hooks.
func (f *fakeAPI) liveHooks() []client.Hook {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []client.Hook
	for _, h := range f.app.hooks {
		if !h.deleted {
			out = append(out, h.hook)
		}
	}
	return out
}

// createDeployment takes the multipart upload. The content key is the
// archive's digest; the same content of a task is the same deployment.
func (f *fakeAPI) createDeployment(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		plainError(w, http.StatusBadRequest, "not multipart: "+err.Error())
		return
	}
	t := f.liveTask(r.FormValue("task_id"))
	if t == nil || t.task.ID != r.FormValue("task_id") {
		refuse(w, http.StatusNotFound, client.ReasonTaskNotFound, taskNotFoundMessage)
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		plainError(w, http.StatusBadRequest, "no bundle")
		return
	}
	archive, _ := io.ReadAll(file)
	members, err := readFakeArchive(archive)
	if err != nil {
		plainError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, execution := fakeContentKey(members)
	for _, id := range f.app.depOrder {
		d := f.app.deployments[id]
		if d.dep.TaskID != t.task.ID || d.dep.ContentHash != hash {
			continue
		}
		if !d.deleted && (d.dep.Status == client.DeploymentReady || d.dep.Status == client.DeploymentBuilding) {
			// The run settings are not in the key: a reuse takes the new ones.
			d.dep.Execution = execution
			writeJSON(w, http.StatusOK, map[string]any{"deployment_id": d.dep.ID, "status": d.dep.Status, "reused": true})
			return
		}
		// A deleted or FAILED row of the same content is built again on the same id.
		d.deleted, d.dep.Status, d.dep.FailureReason, d.pollsLeft = false, client.DeploymentBuilding, nil, f.app.buildPolls
		writeJSON(w, http.StatusAccepted, map[string]any{"deployment_id": d.dep.ID, "status": d.dep.Status, "reused": false})
		return
	}
	d := &fakeDeployment{
		dep: client.Deployment{
			ID: f.id("uuid-dep"), ResourceID: f.id("dep"), TaskID: t.task.ID, TaskSlug: t.task.Slug,
			ContentHash: hash, Status: client.DeploymentBuilding, Execution: execution, CreatedAt: fakeTime,
		},
		pollsLeft: f.app.buildPolls,
		archive:   archive,
	}
	f.app.deployments[d.dep.ID] = d
	f.app.depOrder = append(f.app.depOrder, d.dep.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment_id": d.dep.ID, "status": d.dep.Status, "reused": false})
}

// fakeConfig is the task config the fake reads from an upload.
const fakeConfig = "task.yaml"

// fakeContentKey is the fake's content key: the digest of every member, with
// the config's top-level `execution:` block left out (a run setting, as on
// the server). The block's text is the deployment's execution. Pure.
func fakeContentKey(members map[string][]byte) (string, json.RawMessage) {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	execution := json.RawMessage("null")
	for _, name := range names {
		data := members[name]
		if name == fakeConfig {
			var block string
			data, block = splitExecutionBlock(data)
			if block != "" {
				execution, _ = json.Marshal(map[string]string{"block": block})
			}
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), execution
}

// splitExecutionBlock takes the top-level `execution:` block out of a config. Pure.
func splitExecutionBlock(config []byte) ([]byte, string) {
	var kept, block []string
	inBlock := false
	for _, line := range strings.Split(string(config), "\n") {
		switch {
		case strings.HasPrefix(line, "execution:"):
			inBlock = true
		case inBlock && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
		default:
			inBlock = false
		}
		if inBlock {
			block = append(block, line)
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n")), strings.Join(block, "\n")
}

// readFakeArchive reads an upload as the server does: regular files only.
func readFakeArchive(archive []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("member %s is not a regular file", hdr.Name)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out[hdr.Name] = data
	}
}

func (f *fakeAPI) liveDeployment(id string) *fakeDeployment {
	d, ok := f.app.deployments[id]
	if !ok || d.deleted {
		return nil
	}
	return d
}

func (f *fakeAPI) getDeployment(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	d := f.liveDeployment(r.PathValue("id"))
	if d == nil {
		refuse(w, http.StatusNotFound, client.ReasonDeploymentNotFound, deploymentNotFoundMessage)
		return
	}
	if d.dep.Status == client.DeploymentBuilding {
		if d.pollsLeft > 0 {
			d.pollsLeft--
		} else if f.app.failBuilds {
			reason := fakeFailureReason
			d.dep.Status, d.dep.FailureReason = client.DeploymentFailed, &reason
		} else {
			d.dep.Status = client.DeploymentReady
		}
	}
	writeJSON(w, http.StatusOK, d.dep)
}

func (f *fakeAPI) deleteDeployment(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	if f.app.failDeploymentDelete {
		plainError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	d := f.liveDeployment(r.PathValue("id"))
	if d == nil {
		refuse(w, http.StatusNotFound, client.ReasonDeploymentNotFound, deploymentNotFoundMessage)
		return
	}
	d.deleted = true
	writeJSON(w, http.StatusOK, map[string]any{"deleted": map[string]any{"deployment_id": d.dep.ID}})
}

// listDeployments lists a task's live deployments, oldest first, one page.
func (f *fakeAPI) listDeployments(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	t := f.liveTask(r.URL.Query().Get("task_id"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	after := r.URL.Query().Get("after_id")
	page := []client.Deployment{}
	started := after == ""
	for _, id := range f.app.depOrder {
		d := f.app.deployments[id]
		if t == nil || d.deleted || d.dep.TaskID != t.task.ID {
			continue
		}
		if !started {
			started = id == after
			continue
		}
		if len(page) < limit {
			page = append(page, d.dep)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": page})
}

func (f *fakeAPI) setNotify(w http.ResponseWriter, r *http.Request, body map[string]any) {
	d := f.liveDeployment(r.PathValue("id"))
	if d == nil {
		refuse(w, http.StatusNotFound, client.ReasonDeploymentNotFound, deploymentNotFoundMessage)
		return
	}
	if f.app.refuseNotify {
		refuse(w, http.StatusForbidden, "capability-denied", "capability may_use_webhooks is not in this tier")
		return
	}
	url, isString := body["url"].(string)
	if !isString {
		d.dep.NotifyURL, d.notifySecret = nil, ""
		writeJSON(w, http.StatusOK, map[string]any{"notify_url": nil})
		return
	}
	secret, _ := body["secret"].(string)
	if len(secret) < notifySecretMinLength {
		plainError(w, http.StatusBadRequest, "notify secret is required")
		return
	}
	d.dep.NotifyURL, d.notifySecret = &url, secret
	writeJSON(w, http.StatusOK, map[string]any{"notify_url": url})
}

// deploymentNow is a live deployment, read under the lock, for a test check; nil when gone.
func (f *fakeAPI) deploymentNow(id string) *fakeDeployment {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.liveDeployment(id)
	if d == nil {
		return nil
	}
	snapshot := *d
	return &snapshot
}

// liveDeployments lists the live deployments, oldest first.
func (f *fakeAPI) liveDeployments() []fakeDeployment {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeDeployment
	for _, id := range f.app.depOrder {
		if d := f.app.deployments[id]; !d.deleted {
			out = append(out, *d)
		}
	}
	return out
}

// setRefuseNotify switches the notify refusal while the fake serves requests.
func (f *fakeAPI) setRefuseNotify(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.app.refuseNotify = on
}

// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// fakeTime is the timestamp every fake object carries.
const fakeTime = "2026-01-02T03:04:05.000Z"

// recordedRequest is one request the fake API saw.
type recordedRequest struct {
	method         string
	path           string
	idempotencyKey string
	userAgent      string
	contentType    string
	ifNoneMatch    string
	body           map[string]any
}

// fakeAPI is an in-memory tenant API with the access, secrets, identity,
// task, deployment and hook routes. Its switches force the error paths.
type fakeAPI struct {
	mu       sync.Mutex
	users    map[string]client.AccessUser
	groups   map[string]*client.Group
	keys     map[string]client.AccessKey
	secrets  map[string]client.Secret
	values   map[string]string
	requests []recordedRequest
	nextID   int
	// app holds the tasks, deployments and hooks (fake_app_api_test.go).
	app fakeApp
	// baseURL is where the fake listens.
	baseURL string

	// userKey makes every owner-only route answer 403 owner-required.
	userKey bool
	// redactKeyCreates answers this many key creates without the raw key, as an idempotent replay does.
	redactKeyCreates int
	// vaultOff makes every secrets route answer 503 without Retry-After.
	vaultOff bool
	// secretReadMisses makes a secret read answer 404 for a name that exists,
	// as when another writer creates it between a read and a write.
	secretReadMisses bool
	// failRevoke makes every key revoke answer 500.
	failRevoke bool
	// override, when set, answers every request instead of the routes (for
	// example a proxy page in front of the API).
	override http.HandlerFunc
	// whoami is the identity answer.
	whoami client.Whoami
}

func newFakeAPI(t *testing.T) (*fakeAPI, *client.Client) {
	t.Helper()
	f := &fakeAPI{
		users:   map[string]client.AccessUser{},
		groups:  map[string]*client.Group{},
		keys:    map[string]client.AccessKey{},
		secrets: map[string]client.Secret{},
		values:  map[string]string{},
		app:     newFakeApp(),
	}
	srv := httptest.NewServer(f.routes())
	t.Cleanup(srv.Close)
	f.baseURL = srv.URL
	c, err := client.New(client.Config{Endpoint: srv.URL, APIKey: "fake-test-key", UserAgent: userAgentPrefix + "test"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return f, c
}

func (f *fakeAPI) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s_%04d", prefix, f.nextID)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, status int, reason, message string) {
	writeJSON(w, status, map[string]string{"error": message, "reason": reason})
}

func plainError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// handle wraps a route: records the request, holds the lock, and applies the
// owner-only switch.
func (f *fakeAPI) handle(ownerOnly bool, fn func(w http.ResponseWriter, r *http.Request, body map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The raw body stays readable for the routes that are not JSON (a deploy upload).
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, recordedRequest{
			method:         r.Method,
			path:           r.URL.Path,
			idempotencyKey: r.Header.Get("Idempotency-Key"),
			userAgent:      r.Header.Get("User-Agent"),
			contentType:    r.Header.Get("Content-Type"),
			ifNoneMatch:    r.Header.Get("If-None-Match"),
			body:           body,
		})
		if f.override != nil {
			f.override(w, r)
			return
		}
		if ownerOnly && f.userKey {
			refuse(w, http.StatusForbidden, client.ReasonOwnerRequired, "only an owner key manages users, groups and keys")
			return
		}
		fn(w, r, body)
	}
}

func (f *fakeAPI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", f.handle(true, f.createUser))
	mux.HandleFunc("GET /users/{id}", f.handle(true, f.getUser))
	mux.HandleFunc("DELETE /users/{id}", f.handle(true, f.deleteUser))
	mux.HandleFunc("POST /groups", f.handle(true, f.createGroup))
	mux.HandleFunc("GET /groups/{id}", f.handle(true, f.getGroup))
	mux.HandleFunc("DELETE /groups/{id}", f.handle(true, f.deleteGroup))
	mux.HandleFunc("PUT /groups/{id}/permissions/{permission}", f.handle(true, f.changePermission(true)))
	mux.HandleFunc("DELETE /groups/{id}/permissions/{permission}", f.handle(true, f.changePermission(false)))
	mux.HandleFunc("PUT /groups/{id}/users/{user}", f.handle(true, f.changeMember(true)))
	mux.HandleFunc("DELETE /groups/{id}/users/{user}", f.handle(true, f.changeMember(false)))
	mux.HandleFunc("POST /keys", f.handle(true, f.createKey))
	mux.HandleFunc("GET /keys/{id}", f.handle(true, f.getKey))
	mux.HandleFunc("DELETE /keys/{id}", f.handle(true, f.revokeKey))
	mux.HandleFunc("POST /secrets", f.handle(false, f.setSecret))
	mux.HandleFunc("GET /secrets/{name}", f.handle(false, f.getSecret))
	mux.HandleFunc("DELETE /secrets/{name}", f.handle(false, f.deleteSecret))
	f.appRoutes(mux)
	mux.HandleFunc("GET /whoami", f.handle(false, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(w, http.StatusOK, f.whoami)
	}))
	return mux
}

func (f *fakeAPI) createUser(w http.ResponseWriter, _ *http.Request, body map[string]any) {
	name, _ := body["name"].(string)
	if _, taken := f.users[name]; taken {
		refuse(w, http.StatusConflict, client.ReasonNameTaken, "name taken")
		return
	}
	u := client.AccessUser{ResourceID: f.id("usr"), Name: name, CreatedAt: fakeTime}
	f.users[name] = u
	writeJSON(w, http.StatusCreated, u)
}

func (f *fakeAPI) getUser(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	u, ok := f.users[r.PathValue("id")]
	if !ok {
		refuse(w, http.StatusNotFound, client.ReasonUserNotFound, "no user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (f *fakeAPI) deleteUser(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	name := r.PathValue("id")
	if _, ok := f.users[name]; !ok {
		refuse(w, http.StatusNotFound, client.ReasonUserNotFound, "no user")
		return
	}
	delete(f.users, name)
	for _, g := range f.groups {
		g.Users = without(g.Users, name)
	}
	for id, k := range f.keys {
		if k.User != nil && *k.User == name {
			delete(f.keys, id)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": name, "revoked_keys": []string{}})
}

func (f *fakeAPI) createGroup(w http.ResponseWriter, _ *http.Request, body map[string]any) {
	name, _ := body["name"].(string)
	if _, taken := f.groups[name]; taken {
		refuse(w, http.StatusConflict, client.ReasonNameTaken, "name taken")
		return
	}
	g := &client.Group{ResourceID: f.id("grp"), Name: name, Permissions: stringsOf(body["permissions"]), Users: []string{}, CreatedAt: fakeTime}
	f.groups[name] = g
	writeJSON(w, http.StatusCreated, g)
}

func (f *fakeAPI) getGroup(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	g, ok := f.groups[r.PathValue("id")]
	if !ok {
		refuse(w, http.StatusNotFound, client.ReasonGroupNotFound, "no group")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (f *fakeAPI) deleteGroup(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	name := r.PathValue("id")
	if _, ok := f.groups[name]; !ok {
		refuse(w, http.StatusNotFound, client.ReasonGroupNotFound, "no group")
		return
	}
	delete(f.groups, name)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}

func (f *fakeAPI) changePermission(grant bool) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		g, ok := f.groups[r.PathValue("id")]
		if !ok {
			refuse(w, http.StatusNotFound, client.ReasonGroupNotFound, "no group")
			return
		}
		p := r.PathValue("permission")
		g.Permissions = without(g.Permissions, p)
		if grant {
			g.Permissions = append(g.Permissions, p)
		}
		writeJSON(w, http.StatusOK, g)
	}
}

func (f *fakeAPI) changeMember(add bool) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		g, ok := f.groups[r.PathValue("id")]
		if !ok {
			refuse(w, http.StatusNotFound, client.ReasonGroupNotFound, "no group")
			return
		}
		u := r.PathValue("user")
		if _, ok := f.users[u]; !ok {
			refuse(w, http.StatusNotFound, client.ReasonUserNotFound, "no user")
			return
		}
		g.Users = without(g.Users, u)
		if add {
			g.Users = append(g.Users, u)
		}
		writeJSON(w, http.StatusOK, g)
	}
}

func (f *fakeAPI) createKey(w http.ResponseWriter, _ *http.Request, body map[string]any) {
	user, _ := body["user"].(string)
	label, _ := body["label"].(string)
	if _, ok := f.users[user]; !ok {
		refuse(w, http.StatusNotFound, client.ReasonUserNotFound, "no user")
		return
	}
	id := f.id("key")
	f.keys[id] = client.AccessKey{ResourceID: id, User: &user, Label: &label, CreatedAt: fakeTime}
	answer := map[string]any{"resource_id": id, "user": user, "label": label, "raw_key": "fake-raw-" + id}
	if f.redactKeyCreates > 0 {
		f.redactKeyCreates--
		answer["raw_key"] = nil
		answer["secret_redacted"] = true
	}
	writeJSON(w, http.StatusCreated, answer)
}

func (f *fakeAPI) getKey(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	k, ok := f.keys[r.PathValue("id")]
	if !ok {
		plainError(w, http.StatusNotFound, client.GoneKeyMessage)
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (f *fakeAPI) revokeKey(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	if f.failRevoke {
		plainError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	id := r.PathValue("id")
	if _, ok := f.keys[id]; !ok {
		plainError(w, http.StatusNotFound, client.GoneKeyMessage)
		return
	}
	delete(f.keys, id)
	writeJSON(w, http.StatusOK, map[string]string{"revoked": id})
}

const vaultOffMessage = "secret vault is not enabled on this platform"

func (f *fakeAPI) setSecret(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if f.vaultOff {
		plainError(w, http.StatusServiceUnavailable, vaultOffMessage)
		return
	}
	name, _ := body["name"].(string)
	value, _ := body["value"].(string)
	if _, exists := f.secrets[name]; exists && r.Header.Get("If-None-Match") == "*" {
		refuse(w, http.StatusPreconditionFailed, client.ReasonSecretExists, "a secret with this name already exists")
		return
	}
	f.values[name] = value
	f.nextID++
	f.secrets[name] = client.Secret{Name: name, UpdatedAt: fmt.Sprintf("2026-01-02T03:04:%02d.000Z", f.nextID%60)}
	writeJSON(w, http.StatusCreated, map[string]any{"name": name, "stored": true})
}

func (f *fakeAPI) getSecret(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	if f.vaultOff {
		plainError(w, http.StatusServiceUnavailable, vaultOffMessage)
		return
	}
	s, ok := f.secrets[r.PathValue("name")]
	if !ok || f.secretReadMisses {
		plainError(w, http.StatusNotFound, client.GoneSecretMessage)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (f *fakeAPI) deleteSecret(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	name := r.PathValue("name")
	if _, ok := f.secrets[name]; !ok {
		plainError(w, http.StatusNotFound, client.GoneSecretMessage)
		return
	}
	delete(f.secrets, name)
	delete(f.values, name)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "removed": true})
}

// calls lists the "METHOD path" of every request, in order.
func (f *fakeAPI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		out = append(out, r.method+" "+r.path)
	}
	return out
}

// lastRequest is the newest request the fake saw.
func (f *fakeAPI) lastRequest() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeAPI) allRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// htmlNotFound is the 404 page a web server or proxy answers for a path it does not serve.
func htmlNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<!DOCTYPE html><html><body><pre>Cannot GET /x</pre></body></html>"))
}

func without(values []string, drop string) []string {
	out := []string{}
	for _, v := range values {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

func stringsOf(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

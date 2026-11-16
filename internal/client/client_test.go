// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestClient points a client at handler and records its waits instead of sleeping.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, APIKey: "fake-test-key", UserAgent: "terraform-provider-ignitify/test"})
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return c, &waits
}

func TestHeadersOnEveryRequest(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"resource_id":"usr_1","name":"a","created_at":"x"}`))
	})
	if _, err := c.CreateUser(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer fake-test-key" {
		t.Fatalf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("User-Agent") != "terraform-provider-ignitify/test" {
		t.Fatalf("User-Agent = %q", got.Get("User-Agent"))
	}
	if got.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", got.Get("Content-Type"))
	}
	if key := got.Get("Idempotency-Key"); len(key) < 8 || len(key) > 255 || strings.Trim(key, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:-") != "" {
		t.Fatalf("Idempotency-Key %q breaks the server's key rule", key)
	}
}

func TestReadSendsNoIdempotencyKey(t *testing.T) {
	var key string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		_, _ = w.Write([]byte(`{"name":"A","updated_at":"x"}`))
	})
	if _, err := c.GetSecret(context.Background(), "A"); err != nil {
		t.Fatal(err)
	}
	if key != "" {
		t.Fatalf("a GET sent Idempotency-Key %q", key)
	}
}

func TestRetryKeepsTheIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	c, waits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		switch len(keys) {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			// The first try of this key is still running on the server.
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"a request with this Idempotency-Key is still being processed"}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"A","stored":true}`))
		}
	})
	if err := c.SetSecret(context.Background(), "A", "fake-v"); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("keys = %v, want one key on all tries", keys)
	}
	if len(*waits) != 2 || (*waits)[0] != 2*time.Second {
		t.Fatalf("waits = %v", *waits)
	}
}

func TestNoRetryOnRefusal(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"name taken","reason":"name-taken"}`))
	})
	_, err := c.CreateUser(context.Background(), "a")
	if !HasReason(err, ReasonNameTaken) || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func TestErrorParsing(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"only an owner key manages users, groups and keys","reason":"owner-required"}`))
	})
	_, err := c.GetGroup(context.Background(), "ops")
	if !IsOwnerRequired(err) || IsGone(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "owner-required") {
		t.Fatalf("err text = %q", err.Error())
	}
}

func TestGoneOnlyForTheRoutesOwn404(t *testing.T) {
	cases := []struct {
		name string
		body string
		call func(c *Client) error
		gone bool
	}{
		{"user, its reason", `{"error":"no user","reason":"user-not-found"}`, func(c *Client) error { _, err := c.GetUser(context.Background(), "a"); return err }, true},
		{"user, another reason", `{"error":"no group","reason":"group-not-found"}`, func(c *Client) error { _, err := c.GetUser(context.Background(), "a"); return err }, false},
		{"user, html page", `<html>Cannot GET</html>`, func(c *Client) error { return c.DeleteUser(context.Background(), "a") }, false},
		{"group, its reason", `{"error":"no group","reason":"group-not-found"}`, func(c *Client) error { return c.DeleteGroup(context.Background(), "a") }, true},
		{"key, its text", `{"error":"no live key with that id in this tenant"}`, func(c *Client) error { _, err := c.GetKey(context.Background(), "k"); return err }, true},
		{"key, generic json", `{"error":"not found"}`, func(c *Client) error { return c.RevokeKey(context.Background(), "k") }, false},
		{"key, html page", `not json`, func(c *Client) error { _, err := c.GetKey(context.Background(), "k"); return err }, false},
		{"secret, its text", `{"error":"secret not found"}`, func(c *Client) error { return c.DeleteSecret(context.Background(), "A") }, true},
		{"secret, with a reason", `{"error":"secret not found","reason":"user-not-found"}`, func(c *Client) error { _, err := c.GetSecret(context.Background(), "A"); return err }, false},
		{"task, its text", `{"error":"task not found or does not belong to this tenant"}`, func(c *Client) error { _, err := c.GetTask(context.Background(), "t"); return err }, true},
		{"task, generic json", `{"error":"not found"}`, func(c *Client) error { return c.DeleteTask(context.Background(), "t") }, false},
		{"deployment, its text", `{"error":"deployment not found"}`, func(c *Client) error { _, err := c.GetDeployment(context.Background(), "d"); return err }, true},
		{"deployment, html page", `<html>Cannot GET</html>`, func(c *Client) error { return c.DeleteDeployment(context.Background(), "d") }, false},
		{"hook, its text", `{"error":"not found"}`, func(c *Client) error { _, err := c.GetHook(context.Background(), "h"); return err }, true},
		{"hook, with a reason", `{"error":"not found","reason":"user-not-found"}`, func(c *Client) error { return c.DeleteHook(context.Background(), "h") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(tc.body))
			})
			err := tc.call(c)
			if err == nil || IsGone(err) != tc.gone {
				t.Fatalf("err = %v, gone = %v, want %v", err, IsGone(err), tc.gone)
			}
		})
	}
}

func TestNonJSONErrorKeepsStatusText(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not json"))
	})
	_, err := c.GetKey(context.Background(), "key_1")
	if err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	followed := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed = true
		_, _ = w.Write([]byte(`{"name":"A","updated_at":"x"}`))
	}))
	t.Cleanup(elsewhere.Close)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/secrets/A", http.StatusFound)
	})
	_, err := c.GetSecret(context.Background(), "A")
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("err = %v, want an HTTP 302 error", err)
	}
	if followed {
		t.Fatal("the redirect was followed")
	}
}

func TestDecideRetry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   attemptOutcome
		want retryDecision
	}{
		{"network error", attemptOutcome{transportFailed: true, now: now}, retryDecision{true, time.Second}},
		{"network error, last try", attemptOutcome{attempt: maxAttempts - 1, transportFailed: true}, noRetry},
		{"429 no header backs off", attemptOutcome{attempt: 1, status: 429, now: now}, retryDecision{true, 2 * time.Second}},
		{"429 quota refusal fails at once", attemptOutcome{status: 429, reason: "api-key-quota", now: now}, noRetry},
		{"429 quota refusal with header", attemptOutcome{status: 429, reason: "api-key-quota", retryAfter: "1", now: now}, retryDecision{true, time.Second}},
		{"429 header capped", attemptOutcome{status: 429, retryAfter: "3600", now: now}, retryDecision{true, maxWait}},
		{"503 with date", attemptOutcome{status: 503, retryAfter: now.Add(5 * time.Second).Format(http.TimeFormat), now: now}, retryDecision{true, 5 * time.Second}},
		{"503 without header", attemptOutcome{status: 503, now: now}, noRetry},
		{"409 in progress on a repeat", attemptOutcome{attempt: 1, status: 409, idempotent: true, now: now}, retryDecision{true, 2 * time.Second}},
		{"409 on the first try", attemptOutcome{status: 409, idempotent: true, now: now}, noRetry},
		{"409 name taken", attemptOutcome{attempt: 1, status: 409, reason: ReasonNameTaken, idempotent: true, now: now}, noRetry},
		{"404", attemptOutcome{status: 404, now: now}, noRetry},
		{"500", attemptOutcome{status: 500, now: now}, noRetry},
	}
	for _, tc := range cases {
		if got := decideRetry(tc.in); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestCheckEndpoint(t *testing.T) {
	ok := map[string]string{
		"https://api.example.test":  "https://api.example.test",
		"https://api.example.test/": "https://api.example.test",
		"http://localhost:8180":     "http://localhost:8180",
		"http://127.0.0.1:8180":     "http://127.0.0.1:8180",
		"http://[::1]:8180":         "http://[::1]:8180",
	}
	for in, want := range ok {
		got, err := CheckEndpoint(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	_, err := CheckEndpoint("http://admin:fake-password@api.example.test")
	if err == nil || strings.Contains(err.Error(), "fake-password") || strings.Contains(err.Error(), "admin") {
		t.Errorf("user info echoed or accepted: %v", err)
	}
	_, err = CheckEndpoint("https://admin:fake-password@api.example.test")
	if err == nil || strings.Contains(err.Error(), "fake-password") || strings.Contains(err.Error(), "admin") {
		t.Errorf("user info echoed or accepted: %v", err)
	}
	for _, in := range []string{"http://api.example.test", "http://127.1", "ftp://x.test", "api.example.test", "https://u:p@x.test", "https://x.test?a=1", ""} {
		if _, err := CheckEndpoint(in); err == nil {
			t.Errorf("%q: want a refusal", in)
		}
	}
}

func TestFillPath(t *testing.T) {
	got, err := fillPath(pathGroupUser, "ops", "a/b")
	if err != nil || got != "/groups/ops/users/a%2Fb" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", ".", ".."} {
		if _, err := fillPath(pathUserByName, bad); err == nil {
			t.Errorf("%q: want a refusal", bad)
		}
	}
}

func TestListTaskDeploymentsReadsEveryPage(t *testing.T) {
	const total = 2*deploymentPageSize + 5
	var queries []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		start := 0
		if after := r.URL.Query().Get("after_id"); after != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(after, "dep-"))
			start++
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var rows []string
		for i := start; i < total && len(rows) < limit; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"dep-%d","status":"READY"}`, i))
		}
		_, _ = w.Write([]byte(`{"deployments":[` + strings.Join(rows, ",") + `]}`))
	})
	got, err := c.ListTaskDeployments(context.Background(), "report")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total || got[0].ID != "dep-0" || got[total-1].ID != fmt.Sprintf("dep-%d", total-1) {
		t.Fatalf("got %d rows", len(got))
	}
	if len(queries) != 3 || !strings.Contains(queries[1], "after_id=dep-99") || !strings.Contains(queries[0], "task_id=report") {
		t.Fatalf("queries = %v", queries)
	}
}

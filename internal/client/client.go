// SPDX-License-Identifier: MPL-2.0

// Package client is a small typed HTTP client for the Ignitify tenant API.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	headerAuthorization   = "Authorization"
	headerUserAgent       = "User-Agent"
	headerContentType     = "Content-Type"
	headerAccept          = "Accept"
	headerIdempotencyKey  = "Idempotency-Key"
	headerRetryAfter      = "Retry-After"
	bearerPrefix          = "Bearer "
	contentTypeJSON       = "application/json"
	idempotencyKeyPrefix  = "tf-"
	idempotencyKeyRandLen = 16
	maxBodyBytes          = 4 * 1024 * 1024
	defaultRequestTimeout = 60 * time.Second
)

// Config is what a Client needs.
type Config struct {
	// Endpoint is the API base URL; CheckEndpoint must accept it.
	Endpoint string
	// APIKey is sent as a bearer token on every request.
	APIKey string
	// UserAgent names the client, for example `terraform-provider-ignitify/1.2.3`.
	UserAgent string
}

// Client calls the tenant API. Safe for concurrent use.
type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
	// sleep waits between tries; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
	// newIdempotencyKey makes the key of one write; tests replace it.
	newIdempotencyKey func() (string, error)
	now               func() time.Time
}

// New checks the endpoint and returns a Client.
func New(cfg Config) (*Client, error) {
	base, err := CheckEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if cfg.APIKey == "" {
		return nil, errors.New("an API key is required")
	}
	// The API never redirects. A redirect is answered as an error and never
	// followed, so the key is never sent to the place a 3xx points at. Each
	// try has its own deadline (request.timeout), so the client sets none.
	httpClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Client{
		baseURL:           base,
		apiKey:            cfg.APIKey,
		userAgent:         cfg.UserAgent,
		http:              httpClient,
		sleep:             sleepContext,
		newIdempotencyKey: randomIdempotencyKey,
		now:               time.Now,
	}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// randomIdempotencyKey makes a key the server accepts (8 to 255 of `[A-Za-z0-9._:-]`).
func randomIdempotencyKey() (string, error) {
	buf := make([]byte, idempotencyKeyRandLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("make an idempotency key: %w", err)
	}
	return idempotencyKeyPrefix + hex.EncodeToString(buf), nil
}

// request is one API call.
type request struct {
	method string
	path   string
	// body is sent as JSON; nil sends no body.
	body any
	// raw, when set, is sent as is with contentType instead of body.
	raw         []byte
	contentType string
	// timeout bounds one try; zero means defaultRequestTimeout.
	timeout time.Duration
	// out receives the decoded 2xx answer; nil ignores the body.
	out any
	// gone is the route's "no such object" 404; the zero rule for routes with none.
	gone goneRule
}

// do sends one API call. A write (any method but GET) carries one
// Idempotency-Key, the same on every try, so a retry after a lost answer never
// acts twice. It returns an *APIError for a non-2xx answer.
func (c *Client) do(ctx context.Context, r request) error {
	payload, contentType, err := r.payload()
	if err != nil {
		return err
	}
	timeout := r.timeout
	if timeout == 0 {
		timeout = defaultRequestTimeout
	}
	idemKey := ""
	if r.method != http.MethodGet {
		if idemKey, err = c.newIdempotencyKey(); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		status, header, body, sendErr := c.send(ctx, tryInput{method: r.method, path: r.path, payload: payload, contentType: contentType, idemKey: idemKey, timeout: timeout})
		if sendErr == nil && status >= 200 && status < 300 {
			return decodeBody(r.method, r.path, body, r.out)
		}
		var apiErr *APIError
		if sendErr == nil {
			apiErr = parseAPIError(r.method, r.path, status, body, r.gone)
		}
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), sendErr)
		}
		decision := decideRetry(attemptOutcome{
			attempt:         attempt,
			transportFailed: sendErr != nil,
			status:          status,
			retryAfter:      header.Get(headerRetryAfter),
			reason:          reasonOf(apiErr),
			idempotent:      idemKey != "",
			now:             c.now(),
		})
		if !decision.retry {
			if sendErr != nil {
				return fmt.Errorf("%s %s: %w", r.method, r.path, sendErr)
			}
			return apiErr
		}
		if err := c.sleep(ctx, decision.wait); err != nil {
			return err
		}
	}
}

func reasonOf(apiErr *APIError) string {
	if apiErr == nil {
		return ""
	}
	return apiErr.Reason
}

// payload is the request body and its content type: the raw bytes, or the
// body as JSON, or nothing.
func (r request) payload() ([]byte, string, error) {
	if r.raw != nil {
		return r.raw, r.contentType, nil
	}
	if r.body == nil {
		return nil, "", nil
	}
	payload, err := json.Marshal(r.body)
	if err != nil {
		return nil, "", fmt.Errorf("encode request body: %w", err)
	}
	return payload, contentTypeJSON, nil
}

func decodeBody(method, path string, body []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s %s: decode answer: %w", method, path, err)
	}
	return nil
}

// tryInput is one HTTP try of a request.
type tryInput struct {
	method      string
	path        string
	payload     []byte
	contentType string
	idemKey     string
	timeout     time.Duration
}

// send makes one HTTP try and reads the whole (bounded) answer.
func (c *Client) send(ctx context.Context, in tryInput) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, in.timeout)
	defer cancel()
	var bodyReader io.Reader
	if in.payload != nil {
		bodyReader = bytes.NewReader(in.payload)
	}
	req, err := http.NewRequestWithContext(ctx, in.method, c.baseURL+in.path, bodyReader)
	if err != nil {
		return 0, http.Header{}, nil, err
	}
	req.Header.Set(headerAuthorization, bearerPrefix+c.apiKey)
	req.Header.Set(headerAccept, contentTypeJSON)
	if c.userAgent != "" {
		req.Header.Set(headerUserAgent, c.userAgent)
	}
	if in.payload != nil {
		req.Header.Set(headerContentType, in.contentType)
	}
	if in.idemKey != "" {
		req.Header.Set(headerIdempotencyKey, in.idemKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, http.Header{}, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, http.Header{}, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

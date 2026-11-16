// SPDX-License-Identifier: MPL-2.0

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// The `reason` values of an access refusal (body `{ error, reason, needed? }`).
const (
	ReasonPermissionDenied  = "permission-denied"
	ReasonOwnerRequired     = "owner-required"
	ReasonUserNotFound      = "user-not-found"
	ReasonGroupNotFound     = "group-not-found"
	ReasonNameTaken         = "name-taken"
	ReasonUnknownPermission = "unknown-permission"
)

// APIError is a non-2xx answer of the API.
type APIError struct {
	Method string
	Path   string
	Status int
	// Message is the server's `error` text, or the HTTP status text when the body has none.
	Message string
	// Reason is the server's machine `reason`, empty when the answer has none.
	Reason string
	// Needed names the missing permission of a `permission-denied` refusal.
	Needed string
	// apiShaped is true when the body was the API's JSON error shape (`{ "error": … }`).
	// An unknown route or a proxy in the way answers another body.
	apiShaped bool
	// gone is true when the answer is the route's own "no such object" 404 (goneRule).
	gone bool
}

func (e *APIError) Error() string {
	out := fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
	if e.Reason != "" {
		out += fmt.Sprintf(" (reason: %s)", e.Reason)
	}
	return out
}

type errorBody struct {
	Error  string `json:"error"`
	Reason string `json:"reason"`
	Needed string `json:"needed"`
}

// goneRule names the exact 404 a route answers when its object does not
// exist: a JSON error body with this reason, or (for routes whose 404 has no
// reason) with this error text. Only that answer means "gone"; any other 404
// (an unknown route, a proxy page) is an error, so a wrong path never drops an
// object from the state. The zero rule matches nothing.
type goneRule struct {
	reason  string
	message string
}

// The error texts of the key and secret routes' "no such object" 404, which carry no reason.
const (
	GoneKeyMessage    = "no live key with that id in this tenant"
	GoneSecretMessage = "secret not found"
)

// The "no such object" 404 of each route the provider reads or deletes.
var (
	goneUser   = goneRule{reason: ReasonUserNotFound}
	goneGroup  = goneRule{reason: ReasonGroupNotFound}
	goneKey    = goneRule{message: GoneKeyMessage}
	goneSecret = goneRule{message: GoneSecretMessage}
)

// matches reports whether an error answer is this route's own 404. Pure.
func (g goneRule) matches(e *APIError) bool {
	if e.Status != http.StatusNotFound || !e.apiShaped {
		return false
	}
	switch {
	case g.reason != "":
		return e.Reason == g.reason
	case g.message != "":
		return e.Reason == "" && e.Message == g.message
	default:
		return false
	}
}

// parseAPIError builds the error of a non-2xx answer from its status and body,
// and marks it gone when it is the route's own 404. A body that is not the JSON
// error shape keeps the HTTP status text. Pure.
func parseAPIError(method, path string, status int, body []byte, gone goneRule) *APIError {
	var parsed errorBody
	shaped := json.Unmarshal(body, &parsed) == nil && parsed.Error != ""
	message := parsed.Error
	if !shaped {
		message = http.StatusText(status)
	}
	out := &APIError{Method: method, Path: path, Status: status, Message: message, Reason: parsed.Reason, Needed: parsed.Needed, apiShaped: shaped}
	out.gone = gone.matches(out)
	return out
}

// asAPIError returns the APIError in err's chain, or nil.
func asAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return nil
}

// IsGone reports whether err is the route's own "no such object" 404: a
// read removes the object from the state, a delete is done.
func IsGone(err error) bool {
	apiErr := asAPIError(err)
	return apiErr != nil && apiErr.gone
}

// HasReason reports whether err is an API error answer with this refusal reason.
func HasReason(err error, reason string) bool {
	apiErr := asAPIError(err)
	return apiErr != nil && reason != "" && apiErr.apiShaped && apiErr.Reason == reason
}

// IsOwnerRequired reports whether err is the refusal a user key gets on an
// owner-only route.
func IsOwnerRequired(err error) bool {
	return HasReason(err, ReasonOwnerRequired)
}

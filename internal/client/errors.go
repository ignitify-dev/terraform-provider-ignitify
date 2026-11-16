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
	// gone is true when the answer is the route's own "no such object" 404 (answerRule).
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

// The `reason` codes of the error answers the provider branches on.
const (
	ReasonKeyNotFound           = "key-not-found"
	ReasonSecretNotFound        = "secret-not-found"
	ReasonTaskNotFound          = "task-not-found"
	ReasonDeploymentNotFound    = "deployment-not-found"
	ReasonHookNotFound          = "hook-not-found"
	ReasonTaskSlugTaken         = "task-slug-taken"
	ReasonSecretExists          = "secret-exists"
	ReasonRateLimited           = "rate-limited"
	ReasonAuthThrottled         = "auth-throttled"
	ReasonIdempotencyInProgress = "idempotency-in-progress"
	ReasonDeploymentRace        = "deployment-race"
)

// answerRule names one exact error answer of a route: this status with a JSON
// error body that carries this reason, or (from a server that sends no reason
// for it) no reason and exactly this error text. Any other answer with the
// same status (an unknown route, a proxy page) does not match. The zero rule
// matches nothing.
type answerRule struct {
	status  int
	reason  string
	message string
}

// The error texts of answers that older servers send without a reason.
const (
	GoneKeyMessage        = "no live key with that id in this tenant"
	GoneSecretMessage     = "secret not found"
	GoneTaskMessage       = "task not found or does not belong to this tenant"
	GoneDeploymentMessage = "deployment not found"
	GoneHookMessage       = "not found"
	// TaskSlugTakenMessage is the 409 of a task create on a live task's slug.
	TaskSlugTakenMessage = "a task with this slug already exists for this tenant"
)

// goneAnswer is a route's "no such object" 404. Pure.
func goneAnswer(reason, message string) answerRule {
	return answerRule{status: http.StatusNotFound, reason: reason, message: message}
}

// The "no such object" 404 of each route the provider reads or deletes. Only
// that answer means "gone", so a wrong path never drops an object from the state.
var (
	goneUser       = goneAnswer(ReasonUserNotFound, "")
	goneGroup      = goneAnswer(ReasonGroupNotFound, "")
	goneKey        = goneAnswer(ReasonKeyNotFound, GoneKeyMessage)
	goneSecret     = goneAnswer(ReasonSecretNotFound, GoneSecretMessage)
	goneTask       = goneAnswer(ReasonTaskNotFound, GoneTaskMessage)
	goneDeployment = goneAnswer(ReasonDeploymentNotFound, GoneDeploymentMessage)
	goneHook       = goneAnswer(ReasonHookNotFound, GoneHookMessage)
)

// taskSlugTaken is the refusal of a task create on the slug of a live task.
var taskSlugTaken = answerRule{status: http.StatusConflict, reason: ReasonTaskSlugTaken, message: TaskSlugTakenMessage}

// secretExists is the refusal of a create-only secret write on a name that exists.
var secretExists = answerRule{status: http.StatusPreconditionFailed, reason: ReasonSecretExists}

// matches reports whether an error answer is this rule's answer. Pure.
func (g answerRule) matches(e *APIError) bool {
	if g.status == 0 || e.Status != g.status || !e.apiShaped {
		return false
	}
	byReason := g.reason != "" && e.Reason == g.reason
	byOldText := g.message != "" && e.Reason == "" && e.Message == g.message
	return byReason || byOldText
}

// parseAPIError builds the error of a non-2xx answer from its status and body,
// and marks it gone when it is the route's own 404. A body that is not the JSON
// error shape keeps the HTTP status text. Pure.
func parseAPIError(method, path string, status int, body []byte, gone answerRule) *APIError {
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

// IsTaskSlugTaken reports whether err is the refusal of a task create on the
// slug of a live task.
func IsTaskSlugTaken(err error) bool {
	apiErr := asAPIError(err)
	return apiErr != nil && taskSlugTaken.matches(apiErr)
}

// IsSecretExists reports whether err is the refusal of a create-only secret
// write on a name that already exists.
func IsSecretExists(err error) bool {
	apiErr := asAPIError(err)
	return apiErr != nil && secretExists.matches(apiErr)
}

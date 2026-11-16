// SPDX-License-Identifier: MPL-2.0

package client

import "encoding/json"

// The JSON bodies of the tenant API routes this provider calls. Timestamps are
// ISO 8601 strings. Only the fields the provider reads are declared; the server
// may send more.

// AccessUser is one user as the API answers it.
type AccessUser struct {
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
}

// Group is a group with its permissions and its users' names, as every group
// read and group write answers it.
type Group struct {
	ResourceID  string   `json:"resource_id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Users       []string `json:"users"`
	CreatedAt   string   `json:"created_at"`
}

// AccessKey is one key as a read shows it. It never holds the raw key.
// User is nil for an owner key.
type AccessKey struct {
	ResourceID string  `json:"resource_id"`
	User       *string `json:"user"`
	Label      *string `json:"label"`
	CreatedAt  string  `json:"created_at"`
}

// KeyCreated is the answer of a key create. RawKey is shown once; a replay of
// the same idempotent request answers RawKey nil and SecretRedacted true.
type KeyCreated struct {
	ResourceID     string  `json:"resource_id"`
	User           string  `json:"user"`
	Label          string  `json:"label"`
	RawKey         *string `json:"raw_key"`
	SecretRedacted bool    `json:"secret_redacted"`
}

// Secret is a secret's name and last write time. The value is never readable.
type Secret struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
}

// Whoami is the answer of the identity route.
type Whoami struct {
	TenantID   string        `json:"tenant_id"`
	ResourceID string        `json:"resource_id"`
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Tier       WhoamiTier    `json:"tier"`
	Limits     WhoamiLimits  `json:"limits"`
	Usage      WhoamiUsage   `json:"usage"`
	Access     *WhoamiAccess `json:"access"`
	Policy     WhoamiPolicy  `json:"policy"`
}

// WhoamiPolicy holds the entitlement flags the provider checks before a
// write. A nil flag is one the server did not answer.
type WhoamiPolicy struct {
	MayUseWebhooks *bool `json:"may_use_webhooks"`
}

// WhoamiTier names the tenant's tier.
type WhoamiTier struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

// WhoamiLimits are the summary limits. A nil limit is unlimited.
type WhoamiLimits struct {
	MaxTasks           *int64 `json:"max_tasks"`
	MaxRunningVMs      *int64 `json:"max_running_vms"`
	MaxUnits           *int64 `json:"max_units"`
	MaxPinnedVMs       *int64 `json:"max_pinned_vms"`
	MaxTotalImageBytes *int64 `json:"max_total_image_bytes"`
	MaxStorageMiB      int64  `json:"max_storage_mib"`
	MaxPayloadBytes    int64  `json:"max_payload_bytes"`
	MaxResultBytes     int64  `json:"max_result_bytes"`
}

// WhoamiUsage is what the tenant holds now.
type WhoamiUsage struct {
	TaskCount       int64 `json:"task_count"`
	ActiveRuns      int64 `json:"active_runs"`
	ActiveUnits     int64 `json:"active_units"`
	DeploymentCount int64 `json:"deployment_count"`
	TotalImageBytes int64 `json:"total_image_bytes"`
	PinnedVMs       int64 `json:"pinned_vms"`
}

// WhoamiAccess is the calling key and what it may do. User is nil for an owner key.
type WhoamiAccess struct {
	Key         string   `json:"key"`
	User        *string  `json:"user"`
	Groups      []string `json:"groups"`
	Permissions []string `json:"permissions"`
}

// Task is one live task.
type Task struct {
	ID         string `json:"id"`
	ResourceID string `json:"resource_id"`
	Slug       string `json:"slug"`
	CreatedAt  string `json:"created_at"`
}

// TaskCreated is the answer of a task create. Revived is true when the slug
// was a task this tenant deleted and that same task is back.
type TaskCreated struct {
	TaskID     string `json:"task_id"`
	ResourceID string `json:"resource_id"`
	Revived    bool   `json:"revived"`
}

// Deployment is one live deployment as a read shows it. Execution is the
// declared `execution` block as stored (JSON `null` when none); Secrets are the
// declared secret names, never values.
type Deployment struct {
	ID            string          `json:"id"`
	ResourceID    string          `json:"resource_id"`
	TaskID        string          `json:"task_id"`
	TaskSlug      string          `json:"task_slug"`
	ContentHash   string          `json:"content_hash"`
	Status        string          `json:"status"`
	Secrets       []string        `json:"secrets"`
	Execution     json.RawMessage `json:"execution"`
	FailureReason *string         `json:"failure_reason"`
	NotifyURL     *string         `json:"notify_url"`
	CreatedAt     string          `json:"created_at"`
}

// The deployment statuses.
const (
	DeploymentBuilding = "BUILDING"
	DeploymentReady    = "READY"
	DeploymentFailed   = "FAILED"
)

// DeploymentCreated is the answer of a deploy: 202 when a build was queued,
// 200 with Reused when the same content is already READY or building.
type DeploymentCreated struct {
	DeploymentID string `json:"deployment_id"`
	Status       string `json:"status"`
	Reused       bool   `json:"reused"`
}

type deploymentList struct {
	Deployments []Deployment `json:"deployments"`
}

// HookCreated is the answer of a hook create. Secret is shown once; a replay
// of the same idempotent request answers Secret nil and SecretRedacted true.
type HookCreated struct {
	ResourceID     string  `json:"resource_id"`
	HookID         string  `json:"hook_id"`
	Secret         *string `json:"secret"`
	SecretRedacted bool    `json:"secret_redacted"`
}

// Hook is one live hook as a read shows it; never the secret. TaskID is the
// task's server id.
type Hook struct {
	TaskID     string `json:"task_id"`
	ResourceID string `json:"resource_id"`
	HookID     string `json:"hook_id"`
	CreatedAt  string `json:"created_at"`
}

// The request bodies.

type userCreateRequest struct {
	Name string `json:"name"`
}

type groupCreateRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type keyCreateRequest struct {
	User  string `json:"user"`
	Label string `json:"label"`
}

type secretSetRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type taskCreateRequest struct {
	Slug string `json:"slug"`
}

// notifyRequest sets a notify target (URL and secret) or clears it (URL nil).
type notifyRequest struct {
	URL    *string `json:"url"`
	Secret string  `json:"secret,omitempty"`
}

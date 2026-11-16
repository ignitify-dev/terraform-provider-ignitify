// SPDX-License-Identifier: MPL-2.0

package client

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

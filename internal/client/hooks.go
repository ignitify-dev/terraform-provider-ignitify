// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// A hook belongs to a task and starts a run of the task's newest ready
// deployment when its URL is called. It is addressed by its resource id.

// CreateHook creates a hook on a task. The secret is in the answer once.
func (c *Client) CreateHook(ctx context.Context, taskSlug string) (HookCreated, error) {
	var out HookCreated
	path, err := fillPath(pathTaskHooks, taskSlug)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodPost, path: path, out: &out})
	return out, err
}

// GetHook reads a live hook; never the secret.
func (c *Client) GetHook(ctx context.Context, id string) (Hook, error) {
	var out Hook
	path, err := fillPath(pathHookByID, id)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneHook})
	return out, err
}

// DeleteHook deletes a hook; its URL stops working.
func (c *Client) DeleteHook(ctx context.Context, id string) error {
	path, err := fillPath(pathHookByID, id)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneHook})
}

// HookURL is the public URL that triggers a hook: the endpoint joined with the
// hook's trigger path.
func (c *Client) HookURL(hookID string) (string, error) {
	path, err := fillPath(pathHookTrigger, hookID)
	if err != nil {
		return "", err
	}
	return c.baseURL + path, nil
}

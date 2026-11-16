// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// A task is addressed by its slug (the routes also take its id).

// CreateTask creates a task. A slug this tenant deleted brings back the same task.
func (c *Client) CreateTask(ctx context.Context, slug string) (TaskCreated, error) {
	var out TaskCreated
	err := c.do(ctx, request{method: http.MethodPost, path: pathTasks, body: taskCreateRequest{Slug: slug}, out: &out})
	return out, err
}

// GetTask reads a live task.
func (c *Client) GetTask(ctx context.Context, slug string) (Task, error) {
	var out Task
	path, err := fillPath(pathTaskByRef, slug)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneTask})
	return out, err
}

// DeleteTask deletes a task with its deployments, runs and hooks.
func (c *Client) DeleteTask(ctx context.Context, slug string) error {
	path, err := fillPath(pathTaskByRef, slug)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneTask})
}

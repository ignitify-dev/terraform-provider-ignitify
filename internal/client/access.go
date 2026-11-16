// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// The access routes: users, groups and keys. An owner key only; a user key gets
// a 403 with reason `owner-required`. A user or group is addressed by its name,
// a key by its resource id.

// CreateUser creates a user.
func (c *Client) CreateUser(ctx context.Context, name string) (AccessUser, error) {
	var out AccessUser
	err := c.do(ctx, request{method: http.MethodPost, path: pathUsers, body: userCreateRequest{Name: name}, out: &out})
	return out, err
}

// GetUser reads a live user.
func (c *Client) GetUser(ctx context.Context, name string) (AccessUser, error) {
	var out AccessUser
	path, err := fillPath(pathUserByName, name)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneUser})
	return out, err
}

// DeleteUser removes a user. The server revokes all its keys and takes it out
// of every group in the same step.
func (c *Client) DeleteUser(ctx context.Context, name string) error {
	path, err := fillPath(pathUserByName, name)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneUser})
}

// CreateGroup creates a group with at least one permission.
func (c *Client) CreateGroup(ctx context.Context, name string, permissions []string) (Group, error) {
	var out Group
	err := c.do(ctx, request{method: http.MethodPost, path: pathGroups, body: groupCreateRequest{Name: name, Permissions: permissions}, out: &out})
	return out, err
}

// GetGroup reads a group with its permissions and users.
func (c *Client) GetGroup(ctx context.Context, name string) (Group, error) {
	var out Group
	path, err := fillPath(pathGroupByName, name)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneGroup})
	return out, err
}

// DeleteGroup deletes a group and its memberships; users and keys stay.
func (c *Client) DeleteGroup(ctx context.Context, name string) error {
	path, err := fillPath(pathGroupByName, name)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneGroup})
}

// GrantPermission adds a permission to a group. Granting a held permission changes nothing.
func (c *Client) GrantPermission(ctx context.Context, group, permission string) (Group, error) {
	return c.groupChange(ctx, http.MethodPut, pathGroupPermission, group, permission)
}

// RevokePermission removes a permission from a group.
func (c *Client) RevokePermission(ctx context.Context, group, permission string) (Group, error) {
	return c.groupChange(ctx, http.MethodDelete, pathGroupPermission, group, permission)
}

// AddGroupUser adds a user to a group. Adding a member changes nothing.
func (c *Client) AddGroupUser(ctx context.Context, group, user string) (Group, error) {
	return c.groupChange(ctx, http.MethodPut, pathGroupUser, group, user)
}

// RemoveGroupUser takes a user out of a group.
func (c *Client) RemoveGroupUser(ctx context.Context, group, user string) (Group, error) {
	return c.groupChange(ctx, http.MethodDelete, pathGroupUser, group, user)
}

// groupChange sends one group write; every group write answers the group as it is now.
func (c *Client) groupChange(ctx context.Context, method, pattern, group, item string) (Group, error) {
	var out Group
	path, err := fillPath(pattern, group, item)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: method, path: path, out: &out})
	return out, err
}

// CreateKey creates a key for a user. The raw key is in the answer once.
func (c *Client) CreateKey(ctx context.Context, user, label string) (KeyCreated, error) {
	var out KeyCreated
	err := c.do(ctx, request{method: http.MethodPost, path: pathKeys, body: keyCreateRequest{User: user, Label: label}, out: &out})
	return out, err
}

// GetKey reads one live key; never the raw key.
func (c *Client) GetKey(ctx context.Context, id string) (AccessKey, error) {
	var out AccessKey
	path, err := fillPath(pathKeyByID, id)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneKey})
	return out, err
}

// RevokeKey revokes a live key.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	path, err := fillPath(pathKeyByID, id)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneKey})
}

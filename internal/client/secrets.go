// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// SetSecret stores a secret value. It creates the name or replaces its value.
func (c *Client) SetSecret(ctx context.Context, name, value string) error {
	return c.do(ctx, request{method: http.MethodPost, path: pathSecrets, body: secretSetRequest{Name: name, Value: value}})
}

// GetSecret reads a secret's name and last write time; never the value.
func (c *Client) GetSecret(ctx context.Context, name string) (Secret, error) {
	var out Secret
	path, err := fillPath(pathSecretByName, name)
	if err != nil {
		return out, err
	}
	err = c.do(ctx, request{method: http.MethodGet, path: path, out: &out, gone: goneSecret})
	return out, err
}

// DeleteSecret removes a secret.
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	path, err := fillPath(pathSecretByName, name)
	if err != nil {
		return err
	}
	return c.do(ctx, request{method: http.MethodDelete, path: path, gone: goneSecret})
}

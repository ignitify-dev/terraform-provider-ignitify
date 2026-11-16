// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
)

// Whoami reads the tenant, the calling key and the quota snapshot.
func (c *Client) Whoami(ctx context.Context) (Whoami, error) {
	var out Whoami
	err := c.do(ctx, request{method: http.MethodGet, path: pathWhoami, out: &out})
	return out, err
}

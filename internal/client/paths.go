// SPDX-License-Identifier: MPL-2.0

package client

import (
	"fmt"
	"net/url"
)

// The tenant API paths this provider calls. A path parameter is a user or group
// name, a key id or a secret name.
const (
	pathWhoami          = "/whoami"
	pathUsers           = "/users"
	pathUserByName      = "/users/%s"
	pathGroups          = "/groups"
	pathGroupByName     = "/groups/%s"
	pathGroupPermission = "/groups/%s/permissions/%s"
	pathGroupUser       = "/groups/%s/users/%s"
	pathKeys            = "/keys"
	pathKeyByID         = "/keys/%s"
	pathSecrets         = "/secrets"
	pathSecretByName    = "/secrets/%s"
)

// fillPath renders a path pattern with its parameters. Each parameter is
// percent-encoded. An empty parameter or a dot segment ("." or "..") is refused:
// a URL parser resolves a dot segment instead of sending it, which would aim the
// request at another route. Pure.
func fillPath(pattern string, params ...string) (string, error) {
	escaped := make([]any, 0, len(params))
	for i, p := range params {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("path %s: parameter %d (%q) is not a valid path segment", pattern, i+1, p)
		}
		escaped = append(escaped, url.PathEscape(p))
	}
	return fmt.Sprintf(pattern, escaped...), nil
}

// SPDX-License-Identifier: MPL-2.0

package client

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	schemeHTTPS   = "https"
	schemeHTTP    = "http"
	hostLocalhost = "localhost"
	hostIPv6Loop  = "::1"
)

// ipv4LoopbackPattern matches a full IPv4 address in 127.0.0.0/8.
var ipv4LoopbackPattern = regexp.MustCompile(`^127(?:\.\d{1,3}){3}$`)

// isLoopbackHost reports whether hostname is this machine: `localhost`, an IPv4
// address in 127.0.0.0/8, or IPv6 `::1`. Other spellings are refused, so the
// rule fails closed. Pure.
func isLoopbackHost(hostname string) bool {
	h := strings.ToLower(hostname)
	return h == hostLocalhost || h == hostIPv6Loop || ipv4LoopbackPattern.MatchString(h)
}

// CheckEndpoint returns the API base URL without a trailing slash, or an error
// when a key must not be sent to it. A key is a bearer token: it goes only to an
// https URL, or over plain http to this machine. Pure.
func CheckEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("the endpoint is not an absolute URL")
	}
	shown := withoutUserInfo(u)
	if u.User != nil {
		return "", fmt.Errorf("endpoint %q must not carry user info; pass the key in api_key", shown)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("endpoint %q must not carry a query or a fragment", shown)
	}
	scheme := strings.ToLower(u.Scheme)
	secure := scheme == schemeHTTPS || (scheme == schemeHTTP && isLoopbackHost(u.Hostname()))
	if !secure {
		return "", fmt.Errorf("endpoint %q is not https: an API key is sent only over https, or over plain http to this machine (localhost, 127.0.0.0/8, ::1)", shown)
	}
	return strings.TrimRight(raw, "/"), nil
}

// withoutUserInfo renders u with no user name or password, so an error never
// echoes a credential. It leaves u untouched. Pure.
func withoutUserInfo(u *url.URL) string {
	clean := *u
	clean.User = nil
	return clean.String()
}

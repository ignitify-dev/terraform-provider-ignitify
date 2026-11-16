// SPDX-License-Identifier: MPL-2.0

//go:build !unix

package source

import "os"

// openNoFollow opens a file for reading. This platform has no no-follow open;
// the walk still compares the opened file with the listed one.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

// SPDX-License-Identifier: MPL-2.0

//go:build unix

package source

import (
	"errors"
	"os"
	"syscall"
)

// openNoFollow opens a file for reading without following a link: a link at
// the path fails the open (errLink). O_NONBLOCK makes a FIFO open at once, so
// the regular-file check refuses it instead of the walk blocking.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, errLink
	}
	return f, err
}

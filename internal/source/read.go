// SPDX-License-Identifier: MPL-2.0

package source

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// errTooLarge is a read refused because the file is larger than an upload
// may be.
var errTooLarge = fmt.Errorf("the file is larger than the %d MiB upload limit", MaxUploadBytes/bytesPerMiB)

// errNotRegular is an open refused because the path is not a regular file
// (a FIFO, a socket, a device node or a folder).
var errNotRegular = errors.New("the path is not a regular file")

// openListed opens the regular file that listed describes, without following
// a link. The opened file must still be the listed one, so a path swapped
// after the listing is refused as a link. It returns errLink, errNotRegular,
// or the open error (fs.ErrNotExist for a file removed since the listing).
func openListed(path string, listed fs.FileInfo) (*os.File, fs.FileInfo, error) {
	if listed.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, errLink
	}
	if !listed.Mode().IsRegular() {
		return nil, nil, errNotRegular
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !os.SameFile(listed, opened) {
		f.Close()
		return nil, nil, errLink
	}
	if !opened.Mode().IsRegular() {
		f.Close()
		return nil, nil, errNotRegular
	}
	return f, opened, nil
}

// readRegularFile reads a regular file without following a link and without
// blocking on a FIFO, and never holds more than MaxUploadBytes of it. It
// returns errLink, errNotRegular, errTooLarge, or the read error
// (fs.ErrNotExist when there is no file).
func readRegularFile(path string) ([]byte, error) {
	listed, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	f, _, err := openListed(path, listed)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// One byte past the bound is enough to know the bound is crossed.
	data, err := io.ReadAll(io.LimitReader(f, MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxUploadBytes {
		return nil, errTooLarge
	}
	return data, nil
}

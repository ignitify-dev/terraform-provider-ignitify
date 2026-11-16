// SPDX-License-Identifier: MPL-2.0

package source

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"time"
)

// The archive format the deploy route reads: one regular file member per
// file, in the given order, with a POSIX path. Only the exec bit of a mode
// survives into the image, so a member's mode is 0755 or 0644. Time and owner
// are zero, so the same files always give the same bytes. A long or non-ASCII
// name is written as a PAX record, never as a GNU long-name member.
const (
	modeExec  = 0o755
	modePlain = 0o644
	// digestPrefix marks a source digest.
	digestPrefix = "sha256:"
)

const (
	// tarBlock is the tar record unit: a header is one block, data is padded to whole blocks.
	tarBlock = 512
	// archiveTrailerBytes are the two zero blocks that end an archive.
	archiveTrailerBytes = 2 * tarBlock
)

// archivedSize is the bytes one member of dataLen bytes takes in the archive:
// its header and its data padded to whole blocks. A long or non-ASCII name
// adds a PAX record, which this does not count; the apply checks the exact
// archive size. Pure.
func archivedSize(dataLen int64) int64 {
	return tarBlock + (dataLen+tarBlock-1)/tarBlock*tarBlock
}

// archiveTime is the fixed member time. The Unix epoch, not Go's zero time:
// the zero time does not fit the USTAR header and would force another format.
var archiveTime = time.Unix(0, 0)

// WriteTar writes files as the archive.
func WriteTar(w io.Writer, files []File) error {
	tw := tar.NewWriter(w)
	for _, f := range files {
		if err := tw.WriteHeader(memberHeader(f)); err != nil {
			return fmt.Errorf("archive %s: %w", f.Path, err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			return fmt.Errorf("archive %s: %w", f.Path, err)
		}
	}
	return tw.Close()
}

// memberHeader is the header of one file member. Pure.
func memberHeader(f File) *tar.Header {
	mode := int64(modePlain)
	if f.Exec {
		mode = modeExec
	}
	return &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     f.Path,
		Mode:     mode,
		Size:     int64(len(f.Data)),
		ModTime:  archiveTime,
	}
}

// Pack returns the archive of files.
func Pack(files []File) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteTar(&buf, files); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest is the source digest of the archive of files, computed without
// holding the archive. It changes when a path, a file's bytes or an exec bit
// changes. It is the provider's own change marker, not the server's content
// key.
func Digest(files []File) (string, error) {
	h := sha256.New()
	if err := WriteTar(h, files); err != nil {
		return "", err
	}
	return digestOf(h), nil
}

// ArchiveDigest is the source digest of an archive Pack made. Pure.
func ArchiveDigest(archive []byte) string {
	h := sha256.New()
	h.Write(archive)
	return digestOf(h)
}

func digestOf(h hash.Hash) string {
	return digestPrefix + hex.EncodeToString(h.Sum(nil))
}

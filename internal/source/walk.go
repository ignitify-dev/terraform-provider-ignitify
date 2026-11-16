// SPDX-License-Identifier: MPL-2.0

package source

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// MaxUploadBytes is the largest upload the server accepts. The archive is an
// uncompressed tar, never smaller than its files, so a larger tree is refused
// before it is read.
const MaxUploadBytes = 64 * 1024 * 1024

const (
	bytesPerMiB = 1024 * 1024
	// execBits is any execute bit; the only part of a mode the image keeps.
	execBits = 0o111
)

// File is one selected file: its POSIX path relative to the task root, whether
// any execute bit is set, and its bytes.
type File struct {
	Path string
	Exec bool
	Data []byte
}

// Scan is the result of walking a task folder.
type Scan struct {
	// Files are the selected files, sorted by path.
	Files []File
	// Ignored are the paths the ignore file left out; a folder ends in "/".
	Ignored []string
	// Warnings must be shown, but do not stop the upload (an excluded `.env`, a private-key name).
	Warnings []string
	// Errors refuse the upload.
	Errors []string
	// Truncated is true when the walk stopped at the size bound: Files is then partial.
	Truncated bool
}

func symlinkRefusal(rel string) string {
	return rel + ": symlinks are not uploaded (a link can reach outside the task directory)"
}

func notRegularRefusal(rel string) string {
	return rel + ": not a regular file (sockets, FIFOs and device nodes are not uploaded)"
}

// ScanTree walks root and selects every file the upload carries. Excluded
// paths are left out (a file is reported as a warning), the ignore file and
// the paths its rules match are left out, and symlinks and special files are
// refused (never followed). A registry credential is refused. The walk stops
// at MaxUploadBytes.
func ScanTree(root string, rules []IgnoreRule) Scan {
	w := &walker{root: root, rules: rules, maxBytes: MaxUploadBytes, total: archiveTrailerBytes}
	if _, err := os.Stat(root); err != nil {
		w.out.Errors = append(w.out.Errors, "Task directory does not exist: "+root)
		return w.out
	}
	w.walk(root)
	sort.Slice(w.out.Files, func(i, j int) bool { return w.out.Files[i].Path < w.out.Files[j].Path })
	return w.out
}

type walker struct {
	root     string
	rules    []IgnoreRule
	maxBytes int64
	// total is the archive size so far: the members read and the end blocks.
	total int64
	out   Scan
}

// errLink is an open refused because the path is a link.
var errLink = errors.New("the path is a link")

func (w *walker) errorf(format string, args ...any) {
	w.out.Errors = append(w.out.Errors, fmt.Sprintf(format, args...))
}

func (w *walker) walk(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.errorf("%s: %v", w.rel(dir), err)
		return
	}
	for _, e := range entries {
		if w.out.Truncated {
			return
		}
		abs := filepath.Join(dir, e.Name())
		rel := w.rel(abs)
		isDir := e.IsDir()
		if skip := ExcludeReason(rel); skip != "" {
			// A folder (node_modules) is expected and dropped quietly; a file
			// such as `.env` may surprise its author, so it is named.
			if !isDir {
				w.out.Warnings = append(w.out.Warnings, "excluded "+rel+" — "+skip)
			}
			continue
		}
		if rel == IgnoreFile {
			continue
		}
		if IgnoredBy(w.rules, rel, isDir) {
			if isDir {
				rel += "/"
			}
			w.out.Ignored = append(w.out.Ignored, rel)
			continue
		}
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			w.out.Errors = append(w.out.Errors, symlinkRefusal(rel))
		case isDir:
			w.walk(abs)
		case !e.Type().IsRegular():
			w.out.Errors = append(w.out.Errors, notRegularRefusal(rel))
		default:
			w.readFile(e, abs, rel)
		}
	}
}

// rel is the POSIX path of abs relative to the root.
func (w *walker) rel(abs string) string {
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// readFile reads one regular file. The directory listing is only a hint: the
// open refuses a link, and the opened file must still be the one listed, so a
// path swapped after the listing is refused. A file removed since the listing is not part
// of the upload.
func (w *walker) readFile(e fs.DirEntry, abs, rel string) {
	listed, err := e.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		w.errorf("%s: %v", rel, err)
		return
	}
	f, err := openNoFollow(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if errors.Is(err, errLink) {
		w.out.Errors = append(w.out.Errors, symlinkRefusal(rel))
		return
	}
	if err != nil {
		w.errorf("%s: %v", rel, err)
		return
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		w.errorf("%s: %v", rel, err)
		return
	}
	if !os.SameFile(listed, opened) {
		w.out.Errors = append(w.out.Errors, symlinkRefusal(rel))
		return
	}
	if !opened.Mode().IsRegular() {
		w.out.Errors = append(w.out.Errors, notRegularRefusal(rel))
		return
	}
	// One byte past the bound is enough to know the bound is crossed, so the
	// walk never holds more than MaxUploadBytes. The bound counts the archive:
	// each member's header and padding too.
	data, err := io.ReadAll(io.LimitReader(f, w.maxBytes-w.total+1))
	if err != nil {
		w.errorf("%s: %v", rel, err)
		return
	}
	size := archivedSize(int64(len(data)))
	if w.total+size > w.maxBytes {
		w.out.Truncated = true
		w.errorf("the task directory holds more than %d MiB of files (reached at %s); the server refuses a larger upload",
			w.maxBytes/bytesPerMiB, rel)
		return
	}
	if IsCredentialFile(e.Name()) {
		if reason := CredentialReason(rel, string(data)); reason != "" {
			w.out.Errors = append(w.out.Errors, reason)
			return
		}
	}
	if warning := PrivateKeyWarning(rel); warning != "" {
		w.out.Warnings = append(w.out.Warnings, warning)
	}
	w.total += size
	w.out.Files = append(w.out.Files, File{Path: rel, Exec: opened.Mode().Perm()&execBits != 0, Data: data})
}

// SPDX-License-Identifier: MPL-2.0

// Package source selects the files of a task folder that a source upload
// carries, and packs them into the archive the deploy route accepts. The rules
// match the published CLI and the server's archive check; the shared cases in
// testdata/cases.json prove them.
package source

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// excludedDirs are directory names never uploaded, at any depth: local state,
// vendored dependencies and tool caches. The build makes them on the server.
var excludedDirs = []string{
	".git",
	".ignitify",
	"node_modules",
	"__pycache__",
	".venv",
	"venv",
	".mypy_cache",
	".pytest_cache",
}

// excludedRootDirs are build output only at the task root. Below the root the
// same name is source (a Go package `internal/dist`, a Rust module `src/target`).
var excludedRootDirs = []string{"target", "dist"}

// excludedFilePrefix names files never uploaded: `.env` and `.env.<anything>`.
// A secret baked into an image outlives the mistake that put it there.
const excludedFilePrefix = ".env"

// ExcludeReason says why a path is never uploaded, or "" to include it. The
// server refuses an archive that holds such a path, so the walk must leave it
// out. Pure.
func ExcludeReason(relPath string) string {
	segments := strings.Split(relPath, "/")
	last := len(segments) - 1
	for i, seg := range segments {
		where := "inside excluded directory"
		if i == last {
			where = "excluded directory"
		}
		if slices.Contains(excludedDirs, seg) {
			return fmt.Sprintf("%s %q", where, seg)
		}
		if i == 0 && slices.Contains(excludedRootDirs, seg) {
			return fmt.Sprintf("%s %q (build output at the task root)", where, seg)
		}
	}
	base := segments[last]
	if base == excludedFilePrefix || strings.HasPrefix(base, excludedFilePrefix+".") {
		return fmt.Sprintf("%q may hold secrets — use `secret:set` instead", base)
	}
	return ""
}

// PrivateKeyWarning is a warning, never an exclusion, for a file whose name
// says it holds a private key (`*.pem`, `*.key`, `id_rsa*` but not `*.pub`).
// Such names are also honest source (a test certificate), so the file is
// uploaded, but the author must be told. "" when the name is harmless. Pure.
func PrivateKeyWarning(relPath string) string {
	base := strings.ToLower(relPath[strings.LastIndex(relPath, "/")+1:])
	looksLikeKey := strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		(strings.HasPrefix(base, "id_rsa") && !strings.HasSuffix(base, ".pub"))
	if !looksLikeKey {
		return ""
	}
	return relPath + " looks like a private key and is uploaded with the task, then stored in the image. " +
		"If it is a secret, remove it (secrets go through `secret:set`); to keep a harmless file out, list it in `" + IgnoreFile + "`"
}

// PathRefusal says why a selected path cannot be in an archive the server
// accepts, or "": it must be relative, use "/" only, have no empty, "." or
// ".." segment, and hold no tab or newline. A file name with a backslash,
// a tab or a newline is legal on some systems and is refused here by name.
// Pure.
func PathRefusal(p string) string {
	quoted, _ := json.Marshal(p)
	switch {
	case p == "":
		return "empty path"
	case strings.Contains(p, `\`):
		return fmt.Sprintf("path contains a backslash: %s; rename the file", quoted)
	case strings.HasPrefix(p, "/"):
		return "absolute path: " + p
	case slices.ContainsFunc(strings.Split(p, "/"), func(seg string) bool { return seg == "" || seg == "." || seg == ".." }):
		return "non-canonical path: " + p
	case strings.ContainsAny(p, "\n\t"):
		return fmt.Sprintf("path contains a tab or newline: %s; rename the file", quoted)
	}
	return ""
}

// SPDX-License-Identifier: MPL-2.0

//go:build unix

package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRefusesALink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "real.txt", "x", 0o644)
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := openNoFollow(filepath.Join(root, "link.txt")); err != errLink {
		t.Fatalf("err = %v, want the link refusal", err)
	}
}

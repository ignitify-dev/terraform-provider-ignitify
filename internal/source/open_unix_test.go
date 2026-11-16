// SPDX-License-Identifier: MPL-2.0

//go:build unix

package source

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
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

func TestReadConfigDoesNotFollowALinkOrBlockOnAFIFO(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "task.yaml", "runtime: python3.12\n", 0o644)
	for name, place := range map[string]func(path string) error{
		"link": func(path string) error { return os.Symlink(filepath.Join(outside, "task.yaml"), path) },
		"fifo": func(path string) error { return syscall.Mkfifo(path, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := place(filepath.Join(root, ConfigFileNames[0])); err != nil {
				t.Fatal(err)
			}
			got, text, err := readConfig(root)
			if err != nil || got != ConfigFileNames[0] || text != nil {
				t.Fatalf("readConfig = %q, %q, %v; want the name and no text", got, text, err)
			}
		})
	}
}

func TestIgnoreFileLinkOrFIFOIsRefused(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "rules", "*.log\n", 0o644)
	for name, place := range map[string]func(path string) error{
		"link": func(path string) error { return os.Symlink(filepath.Join(outside, "rules"), path) },
		"fifo": func(path string) error { return syscall.Mkfifo(path, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := place(filepath.Join(root, IgnoreFile)); err != nil {
				t.Fatal(err)
			}
			rules, errs := readIgnoreRules(root, nil)
			if rules != nil || len(errs) != 1 || errs[0] != ignoreFileRefusal() {
				t.Fatalf("rules = %v, errs = %v; want the ignore-file refusal", rules, errs)
			}
		})
	}
}

func TestOpenListedRefusesAPathSwappedAfterTheCheck(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "checked.txt", "a", 0o644)
	writeFile(t, root, "swapped.txt", "b", 0o644)
	checked, err := os.Lstat(filepath.Join(root, "checked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// The path now names another file than the one checked.
	if _, _, err := openListed(filepath.Join(root, "swapped.txt"), checked); err != errLink {
		t.Fatalf("err = %v, want the link refusal", err)
	}
}

func TestReadConfigRefusesAnOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ConfigFileNames[0])
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file: no disk space, but one byte over the bound when read.
	if err := f.Truncate(MaxUploadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, _, err := readConfig(root); !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v, want the size refusal", err)
	}
}

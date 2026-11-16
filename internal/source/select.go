// SPDX-License-Identifier: MPL-2.0

package source

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxEntries is the most members a source archive may hold.
const MaxEntries = 20000

// Selection is a task folder's upload, or the reasons it cannot be one.
type Selection struct {
	Scan
	// Facts are what the plan-time read of the task config found.
	Facts ConfigFacts
}

// OK reports whether the upload may be sent.
func (s Selection) OK() bool { return len(s.Errors) == 0 }

// Paths lists the selected paths. Pure.
func (s Selection) Paths() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		out = append(out, f.Path)
	}
	return out
}

// Select reads a task folder and selects its upload: it finds the task
// config, reads the ignore file with the paths a deploy cannot lose, walks the
// folder, and checks each path, the runtime's lockfile, the entry file and the
// archive size.
func Select(dir string) Selection {
	configName, configText, err := readConfig(dir)
	if err != nil {
		return refusedSelection(err.Error())
	}
	facts := ReadConfigFacts(configName, configText)

	rules, errs := readIgnoreRules(dir, protectedPaths(facts.Runtime))
	if len(errs) > 0 {
		return refusedSelection(errs...)
	}
	out := Selection{Scan: ScanTree(dir, rules), Facts: facts}
	if facts.Runtime == "" {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"could not read the runtime from %s at plan time; the lockfile and entry-file checks are left to the server", configName))
	}
	for _, f := range out.Files {
		if problem := PathRefusal(f.Path); problem != "" {
			out.Errors = append(out.Errors, problem)
		}
	}
	// A walk cut short at the size bound lists only part of the tree, so a
	// presence check would name files that are there.
	if out.Truncated {
		return out
	}
	if facts.Runtime != "" {
		if lock := CheckLockfile(out.Paths(), facts.Runtime); lock != "" {
			out.Errors = append(out.Errors, lock)
		}
	}
	if entry := CheckEntry(out.Paths(), facts.Entry); entry != "" {
		out.Errors = append(out.Errors, entry)
	}
	if len(out.Files) > MaxEntries {
		out.Errors = append(out.Errors, fmt.Sprintf("the task directory holds %d files; the server accepts at most %d", len(out.Files), MaxEntries))
	}
	return out
}

func refusedSelection(errs ...string) Selection {
	return Selection{Scan: Scan{Errors: errs}}
}

// readConfig finds the root task config (the first name present) and reads
// it when it is a regular file. A config that is a link or not a regular file
// is refused later by the walk; its text is not read here.
func readConfig(dir string) (string, []byte, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("Task directory does not exist: %s", dir)
	}
	for _, name := range ConfigFileNames {
		text, err := readRegularFile(filepath.Join(dir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case errors.Is(err, errLink), errors.Is(err, errNotRegular):
			return name, nil, nil
		default:
			return name, text, err
		}
	}
	return "", nil, fmt.Errorf("the task directory has no task config (%s)", strings.Join(ConfigFileNames, ", "))
}

// readIgnoreRules reads the ignore file in the task root; no file means no
// rules. The walk never uploads the ignore file, so a link or a special file
// in its place is refused here.
func readIgnoreRules(dir string, protected []string) ([]IgnoreRule, []string) {
	text, err := readRegularFile(filepath.Join(dir, IgnoreFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, errLink), errors.Is(err, errNotRegular):
		return nil, []string{ignoreFileRefusal()}
	case err != nil:
		return nil, []string{fmt.Sprintf("%s: %v", IgnoreFile, err)}
	}
	return ParseIgnoreFile(string(text), IgnoreFile, protected)
}

func ignoreFileRefusal() string {
	return IgnoreFile + ": must be a regular file in the task directory, not a link or a special file"
}

// SPDX-License-Identifier: MPL-2.0

package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// casesFile is the shared selection cases, copied unchanged from the platform
// (`make check-cases` compares the hash); it is never edited here.
//
// The format, one section per rule:
//   - exclude: a path, whether it is excluded, and a part of the reason;
//   - credential: a file name and text, and whether the file is refused;
//   - private_key_warning: a path, and whether it warns (it is uploaded either way);
//   - ignore_match: ignore-file text parsed with ignore_protected_paths, the
//     rule count, and paths (folder or file) that are or are not left out;
//   - ignore_refusal: ignore-file text and the exact error lines, in order;
//   - runtime_lockfiles and lockfile: each runtime's lockfile, and selected
//     paths that pass or fail with an error holding the listed parts;
//   - trees: a folder (files, symlinks) and what the walk selects, ignores,
//     warns and refuses; each expectation is checked only when present.
const casesFile = "testdata/cases.json"

type selectionCases struct {
	Version              int               `json:"version"`
	RuntimeLockfiles     map[string]string `json:"runtime_lockfiles"`
	IgnoreProtectedPaths []string          `json:"ignore_protected_paths"`
	Exclude              []struct {
		Path           string `json:"path"`
		Excluded       bool   `json:"excluded"`
		ReasonContains string `json:"reason_contains"`
	} `json:"exclude"`
	Credential []struct {
		File    string `json:"file"`
		Text    string `json:"text"`
		Refused bool   `json:"refused"`
	} `json:"credential"`
	PrivateKeyWarning []struct {
		Path  string `json:"path"`
		Warns bool   `json:"warns"`
	} `json:"private_key_warning"`
	IgnoreMatch []struct {
		Name      string `json:"name"`
		Rules     string `json:"rules"`
		RuleCount *int   `json:"rule_count"`
		Checks    []struct {
			Path    string `json:"path"`
			Dir     bool   `json:"dir"`
			Ignored bool   `json:"ignored"`
		} `json:"checks"`
	} `json:"ignore_match"`
	IgnoreRefusal []struct {
		Name   string   `json:"name"`
		Rules  string   `json:"rules"`
		Errors []string `json:"errors"`
	} `json:"ignore_refusal"`
	Lockfile []struct {
		Runtime       string   `json:"runtime"`
		Files         []string `json:"files"`
		ErrorContains []string `json:"error_contains"`
	} `json:"lockfile"`
	Trees []treeCase `json:"trees"`
}

type treeCase struct {
	Name            string            `json:"name"`
	Files           map[string]string `json:"files"`
	Symlinks        map[string]string `json:"symlinks"`
	Selected        []string          `json:"selected"`
	NotSelected     []string          `json:"not_selected"`
	Ignored         []string          `json:"ignored"`
	WarningCount    *int              `json:"warning_count"`
	WarningContains []string          `json:"warning_contains"`
	ErrorCount      *int              `json:"error_count"`
	ErrorContains   []string          `json:"error_contains"`
}

// handledSections are the top-level keys this test reads or knowingly skips.
// A new section fails the test until a check for it is written.
var handledSections = []string{
	"version", "contract", "consumed_by", "scope",
	"runtime_lockfiles", "exclude", "credential", "private_key_warning",
	"ignore_protected_paths", "ignore_match", "ignore_refusal", "lockfile", "trees",
}

const supportedCasesVersion = 1

func loadCases(t *testing.T) selectionCases {
	t.Helper()
	raw, err := os.ReadFile(casesFile)
	if err != nil {
		t.Fatal(err)
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatal(err)
	}
	for name := range sections {
		if !slices.Contains(handledSections, name) {
			t.Errorf("cases section %q has no check in this test", name)
		}
	}
	var c selectionCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Version != supportedCasesVersion {
		t.Fatalf("cases version %d, this test reads version %d", c.Version, supportedCasesVersion)
	}
	return c
}

func TestCasesExclude(t *testing.T) {
	for _, tc := range loadCases(t).Exclude {
		reason := ExcludeReason(tc.Path)
		if (reason != "") != tc.Excluded {
			t.Errorf("%q: reason %q, want excluded=%v", tc.Path, reason, tc.Excluded)
		}
		if tc.Excluded && !strings.Contains(reason, tc.ReasonContains) {
			t.Errorf("%q: reason %q does not contain %q", tc.Path, reason, tc.ReasonContains)
		}
	}
}

func TestCasesCredential(t *testing.T) {
	for _, tc := range loadCases(t).Credential {
		reason := CredentialReason(tc.File, tc.Text)
		if (reason != "") != tc.Refused {
			t.Errorf("%s %q: reason %q, want refused=%v", tc.File, tc.Text, reason, tc.Refused)
		}
		if tc.Refused && !strings.Contains(reason, tc.File+" holds a registry credential") {
			t.Errorf("%s: reason %q", tc.File, reason)
		}
	}
}

func TestCasesPrivateKeyWarning(t *testing.T) {
	for _, tc := range loadCases(t).PrivateKeyWarning {
		warning := PrivateKeyWarning(tc.Path)
		if (warning != "") != tc.Warns {
			t.Errorf("%q: warning %q, want warns=%v", tc.Path, warning, tc.Warns)
		}
		if tc.Warns && !strings.Contains(warning, "looks like a private key") {
			t.Errorf("%q: warning %q", tc.Path, warning)
		}
	}
}

func TestCasesIgnoreMatch(t *testing.T) {
	c := loadCases(t)
	for _, tc := range c.IgnoreMatch {
		rules, errs := ParseIgnoreFile(tc.Rules, IgnoreFile, c.IgnoreProtectedPaths)
		if len(errs) > 0 {
			t.Errorf("%s: errors %v", tc.Name, errs)
			continue
		}
		if tc.RuleCount != nil && len(rules) != *tc.RuleCount {
			t.Errorf("%s: %d rules, want %d", tc.Name, len(rules), *tc.RuleCount)
		}
		for _, check := range tc.Checks {
			if got := IgnoredBy(rules, check.Path, check.Dir); got != check.Ignored {
				t.Errorf("%s: %q (dir %v) ignored=%v, want %v", tc.Name, check.Path, check.Dir, got, check.Ignored)
			}
		}
	}
}

func TestCasesIgnoreRefusal(t *testing.T) {
	c := loadCases(t)
	for _, tc := range c.IgnoreRefusal {
		_, errs := ParseIgnoreFile(tc.Rules, IgnoreFile, c.IgnoreProtectedPaths)
		if !slices.Equal(errs, tc.Errors) {
			t.Errorf("%s:\n got %q\nwant %q", tc.Name, errs, tc.Errors)
		}
	}
}

func TestCasesRuntimeLockfiles(t *testing.T) {
	c := loadCases(t)
	if len(c.RuntimeLockfiles) != len(runtimes) {
		t.Errorf("runtimes: cases name %d, the provider %d", len(c.RuntimeLockfiles), len(runtimes))
	}
	for runtime, lockfile := range c.RuntimeLockfiles {
		if got := Lockfile(runtime); got != lockfile {
			t.Errorf("%s: lockfile %q, want %q", runtime, got, lockfile)
		}
	}
}

func TestCasesLockfile(t *testing.T) {
	for _, tc := range loadCases(t).Lockfile {
		got := CheckLockfile(tc.Files, tc.Runtime)
		if tc.ErrorContains == nil {
			if got != "" {
				t.Errorf("%s %v: error %q, want none", tc.Runtime, tc.Files, got)
			}
			continue
		}
		for _, part := range tc.ErrorContains {
			if !strings.Contains(got, part) {
				t.Errorf("%s %v: error %q does not contain %q", tc.Runtime, tc.Files, got, part)
			}
		}
	}
}

func TestCasesTrees(t *testing.T) {
	c := loadCases(t)
	for _, tc := range c.Trees {
		t.Run(tc.Name, func(t *testing.T) {
			root := buildTree(t, tc)
			var rules []IgnoreRule
			if text, ok := tc.Files[IgnoreFile]; ok {
				var errs []string
				rules, errs = ParseIgnoreFile(text, IgnoreFile, c.IgnoreProtectedPaths)
				if len(errs) > 0 {
					t.Fatalf("ignore file: %v", errs)
				}
			}
			checkTree(t, tc, ScanTree(root, rules))
		})
	}
}

func buildTree(t *testing.T, tc treeCase) string {
	t.Helper()
	root := t.TempDir()
	for rel, text := range tc.Files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range tc.Symlinks {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func checkTree(t *testing.T, tc treeCase, scan Scan) {
	t.Helper()
	selected := make([]string, 0, len(scan.Files))
	for _, f := range scan.Files {
		selected = append(selected, f.Path)
	}
	if tc.Selected != nil && !slices.Equal(selected, tc.Selected) {
		t.Errorf("selected %v, want %v", selected, tc.Selected)
	}
	for _, p := range tc.NotSelected {
		if slices.Contains(selected, p) {
			t.Errorf("%s is selected", p)
		}
	}
	if tc.Ignored != nil {
		ignored := append([]string{}, scan.Ignored...)
		sort.Strings(ignored)
		if !slices.Equal(ignored, tc.Ignored) {
			t.Errorf("ignored %v, want %v", ignored, tc.Ignored)
		}
	}
	checkMessages(t, "warning", scan.Warnings, tc.WarningCount, tc.WarningContains)
	checkMessages(t, "error", scan.Errors, tc.ErrorCount, tc.ErrorContains)
}

func checkMessages(t *testing.T, kind string, got []string, count *int, parts []string) {
	t.Helper()
	if count != nil && len(got) != *count {
		t.Errorf("%d %ss %q, want %d", len(got), kind, got, *count)
	}
	joined := strings.Join(got, "\n")
	for _, part := range parts {
		if !strings.Contains(joined, part) {
			t.Errorf("%ss %q do not contain %q", kind, got, part)
		}
	}
}

// SPDX-License-Identifier: MPL-2.0

package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, text string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func TestSelectReadsTheTaskFolder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "task.yaml", "schema_version: 1\nslug: demo\nruntime: \"python\" # the runtime\n", 0o644)
	writeFile(t, root, "main.py", "def run(p, c):\n    return p\n", 0o644)
	writeFile(t, root, "tool.sh", "#!/bin/sh\n", 0o755)
	writeFile(t, root, ".env", "S=1", 0o644)

	sel := Select(root)
	if sel.Facts.Runtime != "python" || sel.Facts.Entry != "main.py" {
		t.Fatalf("facts %+v", sel.Facts)
	}
	if sel.OK() || !strings.Contains(strings.Join(sel.Errors, "\n"), "requirements.txt is missing") {
		t.Fatalf("want the lockfile refusal, got %v", sel.Errors)
	}

	writeFile(t, root, "requirements.txt", "", 0o644)
	sel = Select(root)
	if !sel.OK() {
		t.Fatalf("errors %v", sel.Errors)
	}
	want := []string{"main.py", "requirements.txt", "task.yaml", "tool.sh"}
	if got := sel.Paths(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths %v, want %v", got, want)
	}
	if !sel.Files[3].Exec || sel.Files[0].Exec {
		t.Fatal("the exec bit is not kept")
	}
	if len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], ".env") {
		t.Fatalf("warnings %v", sel.Warnings)
	}
}

func TestSelectEntryFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "task.yaml", "slug: demo\nruntime: python\nentry: app/run.py\n", 0o644)
	writeFile(t, root, "requirements.txt", "", 0o644)
	writeFile(t, root, "main.py", "x", 0o644)
	if sel := Select(root); sel.OK() || !strings.Contains(sel.Errors[0], `the entry file "app/run.py" is not in the upload`) {
		t.Fatalf("errors %v", sel.Errors)
	}
	writeFile(t, root, "app/run.py", "x", 0o644)
	if sel := Select(root); !sel.OK() {
		t.Fatalf("errors %v", sel.Errors)
	}
}

func TestSelectUnknownRuntimeWarns(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "task.yaml", "slug: demo\nruntime: &r python\n", 0o644)
	sel := Select(root)
	if !sel.OK() || len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], "could not read the runtime") {
		t.Fatalf("errors %v warnings %v", sel.Errors, sel.Warnings)
	}
}

func TestSelectRefusesPathsTheServerRefuses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "task.yaml", "slug: demo\nruntime: python\n", 0o644)
	writeFile(t, root, "requirements.txt", "", 0o644)
	writeFile(t, root, "main.py", "x", 0o644)
	writeFile(t, root, `a\b.txt`, "x", 0o644)
	writeFile(t, root, "tab\there.txt", "x", 0o644)
	errs := strings.Join(Select(root).Errors, "\n")
	if !strings.Contains(errs, "backslash") || !strings.Contains(errs, "tab or newline") {
		t.Fatalf("errors %v", errs)
	}
}

func TestSelectRefusals(t *testing.T) {
	if sel := Select(filepath.Join(t.TempDir(), "missing")); sel.OK() {
		t.Fatal("a missing folder must be refused")
	}
	if sel := Select(t.TempDir()); sel.OK() || !strings.Contains(sel.Errors[0], "no task config") {
		t.Fatalf("errors %v", sel.Errors)
	}
	root := t.TempDir()
	writeFile(t, root, "task.json", `{"runtime":"node"}`, 0o644)
	writeFile(t, root, IgnoreFile, "package-lock.json\n", 0o644)
	if sel := Select(root); sel.OK() || !strings.Contains(sel.Errors[0], "would leave out package-lock.json") {
		t.Fatalf("errors %v", sel.Errors)
	}
}

func TestPathRefusal(t *testing.T) {
	ok := []string{"a", "a/b.txt", ".env.example", "x/.y"}
	for _, p := range ok {
		if got := PathRefusal(p); got != "" {
			t.Errorf("%q: %s", p, got)
		}
	}
	bad := []string{"", `a\b`, "/a", "a//b", "./a", "a/../b", "a\nb", "a\tb"}
	for _, p := range bad {
		if PathRefusal(p) == "" {
			t.Errorf("%q: want a refusal", p)
		}
	}
}

func TestReadConfigFacts(t *testing.T) {
	cases := []struct {
		text string
		want ConfigFacts
	}{
		{"runtime: go\n", ConfigFacts{Runtime: "go", Entry: "main.go"}},
		{"runtime: 'rust'\n", ConfigFacts{Runtime: "rust", Entry: "src/main.rs"}},
		{"a: 1\r\nruntime: node\r\nentry: src/app.ts # main\r\n", ConfigFacts{Runtime: "node", Entry: "src/app.ts"}},
		{"runtime: python\nentry:\n  - x\n", ConfigFacts{Runtime: "python"}},
		{"  runtime: node\n", ConfigFacts{}},
		{"runtime: cobol\n", ConfigFacts{}},
		{"runtime: [node]\n", ConfigFacts{}},
		{"execution:\n  runtime: go\n", ConfigFacts{}},
		{"runtime: python   # comment\n", ConfigFacts{Runtime: "python", Entry: "main.py"}},
	}
	for _, tc := range cases {
		if got := ReadConfigFacts("task.yaml", []byte(tc.text)); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.text, got, tc.want)
		}
	}
	if got := ReadConfigFacts("task.json", []byte(`{"runtime":"go","entry":"cmd/main.go"}`)); got != (ConfigFacts{Runtime: "go", Entry: "cmd/main.go"}) {
		t.Errorf("json: %+v", got)
	}
}

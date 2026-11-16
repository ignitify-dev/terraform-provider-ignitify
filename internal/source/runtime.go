// SPDX-License-Identifier: MPL-2.0

package source

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ConfigFileNames are the task config names, in the order every reader looks
// for them in the task root.
var ConfigFileNames = []string{"task.yaml", "task.yml", "task.json"}

const configJSONName = "task.json"

// runtimeFiles are the files a runtime needs in the upload.
type runtimeFiles struct {
	depManifest string
	lockfile    string
	// entry is the entry file when the task config names none.
	entry string
}

// runtimes maps each runtime to its files.
var runtimes = map[string]runtimeFiles{
	"node":   {depManifest: "package.json", lockfile: "package-lock.json", entry: "index.ts"},
	"python": {depManifest: "requirements.txt", lockfile: "requirements.txt", entry: "main.py"},
	"go":     {depManifest: "go.mod", lockfile: "go.sum", entry: "main.go"},
	"rust":   {depManifest: "Cargo.toml", lockfile: "Cargo.lock", entry: "src/main.rs"},
}

// Lockfile names the lockfile a runtime needs, or "" for an unknown runtime. Pure.
func Lockfile(runtime string) string {
	return runtimes[runtime].lockfile
}

// CheckLockfile says why an upload without the runtime's lockfile is refused,
// or "" when the lockfile is among the selected paths. Without a lockfile the
// build is not reproducible. Pure.
func CheckLockfile(paths []string, runtime string) string {
	required := Lockfile(runtime)
	for _, p := range paths {
		if p == required {
			return ""
		}
	}
	return fmt.Sprintf("%s is missing. A %s task must upload its lockfile: without one the "+
		"build is not reproducible and two deployments of the same source can resolve different versions.", required, runtime)
}

// protectedPaths are the root paths an ignore rule may not leave out: every
// task config name, and the runtime's dependency manifest and lockfile when
// the runtime is known. Pure.
func protectedPaths(runtime string) []string {
	out := append([]string(nil), ConfigFileNames...)
	if files, ok := runtimes[runtime]; ok {
		out = append(out, files.depManifest, files.lockfile)
	}
	return out
}

// ConfigFacts are what the plan-time checks read from the task config.
type ConfigFacts struct {
	// Runtime is a known runtime, or "" when the read could not tell.
	Runtime string
	// Entry is the entry file (the config's, or the runtime's default), or ""
	// when the read could not tell.
	Entry string
}

// CheckEntry says why an upload without its entry file is refused, or "".
// Pure.
func CheckEntry(paths []string, entry string) string {
	if entry == "" || slices.Contains(paths, entry) {
		return ""
	}
	return fmt.Sprintf("the entry file %q is not in the upload.", entry)
}

// scalarState says how a top-level key reads.
type scalarState int

const (
	scalarAbsent scalarState = iota
	scalarRead
	scalarUnreadable
)

// yamlScalarLine matches a top-level `key: value` line with a plain or quoted
// scalar and an optional comment.
var yamlScalarLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):[ \t]*(?:"([^"]*)"|'([^']*)'|([^\s"'#]+))[ \t]*(?:#.*)?$`)

// yamlTopLevelKey matches the key of any top-level mapping line.
var yamlTopLevelKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)[ \t]*:`)

// yamlScalar reads a top-level key of a YAML config: its scalar value, or
// absent, or unreadable (any other form). Pure.
func yamlScalar(text, key string) (string, scalarState) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := yamlScalarLine.FindStringSubmatch(line); m != nil && m[1] == key {
			return m[2] + m[3] + m[4], scalarRead
		}
		if m := yamlTopLevelKey.FindStringSubmatch(line); m != nil && m[1] == key {
			return "", scalarUnreadable
		}
	}
	return "", scalarAbsent
}

// jsonScalar reads a top-level key of a JSON config. Pure.
func jsonScalar(text []byte, key string) (string, scalarState) {
	var parsed map[string]any
	if json.Unmarshal(text, &parsed) != nil {
		return "", scalarUnreadable
	}
	v, ok := parsed[key]
	if !ok {
		return "", scalarAbsent
	}
	if str, ok := v.(string); ok {
		return str, scalarRead
	}
	return "", scalarUnreadable
}

// ReadConfigFacts reads the runtime and the entry file a task config names,
// for the plan-time checks only (lockfile, entry file, protected paths). It
// reads top-level `runtime:` and `entry:` lines of a YAML config, or those
// keys of a JSON one. It is no full parse: what it cannot tell is "", and the
// checks that need it are then left to the server and the build. Pure.
func ReadConfigFacts(configName string, text []byte) ConfigFacts {
	read := func(key string) (string, scalarState) { return yamlScalar(string(text), key) }
	if configName == configJSONName {
		read = func(key string) (string, scalarState) { return jsonScalar(text, key) }
	}
	runtime, state := read("runtime")
	files, known := runtimes[runtime]
	if state != scalarRead || !known {
		return ConfigFacts{}
	}
	entry, state := read("entry")
	switch state {
	case scalarAbsent:
		entry = files.entry
	case scalarUnreadable:
		entry = ""
	}
	return ConfigFacts{Runtime: runtime, Entry: entry}
}

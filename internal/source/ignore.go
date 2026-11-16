// SPDX-License-Identifier: MPL-2.0

package source

import (
	"fmt"
	"regexp"
	"strings"
)

// IgnoreFile names the file in the task root that leaves files out of the
// upload. It is read by the client only and is never uploaded.
const IgnoreFile = ".ignitifyignore"

// The ignore file is a small gitignore subset that can only leave files out:
//   - blank and `#` lines are skipped;
//   - `name` matches a path segment at any depth; a leading `/` anchors it to
//     the task root, and so does a `/` inside the pattern;
//   - a trailing `/` matches folders only;
//   - `*` and `?` work inside one segment; `**` as a whole segment spans segments;
//   - `!` (re-include), `[...]` and `\` are errors naming the line.
//
// A rule that would leave out a protected path (the task config, the runtime's
// dependency manifest or lockfile) is an error, not a later refusal.

// IgnoreRule is one parsed rule.
type IgnoreRule struct {
	Line    int
	Pattern string
	dirOnly bool
	re      *regexp.Regexp
}

const (
	ignoreComment     = "#"
	ignoreReinclude   = "!"
	ignoreUnsupported = `[]\`
	globDoubleStar    = "**"
	// anyChar is "any character but a line terminator", the meaning `.` has in
	// the CLI's patterns.
	anyChar = `[^\n\r\x{2028}\x{2029}]`
)

// ParseIgnoreFile parses the ignore file's text. fileName is used in the error
// lines (`<file>:<line>: ...`). Every bad line is reported, in order. Pure.
func ParseIgnoreFile(text, fileName string, protectedPaths []string) ([]IgnoreRule, []string) {
	var rules []IgnoreRule
	var errs []string
	for i, raw := range strings.Split(text, "\n") {
		rule, problem := parseIgnoreLine(i+1, strings.TrimFunc(raw, isJSSpace), fileName, protectedPaths)
		switch {
		case problem != "":
			errs = append(errs, fmt.Sprintf("%s:%d: %s", fileName, i+1, problem))
		case rule != nil:
			rules = append(rules, *rule)
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return rules, nil
}

// parseIgnoreLine parses one trimmed line: a rule, nothing (blank or comment),
// or a problem. Pure.
func parseIgnoreLine(line int, trimmed, fileName string, protectedPaths []string) (*IgnoreRule, string) {
	if trimmed == "" || strings.HasPrefix(trimmed, ignoreComment) {
		return nil, ""
	}
	if strings.HasPrefix(trimmed, ignoreReinclude) {
		return nil, fmt.Sprintf(`"!" (re-include) is not supported; %s can only leave files out`, fileName)
	}
	if strings.ContainsAny(trimmed, ignoreUnsupported) {
		return nil, fmt.Sprintf(`"%s" uses "[...]" or "\", which are not supported`, trimmed)
	}
	dirOnly := strings.HasSuffix(trimmed, "/")
	body := strings.TrimSuffix(trimmed, "/")
	rooted := strings.HasPrefix(body, "/")
	body = strings.TrimPrefix(body, "/")
	re, problem := compileIgnorePattern(body, rooted || strings.Contains(body, "/"))
	if problem != "" {
		return nil, fmt.Sprintf(`"%s" has %s`, trimmed, problem)
	}
	rule := IgnoreRule{Line: line, Pattern: trimmed, dirOnly: dirOnly, re: re}
	for _, p := range protectedPaths {
		if IgnoredBy([]IgnoreRule{rule}, p, false) {
			return nil, fmt.Sprintf(`"%s" would leave out %s, which the deploy needs`, trimmed, p)
		}
	}
	return &rule, ""
}

// compileIgnorePattern turns a pattern body into an anchored expression, or
// names the problem. Pure.
func compileIgnorePattern(body string, anchored bool) (*regexp.Regexp, string) {
	segs := strings.Split(body, "/")
	for _, s := range segs {
		if s == "" {
			return nil, "an empty path segment"
		}
	}
	for _, s := range segs {
		if strings.Contains(s, globDoubleStar) && s != globDoubleStar {
			return nil, `"**" must be a whole path segment`
		}
	}
	var src strings.Builder
	src.WriteString("^")
	if !anchored {
		src.WriteString("(?:" + anyChar + "*/)?")
	}
	last := len(segs) - 1
	for i, s := range segs {
		switch {
		case s == globDoubleStar && i == last:
			src.WriteString(anyChar + "*")
		case s == globDoubleStar:
			src.WriteString("(?:[^/]+/)*")
		default:
			src.WriteString(segmentExpression(s))
			if i != last {
				src.WriteString("/")
			}
		}
	}
	src.WriteString("$")
	return regexp.MustCompile(src.String()), ""
}

// segmentExpression is one segment's glob: `*` any run and `?` one character,
// both inside the segment; every other character is literal. Pure.
func segmentExpression(seg string) string {
	var out strings.Builder
	for _, c := range seg {
		switch c {
		case '*':
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return out.String()
}

// IgnoredBy reports whether a rule leaves this path out. relPath is
// POSIX-relative to the task root; isDir marks a folder (a folder match prunes
// its subtree). Pure.
func IgnoredBy(rules []IgnoreRule, relPath string, isDir bool) bool {
	for _, r := range rules {
		if (!r.dirOnly || isDir) && r.re.MatchString(relPath) {
			return true
		}
	}
	return false
}

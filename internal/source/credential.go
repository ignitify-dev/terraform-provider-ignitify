// SPDX-License-Identifier: MPL-2.0

package source

import (
	"strings"
	"unicode/utf8"
)

// A package-manager config file is honest source (`build_from_source=true`, a
// scope's registry); only a literal registry credential in it is refused: it
// would be stored with the source and the image. A `${VAR}` reference passes.
//
// The rules follow the published CLI's line rules exactly, including what
// counts as a line start (after "\n", "\r", U+2028 or U+2029) and as white
// space, so the provider refuses the same files the server refuses.

// credentialRules maps a file name, matched at any depth, to its rule.
var credentialRules = map[string]func(text string) bool{
	".npmrc":      npmrcHasCredential,
	".yarnrc.yml": yarnrcHasCredential,
}

// npmrc keys that hold a credential, bare or scoped (`//registry.example.com/:_authToken`).
var npmrcCredentialKeySuffixes = []string{"_authToken", "_auth", "_password"}

// yarnrc keys that hold a credential.
var yarnrcCredentialKeys = []string{"npmAuthToken", "npmAuthIdent"}

const (
	npmrcCommentMarks = "#;"
	inlineSpace       = " \t"
	varRefOpen        = "${"
	varRefClose       = '}'
	yamlQuotes        = `"'`
)

// IsCredentialFile reports whether a file of this base name is read for a
// registry credential. Pure.
func IsCredentialFile(base string) bool {
	_, ok := credentialRules[base]
	return ok
}

// CredentialReason says why a file must not be uploaded because it holds a
// registry credential, or "" when it may be. Pure.
func CredentialReason(relPath, text string) string {
	rule, ok := credentialRules[relPath[strings.LastIndex(relPath, "/")+1:]]
	if !ok || !rule(text) {
		return ""
	}
	return relPath + " holds a registry credential — it would be stored with the source and the image. " +
		"Remove it; a build reaches public registries without one"
}

// npmrcHasCredential: a line not starting with a comment mark whose key (the
// text before the first '=') ends with a credential key, and whose value is a
// literal. Pure.
func npmrcHasCredential(text string) bool {
	for _, start := range lineStarts(text) {
		line := text[start:]
		if line != "" && strings.ContainsRune(npmrcCommentMarks, rune(line[0])) {
			continue
		}
		end := strings.IndexAny(line, "=\n")
		if end < 0 || line[end] != '=' {
			continue
		}
		key := strings.TrimRight(line[:end], inlineSpace)
		if hasAnySuffix(key, npmrcCredentialKeySuffixes) && isLiteralValue(line[end+1:], false) {
			return true
		}
	}
	return false
}

// yarnrcHasCredential: a line whose key is a credential key and whose value is
// a literal (a quoted `${VAR}` passes). Pure.
func yarnrcHasCredential(text string) bool {
	for _, start := range lineStarts(text) {
		line := strings.TrimLeft(text[start:], inlineSpace)
		for _, key := range yarnrcCredentialKeys {
			if !strings.HasPrefix(line, key) {
				continue
			}
			rest := strings.TrimLeft(line[len(key):], inlineSpace)
			if strings.HasPrefix(rest, ":") && isLiteralValue(rest[1:], true) {
				return true
			}
		}
	}
	return false
}

// isLiteralValue reports whether the text after a key's separator starts a
// value that is not a bare `${VAR}` reference. Pure.
func isLiteralValue(afterSeparator string, quotesAllowed bool) bool {
	value := strings.TrimLeft(afterSeparator, inlineSpace)
	if value == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(value)
	return !isJSSpace(first) && !isVarReference(value, quotesAllowed)
}

// isVarReference reports whether value is exactly `${NAME}` (NAME of word
// characters), optionally quoted, then only spaces to the end of its line. Pure.
func isVarReference(value string, quotesAllowed bool) bool {
	rest := value
	if quotesAllowed && rest != "" && strings.ContainsRune(yamlQuotes, rune(rest[0])) {
		rest = rest[1:]
	}
	if !strings.HasPrefix(rest, varRefOpen) {
		return false
	}
	rest = rest[len(varRefOpen):]
	name := 0
	for name < len(rest) && isWordByte(rest[name]) {
		name++
	}
	if name == 0 || name >= len(rest) || rest[name] != varRefClose {
		return false
	}
	rest = rest[name+1:]
	if quotesAllowed && rest != "" && strings.ContainsRune(yamlQuotes, rune(rest[0])) {
		rest = rest[1:]
	}
	rest = strings.TrimLeft(rest, inlineSpace)
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return isLineTerminator(r)
}

// lineStarts lists the byte offsets where a line starts: 0 and after every
// line terminator. Pure.
func lineStarts(text string) []int {
	starts := []int{0}
	for i, r := range text {
		if isLineTerminator(r) {
			starts = append(starts, i+utf8.RuneLen(r))
		}
	}
	return starts
}

func isLineTerminator(r rune) bool {
	return r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029'
}

// isJSSpace is the white space class of the CLI's rules (ECMAScript `\s`). It
// differs from unicode.IsSpace: it holds U+FEFF and not U+0085.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func hasAnySuffix(s string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

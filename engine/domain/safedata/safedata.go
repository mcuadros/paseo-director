// SPDX-License-Identifier: Apache-2.0

// Package safedata owns the shared, pure classification rules for values that
// may cross a Director trust boundary. Callers still own their exact schema and
// contextual path policy; this package makes credential and private-path
// detection consistent at every typed ingress and public sink.
package safedata

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Classification is a closed, content-free safety result.
type Classification string

const (
	Safe        Classification = "SAFE"
	Invalid     Classification = "INVALID"
	Secret      Classification = "SECRET"
	PrivatePath Classification = "PRIVATE_PATH"
)

// ScanRules make contextual path and object-key treatment explicit. Private
// paths are legitimate inside a few engine-owned operational records, but are
// never legitimate in agent input or public diagnostics.
type ScanRules struct {
	MaximumBytes        int
	RejectPrivatePaths  bool
	RejectSensitiveKeys bool
}

var (
	secretPattern      = regexp.MustCompile(`(?i)(?:-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|\bgithub_pat_[A-Za-z0-9_]{4,}|\bgh[pousr]_[A-Za-z0-9_]{4,}|\bsk-(?:live_|test_|proj-)?[A-Za-z0-9_-]{4,}|\bglpat-[A-Za-z0-9_-]{4,}|\bnpm_[A-Za-z0-9]{4,}|\bxox[baprs]-[A-Za-z0-9-]{4,}|\bAIza[0-9A-Za-z_-]{20,}|\b(?:AKIA|ASIA)[0-9A-Z]{16}|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}|(?:authorization|auth[_ -]?header|password|passphrase|private[_ -]?key|client[_ -]?secret|access[_ -]?token|refresh[_ -]?token|api[_ -]?key|secret|token|credential)\s*["']?\s*[:=]\s*["']?[^\s"',}\]]{4,}|\b(?:bearer|basic)\s+[A-Za-z0-9+/_.=-]{3,}|[a-z][a-z0-9+.-]*://[^/@\s:]+:[^/@\s]+@)`)
	privatePathPattern = regexp.MustCompile(`(?i)(?:^|[\s("'` + "`" + `])(?:/(?:home|users|root|tmp|etc|var(?:/tmp)?|run(?:/user)?|proc|sys|dev|opt|mnt|srv|volumes|workspaces?)(?:/|\b)|[A-Z]:[\\/]|\\\\[^\\\s]+\\[^\\\s]+|file://|~[/\\])[^\s"'),;]*`)
	encodedPathPattern = regexp.MustCompile(`(?i)(?:%2e){2}(?:%2f|%5c)|(?:%2f|%5c)(?:home|users|root|tmp|etc|var|run|opt|mnt|srv|volumes|workspaces?)(?:%2f|%5c)`)
	traversalPattern   = regexp.MustCompile(`(?:^|[/\\])\.\.(?:[/\\]|$)`)
)

var sensitiveKeys = map[string]struct{}{
	"apikey": {}, "apitoken": {}, "accesstoken": {}, "refreshtoken": {}, "authtoken": {},
	"authorization": {}, "authheader": {}, "bearertoken": {}, "clientsecret": {},
	"credential": {}, "credentials": {}, "password": {}, "passphrase": {},
	"privatekey": {}, "secret": {}, "secretaccesskey": {}, "token": {},
}

func normalizedKey(value string) string {
	var result strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(unicode.ToLower(character))
		}
	}
	return result.String()
}

// SensitiveKey reports whether a caller-controlled object key names secret
// material. It deliberately does not classify metadata such as
// "credentialScope" or status codes such as "credential_required".
func SensitiveKey(value string) bool {
	_, found := sensitiveKeys[normalizedKey(value)]
	return found
}

// ContainsSecret detects supported credential shapes without returning or
// interpolating the rejected bytes.
func ContainsSecret(value string) bool {
	return !utf8.ValidString(value) || secretPattern.MatchString(value)
}

// ContainsPrivatePath detects private absolute paths, traversal, file URLs,
// Windows/UNC paths, and percent-encoded traversal/root aliases.
func ContainsPrivatePath(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) ||
		privatePathPattern.MatchString(value) || encodedPathPattern.MatchString(value) ||
		traversalPattern.MatchString(value) || strings.Contains(value, "..\\")
}

// ClassifyText applies secret-first classification so callers never disclose
// which part of a credential-bearing path matched.
func ClassifyText(value string, rejectPrivatePaths bool) Classification {
	if !utf8.ValidString(value) {
		return Invalid
	}
	if ContainsSecret(value) {
		return Secret
	}
	if rejectPrivatePaths && ContainsPrivatePath(value) {
		return PrivatePath
	}
	return Safe
}

// ClassifyValue recursively scans a decoded JSON value. JSON number and bool
// leaves are inert. Unknown Go values fail closed as invalid.
func ClassifyValue(value any, rules ScanRules) Classification {
	switch current := value.(type) {
	case map[string]any:
		classification := Safe
		for key, child := range current {
			if rules.RejectSensitiveKeys && SensitiveKey(key) {
				classification = mergeClassification(classification, Secret)
			}
			classification = mergeClassification(classification, ClassifyValue(child, rules))
		}
		return classification
	case []any:
		classification := Safe
		for _, child := range current {
			classification = mergeClassification(classification, ClassifyValue(child, rules))
		}
		return classification
	case string:
		return ClassifyText(current, rules.RejectPrivatePaths)
	case nil, bool, json.Number:
		return Safe
	default:
		return Invalid
	}
}

func mergeClassification(left, right Classification) Classification {
	for _, classification := range []Classification{Invalid, Secret, PrivatePath} {
		if left == classification || right == classification {
			return classification
		}
	}
	return Safe
}

// ClassifyJSON strictly scans one bounded JSON value and rejects trailing input.
// Ingress callers separately enforce their canonical duplicate-key policy. This
// independent complete decode ensures escaped credential/path bytes cannot
// evade the text rules.
func ClassifyJSON(raw []byte, rules ScanRules) Classification {
	if rules.MaximumBytes < 1 || len(raw) == 0 || len(raw) > rules.MaximumBytes || !utf8.Valid(raw) {
		return Invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return Invalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Invalid
	}
	return ClassifyValue(value, rules)
}

// Redact replaces recognized secret and private-path spans. If a less
// structured path detector still fires after replacement, it suppresses the
// whole value rather than risk returning a partial path.
func Redact(value string) (string, Classification) {
	if !utf8.ValidString(value) {
		return "[REDACTED]", Invalid
	}
	classification := ClassifyText(value, true)
	if classification == Safe {
		return value, Safe
	}
	redacted := secretPattern.ReplaceAllString(value, "[REDACTED_SECRET]")
	redacted = privatePathPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		prefix := ""
		if len(match) > 0 && (unicode.IsSpace(rune(match[0])) || strings.ContainsRune("(\"'`", rune(match[0]))) {
			prefix = match[:1]
		}
		return prefix + "[REDACTED_PATH]"
	})
	if encodedPathPattern.MatchString(redacted) || traversalPattern.MatchString(redacted) ||
		strings.HasPrefix(strings.TrimSpace(redacted), "/") || strings.Contains(redacted, "..\\") {
		redacted = "[REDACTED_PATH]"
	}
	return redacted, classification
}

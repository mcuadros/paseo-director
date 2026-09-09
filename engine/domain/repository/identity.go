// SPDX-License-Identifier: Apache-2.0

// Package repository owns the pure canonical identity rules for source Git
// repositories. Filesystem and Git adapters supply observed paths and remotes;
// this package performs no I/O.
package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	pathpkg "path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaximumRemoteBytes = 2048
	MaximumPathBytes   = 4096
)

var ErrInvalidRemote = errors.New("repository remote is invalid")

// Remote is one normalized transport locator and its transport-independent
// repository key. Equivalent supported HTTPS, SSH URL, and SCP spellings map
// to the same Key without weakening host/path identity.
type Remote struct {
	Canonical string `json:"canonical"`
	Key       string `json:"key"`
	ID        string `json:"id"`
}

func unsafeRune(value rune) bool {
	return unicode.IsSpace(value) || unicode.IsControl(value) || unicode.In(value, unicode.Cf) ||
		strings.ContainsRune("\\\"'`$;&|<>", value)
}

func safeComponent(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.IndexFunc(value, unsafeRune) < 0
}

func decodeEscapes(value string) (string, bool) {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			decoded = append(decoded, value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", false
		}
		part, err := strconv.ParseUint(value[index+1:index+3], 16, 8)
		if err != nil {
			return "", false
		}
		decoded = append(decoded, byte(part))
		index += 2
	}
	return string(decoded), utf8.Valid(decoded)
}

func normalizedRepositoryPath(value string) (string, bool) {
	if !safeComponent(value) {
		return "", false
	}
	value = strings.TrimLeft(value, "/")
	cleaned := pathpkg.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", false
		}
	}
	// A repeated suffix is ambiguous under the one supported convenience
	// normalization: removing only one suffix would emit another input which
	// normalizes to a different key. Reject the entire case-insensitive chain
	// instead of creating a new repository alias class.
	if strings.HasSuffix(strings.ToLower(cleaned), ".git.git") {
		return "", false
	}
	cleaned = strings.TrimSuffix(cleaned, ".git")
	if cleaned == "" || cleaned == "." || strings.HasSuffix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

// encodedRepositoryPath emits the normalized logical path without leaving a
// percent sequence which a later parse could decode again. A literal percent
// can enter only through one valid %25 input escape and is re-escaped here.
func encodedRepositoryPath(value string) string {
	return strings.ReplaceAll(value, "%", "%25")
}

func canonicalIPv4(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return "", false
	}
	canonical := make([]string, len(parts))
	for index, part := range parts {
		if part == "" || len(part) > 3 || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return "", false
		}
		if len(part) > 1 && part[0] == '0' {
			return "", false
		}
		number, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			return "", false
		}
		canonical[index] = strconv.FormatUint(number, 10)
	}
	return strings.Join(canonical, "."), true
}

func ipv6Groups(value string) ([]uint16, bool) {
	if value == "" {
		return []uint16{}, true
	}
	parts := strings.Split(value, ":")
	groups := make([]uint16, 0, len(parts))
	for index, part := range parts {
		if part == "" {
			return nil, false
		}
		if strings.Contains(part, ".") {
			if index != len(parts)-1 {
				return nil, false
			}
			address, ok := canonicalIPv4(part)
			if !ok {
				return nil, false
			}
			octets := strings.Split(address, ".")
			high, _ := strconv.ParseUint(octets[0], 10, 8)
			highLow, _ := strconv.ParseUint(octets[1], 10, 8)
			lowHigh, _ := strconv.ParseUint(octets[2], 10, 8)
			low, _ := strconv.ParseUint(octets[3], 10, 8)
			groups = append(groups, uint16(high<<8|highLow), uint16(lowHigh<<8|low))
			continue
		}
		if len(part) > 4 || strings.IndexFunc(part, func(r rune) bool {
			return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'))
		}) >= 0 {
			return nil, false
		}
		number, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return nil, false
		}
		groups = append(groups, uint16(number))
	}
	return groups, true
}

func canonicalIPv6(value string) (string, bool) {
	if value == "" || strings.Count(value, "::") > 1 {
		return "", false
	}
	var groups []uint16
	if separator := strings.Index(value, "::"); separator >= 0 {
		// An embedded IPv4 address represents the final 32 bits. It cannot
		// appear before a compression marker which appends later zero groups.
		if strings.Contains(value[:separator], ".") {
			return "", false
		}
		left, leftOK := ipv6Groups(value[:separator])
		right, rightOK := ipv6Groups(value[separator+2:])
		if !leftOK || !rightOK || len(left)+len(right) >= 8 {
			return "", false
		}
		groups = append(groups, left...)
		groups = append(groups, make([]uint16, 8-len(left)-len(right))...)
		groups = append(groups, right...)
	} else {
		var ok bool
		groups, ok = ipv6Groups(value)
		if !ok || len(groups) != 8 {
			return "", false
		}
	}
	canonical := make([]string, len(groups))
	for index, group := range groups {
		canonical[index] = strconv.FormatUint(uint64(group), 16)
	}
	return strings.Join(canonical, ":"), true
}

func canonicalHostname(value string) (string, bool) {
	value = strings.ToLower(value)
	if value == "" || len(value) > 253 {
		return "", false
	}
	if strings.IndexFunc(value, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' }) >= 0 {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", false
		}
	}
	return value, true
}

func normalizedHost(hostname, port, scheme string, bracketed bool) (string, bool) {
	var ok bool
	if bracketed {
		hostname, ok = canonicalIPv6(hostname)
		if !ok {
			return "", false
		}
		hostname = "[" + hostname + "]"
	} else {
		if strings.Contains(hostname, ":") {
			return "", false
		}
		if strings.IndexFunc(hostname, func(r rune) bool { return (r < '0' || r > '9') && r != '.' }) < 0 {
			hostname, ok = canonicalIPv4(hostname)
		} else {
			hostname, ok = canonicalHostname(hostname)
		}
		if !ok {
			return "", false
		}
	}
	if port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return "", false
		}
		if (scheme == "https" && value == 443) || (scheme == "ssh" && value == 22) ||
			(scheme == "git" && value == 9418) {
			port = ""
		} else {
			port = strconv.FormatUint(value, 10)
		}
	}
	if port != "" {
		return hostname + ":" + port, true
	}
	return hostname, true
}

func repositoryID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "repository-" + hex.EncodeToString(digest[:])
}

func splitURLHostPort(value string) (string, string, bool, bool) {
	if strings.HasPrefix(value, "[") {
		closing := strings.IndexByte(value, ']')
		if closing < 2 {
			return "", "", false, false
		}
		hostname := value[1:closing]
		remainder := value[closing+1:]
		if remainder == "" {
			return hostname, "", true, true
		}
		if !strings.HasPrefix(remainder, ":") || len(remainder) == 1 {
			return "", "", false, false
		}
		return hostname, remainder[1:], true, true
	}
	if strings.Count(value, ":") > 1 {
		return "", "", false, false
	}
	if separator := strings.LastIndexByte(value, ':'); separator >= 0 {
		if separator == 0 || separator == len(value)-1 {
			return "", "", false, false
		}
		return value[:separator], value[separator+1:], false, true
	}
	return value, "", false, value != ""
}

func validSSHUsername(value string) bool {
	return value == "git"
}

func parseURLRemote(value string) (Remote, error) {
	separator := strings.Index(value, "://")
	if separator <= 0 {
		return Remote{}, ErrInvalidRemote
	}
	scheme := strings.ToLower(value[:separator])
	if scheme != "https" && scheme != "ssh" && scheme != "git" {
		return Remote{}, ErrInvalidRemote
	}
	remainder := value[separator+3:]
	slash := strings.IndexByte(remainder, '/')
	if slash <= 0 || strings.ContainsAny(remainder, "?#") {
		return Remote{}, ErrInvalidRemote
	}
	authority, remotePath := remainder[:slash], remainder[slash:]
	if strings.Count(authority, "@") > 1 {
		return Remote{}, ErrInvalidRemote
	}
	username := ""
	hostPort := authority
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		username = authority[:at]
		hostPort = authority[at+1:]
		if scheme != "ssh" || !validSSHUsername(username) {
			return Remote{}, ErrInvalidRemote
		}
	}
	hostname, port, bracketed, ok := splitURLHostPort(hostPort)
	if !ok {
		return Remote{}, ErrInvalidRemote
	}
	host, ok := normalizedHost(hostname, port, scheme, bracketed)
	if !ok {
		return Remote{}, ErrInvalidRemote
	}
	repositoryPath, ok := normalizedRepositoryPath(remotePath)
	if !ok {
		return Remote{}, ErrInvalidRemote
	}
	userPrefix := ""
	if username != "" {
		userPrefix = username + "@"
	}
	canonical := scheme + "://" + userPrefix + host + "/" + encodedRepositoryPath(repositoryPath)
	key := host + "/" + repositoryPath
	return Remote{Canonical: canonical, Key: key, ID: repositoryID(key)}, nil
}

func parseSCPRemote(value string) (Remote, error) {
	if strings.Contains(value, "://") || strings.Count(value, "@") > 1 {
		return Remote{}, ErrInvalidRemote
	}
	username := ""
	hostPath := value
	if at := strings.LastIndexByte(value, '@'); at >= 0 {
		username = value[:at]
		hostPath = value[at+1:]
		if !validSSHUsername(username) {
			return Remote{}, ErrInvalidRemote
		}
	}
	separator := strings.IndexByte(hostPath, ':')
	if separator <= 0 {
		return Remote{}, ErrInvalidRemote
	}
	host, ok := normalizedHost(hostPath[:separator], "", "ssh", false)
	if !ok || (username == "" && !strings.Contains(host, ".") && !strings.EqualFold(host, "localhost")) {
		return Remote{}, ErrInvalidRemote
	}
	repositoryPath, ok := normalizedRepositoryPath(hostPath[separator+1:])
	if !ok {
		return Remote{}, ErrInvalidRemote
	}
	userPrefix := ""
	if username != "" {
		userPrefix = username + "@"
	}
	canonical := "ssh://" + userPrefix + host + "/" + encodedRepositoryPath(repositoryPath)
	key := host + "/" + repositoryPath
	return Remote{Canonical: canonical, Key: key, ID: repositoryID(key)}, nil
}

// CanonicalRemote parses one supported credential-free Git remote. It
// rejects option-like, command-bearing, local-path, helper, traversal, query,
// and fragment forms before deriving a stable repository identity.
func CanonicalRemote(value string) (Remote, error) {
	if len(value) == 0 || len(value) > MaximumRemoteBytes || strings.HasPrefix(value, "-") || !utf8.ValidString(value) {
		return Remote{}, ErrInvalidRemote
	}
	decoded, ok := decodeEscapes(value)
	if !ok || strings.IndexFunc(decoded, unsafeRune) >= 0 {
		return Remote{}, ErrInvalidRemote
	}
	if strings.Contains(decoded, "://") {
		return parseURLRemote(decoded)
	}
	if strings.Contains(decoded, "::") {
		return Remote{}, ErrInvalidRemote
	}
	return parseSCPRemote(decoded)
}

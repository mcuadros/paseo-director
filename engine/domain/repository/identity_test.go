// SPDX-License-Identifier: Apache-2.0

package repository

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type canonicalRemoteLengthCase struct {
	name          string
	input         string
	wantCanonical string
	wantBytes     int
	wantAccepted  bool
}

func paddedRemote(t *testing.T, prefix, suffix string, totalBytes int) string {
	t.Helper()
	padding := totalBytes - len(prefix) - len(suffix)
	if padding < 1 {
		t.Fatalf("remote fixture length %d is too short for %q and %q", totalBytes, prefix, suffix)
	}
	return prefix + strings.Repeat("a", padding) + suffix
}

func canonicalRemoteLengthCases(t *testing.T) []canonicalRemoteLengthCase {
	t.Helper()
	cases := make([]canonicalRemoteLengthCase, 0, 24)
	for _, target := range []int{MaximumRemoteBytes - 1, MaximumRemoteBytes, MaximumRemoteBytes + 1} {
		accepted := target <= MaximumRemoteBytes
		suffix := fmt.Sprintf("canonical-%d", target)

		raw := paddedRemote(t, "https://a.example/", "", target)
		cases = append(cases, canonicalRemoteLengthCase{
			name: "raw-url/" + suffix, input: raw, wantCanonical: raw,
			wantBytes: target, wantAccepted: accepted,
		})

		scpCanonicalPrefix := "ssh://git@a/"
		scpPath := strings.Repeat("a", target-len(scpCanonicalPrefix))
		cases = append(cases, canonicalRemoteLengthCase{
			name: "scp-expansion/" + suffix, input: "git@a:" + scpPath,
			wantCanonical: scpCanonicalPrefix + scpPath, wantBytes: target, wantAccepted: accepted,
		})
		scpRawPath := strings.Repeat("a", target-len("git@a:"))
		cases = append(cases, canonicalRemoteLengthCase{
			name: "scp-raw-input/" + suffix, input: "git@a:" + scpRawPath,
			wantCanonical: scpCanonicalPrefix + scpRawPath,
			wantBytes:     target + len(scpCanonicalPrefix) - len("git@a:"), wantAccepted: false,
		})

		urlCanonicalPrefix := "ssh://git@[0:0:0:0:0:0:0:1]/"
		urlPath := strings.Repeat("a", target-len(urlCanonicalPrefix))
		cases = append(cases, canonicalRemoteLengthCase{
			name: "ipv6-url-expansion/" + suffix, input: "ssh://git@[::1]/" + urlPath,
			wantCanonical: urlCanonicalPrefix + urlPath, wantBytes: target, wantAccepted: accepted,
		})
		urlRawPrefix := "ssh://git@[::1]/"
		urlRawPath := strings.Repeat("a", target-len(urlRawPrefix))
		cases = append(cases, canonicalRemoteLengthCase{
			name: "ipv6-url-raw-input/" + suffix, input: urlRawPrefix + urlRawPath,
			wantCanonical: urlCanonicalPrefix + urlRawPath,
			wantBytes:     target + len(urlCanonicalPrefix) - len(urlRawPrefix), wantAccepted: false,
		})

		percent := paddedRemote(t, "https://a.example/", "%25z", target)
		cases = append(cases, canonicalRemoteLengthCase{
			name: "percent-encoded/" + suffix, input: percent, wantCanonical: percent,
			wantBytes: target, wantAccepted: accepted,
		})

		unicode := paddedRemote(t, "https://a.example/", "é", target)
		cases = append(cases, canonicalRemoteLengthCase{
			name: "utf8-byte-boundary/" + suffix, input: unicode, wantCanonical: unicode,
			wantBytes: target, wantAccepted: accepted,
		})

		cases = append(cases, canonicalRemoteLengthCase{
			name:      "repeated-git-suffix/" + suffix,
			input:     paddedRemote(t, "https://a.example/", ".git.git", target),
			wantBytes: target, wantAccepted: false,
		})
	}
	return cases
}

func TestCanonicalRemoteMapsSupportedAliasesToStableIdentity(t *testing.T) {
	aliases := []string{
		"https://GitHub.com:443/example/product.git/",
		"ssh://git@github.com:22/example/product.git",
		"git@github.com:example/product.git",
		"git://github.com:9418/example/product",
	}
	var first Remote
	for index, alias := range aliases {
		identity, err := CanonicalRemote(alias)
		if err != nil {
			t.Fatalf("CanonicalRemote(%q): %v", alias, err)
		}
		if index == 0 {
			first = identity
		}
		if identity.Key != "github.com/example/product" || identity.ID != first.ID {
			t.Fatalf("alias %q mapped to %#v, first %#v", alias, identity, first)
		}
	}
	if first.Canonical != "https://github.com/example/product" {
		t.Fatalf("canonical HTTPS remote = %q", first.Canonical)
	}

	otherPort, err := CanonicalRemote("ssh://git@github.com:2222/example/product.git")
	if err != nil {
		t.Fatal(err)
	}
	if otherPort.ID == first.ID || otherPort.Key != "github.com:2222/example/product" {
		t.Fatalf("non-default port alias collapsed unsafely: %#v", otherPort)
	}
}

func TestCanonicalRemoteBoundsCanonicalOutputAndRequiresFixedPoint(t *testing.T) {
	for _, test := range canonicalRemoteLengthCases(t) {
		t.Run(test.name, func(t *testing.T) {
			identity, err := CanonicalRemote(test.input)
			if !test.wantAccepted {
				if !errors.Is(err, ErrInvalidRemote) || strings.Contains(err.Error(), test.input) {
					t.Fatalf("rejected remote result = %#v, error = %v", identity, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if identity.Canonical != test.wantCanonical || len(identity.Canonical) != test.wantBytes ||
				len(identity.Canonical) > MaximumRemoteBytes {
				t.Fatalf("canonical identity bytes = %d, want %d", len(identity.Canonical), test.wantBytes)
			}
			roundTrip, err := CanonicalRemote(identity.Canonical)
			if err != nil || roundTrip != identity {
				t.Fatalf("canonical fixed point = %#v, %v; want %#v", roundTrip, err, identity)
			}
		})
	}
}

func TestCanonicalRemoteIsDeterministic(t *testing.T) {
	for _, input := range []string{
		"git@host.example:group/repository.git",
		"https://github.com/acme/repo%252egit",
		"https://github.com/acme/repo%25252egit",
		"ssh://git@host.example/acme/repo%25.git",
		"git://host.example/acme/r%C3%A9sum%C3%A9.git",
		"ssh://git@[::ffff:192.168.1.1]/acme/repository.git",
		"ssh://git@[2001:db8::192.0.2.1]:22/acme/repository.git",
	} {
		t.Run(input, func(t *testing.T) {
			first, err := CanonicalRemote(input)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 1_000; attempt++ {
				current, err := CanonicalRemote(input)
				if err != nil || current != first {
					t.Fatalf("attempt %d = %#v, %v; want %#v", attempt, current, err, first)
				}
				roundTrip, err := CanonicalRemote(current.Canonical)
				if err != nil || roundTrip != current {
					t.Fatalf("canonical round trip %d = %#v, %v; want %#v", attempt, roundTrip, err, current)
				}
			}
		})
	}
}

func TestCanonicalRemoteEncodesLiteralPercentExactlyOnce(t *testing.T) {
	tests := map[string]Remote{
		"https://github.com/acme/repo%252egit": {
			Canonical: "https://github.com/acme/repo%252egit",
			Key:       "github.com/acme/repo%2egit",
		},
		"git@github.com:acme/repo%25252egit": {
			Canonical: "ssh://git@github.com/acme/repo%25252egit",
			Key:       "github.com/acme/repo%252egit",
		},
	}
	for input, expected := range tests {
		identity, err := CanonicalRemote(input)
		if err != nil || identity.Canonical != expected.Canonical || identity.Key != expected.Key {
			t.Fatalf("CanonicalRemote(%q) = %#v, %v; want canonical=%q key=%q", input, identity, err, expected.Canonical, expected.Key)
		}
		if identity.ID != repositoryID(expected.Key) {
			t.Fatalf("CanonicalRemote(%q) ID = %q", input, identity.ID)
		}
		roundTrip, err := CanonicalRemote(identity.Canonical)
		if err != nil || roundTrip != identity {
			t.Fatalf("CanonicalRemote(%q canonical) = %#v, %v; want %#v", input, roundTrip, err, identity)
		}
	}
}

func TestCanonicalRemoteRejectsAmbiguousAndUnsafeInputs(t *testing.T) {
	for _, input := range []string{
		"", "-upload-pack=payload", "/srv/repository", "file:///srv/repository",
		"http://github.com/example/product.git", "https://user:secret@github.com/example/product.git",
		"https://token-shaped-username-0123456789abcdef@github.com/example/product.git",
		"git://git@github.com/example/product.git", "ssh://deploy@github.com/example/product.git",
		"ext::sh", "host.example::path", "git@host.example::path",
		"https://github.com/example/../other.git", "https://github.com/example/product.git?token=secret",
		"https://github.com/example/%60id%60.git", "https://github.com/example/product.git#other",
		"ssh://git@[::::]/example/product.git", "ssh://git@[2001:db8:1]/example/product.git",
		"ssh://git@[192.168.1.1::]/example/product.git", "ssh://git@[192.168.1.1::1]/example/product.git",
		"ssh://git@[::ffff:192.168.001.001]/example/product.git",
		"ssh://git@999.1.1.1/example/product.git", "ssh://git@[2001:db8::1/example/product.git",
		"ssh://git@192.168.001.010/example/product.git", "ssh://git@01.2.3.4/example/product.git",
		"ssh://git@github.com:0/example/product.git", "ssh://git@github.com:65536/example/product.git",
		"https://github.com/example/repository%2",
		"https://github.com/acme/repository.git.git",
		"https://github.com/acme/repository.GIT.git",
		"https://github.com/acme/repository%2egit.git",
		"https://github.com/acme/repository.git%2egit",
		"https://github.com/acme/repository%2Egit%2egit%2EGIT",
		"https://github.com/acme/repository%252egit.git.git",
		"git@github.com:acme/repository%252egit%2egit.git",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := CanonicalRemote(input); !errors.Is(err, ErrInvalidRemote) {
				t.Fatalf("CanonicalRemote(%q) error = %v", input, err)
			}
		})
	}
}

func FuzzCanonicalRemoteRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"https://github.com/acme/repository.git",
		"git@github.com:acme/repository.git",
		"ssh://git@[2001:db8::192.0.2.1]/acme/repository.git",
		"https://github.com/acme/repository%252egit",
		"https://github.com/acme/repository%252egit.git",
		"https://github.com/acme/repository.git.git",
		"https://github.com/acme/repository%2egit.git",
		"https://token-shaped-username-0123456789abcdef@github.com/acme/repository.git",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > MaximumRemoteBytes {
			return
		}
		first, err := CanonicalRemote(input)
		if err != nil {
			return
		}
		second, err := CanonicalRemote(first.Canonical)
		if err != nil || second != first {
			t.Fatalf("CanonicalRemote fixed point for %q = %#v, %v; want %#v", input, second, err, first)
		}
	})
}

func TestCanonicalRemoteNormalizesStructuredIPAddressesAndPorts(t *testing.T) {
	tests := map[string]string{
		"ssh://git@192.168.1.10:0022/example/product.git":   "ssh://git@192.168.1.10/example/product",
		"ssh://git@[2001:0DB8::1]:0022/example/product.git": "ssh://git@[2001:db8:0:0:0:0:0:1]/example/product",
		"https://[2001:0DB8::1]:0443/example/product.git":   "https://[2001:db8:0:0:0:0:0:1]/example/product",
		"https://192.168.1.10:0443/example/product.git":     "https://192.168.1.10/example/product",
		"ssh://git@[::ffff:192.168.1.1]/example/product":    "ssh://git@[0:0:0:0:0:ffff:c0a8:101]/example/product",
	}
	for input, expected := range tests {
		identity, err := CanonicalRemote(input)
		if err != nil || identity.Canonical != expected {
			t.Fatalf("CanonicalRemote(%q) = %#v, %v; want %q", input, identity, err, expected)
		}
		roundTrip, err := CanonicalRemote(identity.Canonical)
		if err != nil || roundTrip != identity {
			t.Fatalf("round trip %q = %#v, %v; want %#v", identity.Canonical, roundTrip, err, identity)
		}
	}
}

func TestCanonicalRemoteErrorDoesNotPropagateCredential(t *testing.T) {
	const secret = "token-shaped-username-0123456789abcdef"
	_, err := CanonicalRemote("https://" + secret + "@github.com/example/product.git")
	if !errors.Is(err, ErrInvalidRemote) || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential rejection = %v", err)
	}
}

func TestCanonicalRemoteRepeatedSuffixErrorDoesNotPropagateInput(t *testing.T) {
	for _, input := range []string{
		"https://github.com/acme/repository.git.git",
		"https://github.com/acme/repository%2egit.git",
		"https://github.com/acme/repository.git%2egit",
	} {
		_, err := CanonicalRemote(input)
		if !errors.Is(err, ErrInvalidRemote) || strings.Contains(err.Error(), input) {
			t.Fatalf("repeated suffix rejection for %q = %v", input, err)
		}
	}
}

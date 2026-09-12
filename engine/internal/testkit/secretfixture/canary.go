// SPDX-License-Identifier: Apache-2.0

// Package secretfixture constructs credential-shaped test canaries at runtime.
// No source fragment is itself a provider credential, so tracked blobs remain
// safe for publication while boundary tests still exercise the exact shapes.
package secretfixture

import "strings"

func assemble(parts ...string) string {
	return strings.Join(parts, "")
}

func GitHubFineGrained() string {
	return assemble("github", "_pat_", "01234567", "89abcdef", "ghijklmn", "op")
}

func GitHubFineGrainedLetters() string {
	return assemble("github", "_pat_", "abcdefgh", "ijklmnop")
}

func GitHubLegacyPAT() string {
	return assemble("g", "hp_", "01234567", "89abcdef", "ghijklmn", "op")
}

func OpenAIKey() string {
	return assemble("s", "k-proj-", "01234567", "89abcdef", "ghijklmn", "op")
}

func AWSAccessKey() string {
	return assemble("AK", "IA", "01234567", "89ABCDEF")
}

func AWSTemporaryAccessKey() string {
	return assemble("AS", "IA", "01234567", "89ABCDEF")
}

func GitLabPAT() string {
	return assemble("gl", "pat-", "01234567", "89abcdef", "ghijklmn", "op")
}

func NPMToken() string {
	return assemble("n", "pm_", "01234567", "89abcdef", "ghijklmn", "op")
}

func SlackBotToken() string {
	return assemble("xo", "xb-", "0123456789", "-", "abcdefgh", "ijklmnop")
}

func GoogleAPIKey() string {
	return assemble("AI", "za", "01234567", "89abcdef", "ghijklmn", "opqrstuv", "w")
}

func JWT() string {
	return assemble("e", "yJabcdefghijk", ".", "abcdefghijkl", ".", "abcdefghijkl")
}

func PrivateKeyHeader(kind string) string {
	prefix := assemble("-----", "BEGIN ")
	if kind != "" {
		prefix += kind + " "
	}
	return assemble(prefix, "PRIVATE ", "KEY", "-----")
}

func ProviderCanaries() []string {
	return []string{
		GitHubFineGrained(),
		GitHubLegacyPAT(),
		OpenAIKey(),
		AWSAccessKey(),
		AWSTemporaryAccessKey(),
		GitLabPAT(),
		NPMToken(),
		SlackBotToken(),
		GoogleAPIKey(),
		JWT(),
		"Authorization: Bearer abcdefghijklmnop",
		"api_key=0123456789abcdefghijklmnop",
		"https://owner:0123456789abcdef@example.invalid/repo.git",
		PrivateKeyHeader("OPENSSH"),
	}
}

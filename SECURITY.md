# Security policy

Director for Paseo is an independent community plugin. Security reports are
handled privately and separately from ordinary support requests.

## Report a vulnerability privately

Use [GitHub private vulnerability reporting](https://github.com/mcuadros/paseo-director/security/advisories/new).
Do not open a public issue, discussion, pull request, or chat thread for a
suspected vulnerability. Do not include credentials, access tokens, private
keys, unredacted private paths, repository contents, customer data, or a
Director support bundle in a public post.

If the private reporting form is unavailable, do not publish the report as a
fallback. Retain the minimum redacted evidence and wait for a private project
contact path to be restored.

## Supported versions and scope

No public beta or stable version has been released yet. Unreleased commits and
internal alpha builds do not carry a production support promise, but suspected
vulnerabilities in them may still be reported privately.

Once channels are explicitly promoted, the policy is:

| Release line | Security status |
|---|---|
| Latest stable `1.x` release | Supported |
| Current public beta, while the beta channel is active | Supported |
| Current tagged alpha, while an alpha evaluation is active | Pre-release reports accepted; not production-supported |
| Default `main` | Development only; reports accepted |
| Older stable, beta, alpha, exact-commit, or other development builds | Unsupported unless a release advisory says otherwise |

The supported `1.0` boundary is Linux amd64 with exact stable Paseo `0.7.2`.
Paseo `0.8` preview builds, other Paseo versions, other operating systems and
architectures, and modified or unverified release artifacts are outside the
support claim. See the [compatibility policy](docs/compatibility.md).

Reports are in scope when they concern Director source or reviewed release
artifacts, the standalone engine, the Paseo connector or UI, installation and
update integrity, handler-scoped Paseo authority, plugin-owned Engine/Dolt
supervision, TaskStore credential separation and access, agent isolation,
exact-Candidate delivery, cleanup ownership, redaction, or support bundles.
Upstream vulnerabilities in Paseo, provider CLIs, Git, GitHub CLI, Dolt, Go,
Node.js, npm, or the Linux host should also be reported to the upstream owner;
privately report any evidence that Director makes an upstream issue cross its
documented boundary.

The `1.0` threat model trusts the reviewed Director Engine and dependencies,
the Paseo daemon, and exact admitted provider CLI binaries. It does not claim
containment after compromise of those components, the kernel, or the host root
account. Model output, repository content, dependencies, tests, and tool output
remain untrusted inputs.

## Safe evidence

Provide only what is needed to reproduce and assess the issue:

- a concise impact statement and the affected Director/Paseo versions;
- minimal reproduction steps against data and systems you are authorized to
  test;
- exact Candidate, release tag, and artifact digests when known;
- bounded diagnostic codes and a redacted excerpt; and
- whether the issue is already public or actively exploited.

Remove secrets, private paths, remote URLs, repository contents, user data,
agent transcripts, prompts, raw logs, and TaskStore rows. Director support
bundles are generated locally, mode `0600`, and never uploaded automatically;
inspect and share one only through the private report when it is necessary.
Do not disrupt production systems, access another person's data, persist after
proof, or use destructive testing.

## Response and disclosure

Maintainers target acknowledgement within three business days and an initial
assessment within seven calendar days. These are best-effort response targets,
not an availability or remediation SLA. The assessment will establish scope,
severity, affected versions, a safe communication cadence, and whether an
advisory or coordinated fix is required.

Please keep the report private until a fix and advisory are available or a
coordinated disclosure date is agreed. Maintainers will minimize shared data,
credit reporters who request attribution, and publish enough remediation
information for users without exposing secrets or unnecessary exploit detail.

For non-security help, use the [support policy](SUPPORT.md).

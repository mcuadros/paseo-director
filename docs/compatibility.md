# Compatibility policy

This policy is the release-facing compatibility contract for Director for
Paseo `1.0`. It is exact and capability-based; a nearby semantic version does
not inherit support.

## Host and runtime matrix

| Component | Supported `1.0` value | Notes |
|---|---|---|
| Host OS/architecture/libc | Linux amd64 with glibc | Other targets require a new plan decision and release evidence. |
| Paseo | Exact stable `0.7.2` | The `0.8` preview and every other version are unsupported. |
| Node.js | `>=22` | Connector code retains the Node 22 baseline. |
| npm | Lockfile-version-3 capable | Candidate preparation uses the committed lockfile only. |
| Default `main` bootstrap and Engine | Exact checkout compiled during declared install/update preparation | Requires exact Go `1.26.5` linux/amd64; reload never compiles. |
| Tagged alpha/beta/stable Engine | Pinned precompiled linux-amd64 asset | Never invokes Go or falls back to source. |
| Dolt | Canonical precompiled `2.3.2` linux-amd64 archive/executable | Digest- and size-bound in every channel; no system installation or `PATH` discovery. |

Exact Paseo `0.7.2` provides no public daemon-version accessor. Director
combines the host's plugin compile boundary, an exact CLI check, the locked
`@getpaseo/client@0.7.2` contract, and a fixed generated host-contract
descriptor. Any mismatch stops before runtime preparation or host mutation.
The connector uses only the handler-scoped object: the public `PaseoApi`
supplied to each plugin handler; there is no secondary Paseo URL, credential,
client, or bridge.

## Installation closure

The source checkout is Apache-2.0 and does not vendor `node_modules`, Paseo,
provider CLIs, Git, GitHub CLI, Dolt, Go, Node.js, npm, or another executable.
Paseo's candidate preparation installs the exact seven-package production npm
closure with dependency lifecycle scripts disabled, then verifies registry
origins, SHA-512 integrity, dependency edges, installed versions, license
identity, and the supported host.

The complete 580-entry npm lock graph deduplicates to 546 exact coordinates:
seven shipped production coordinates, 539 CI/build-only coordinates, and 38
platform-excluded entries on the supported glibc Linux amd64 host. The exact
`@getpaseo/cli@0.7.2` dependency is CI/build-only. Release notices contain only
the shipped plugin closure plus release-binary obligations; CI/build-only and
platform-excluded packages remain explicitly classified and omitted. Unknown,
incompatible, missing, stale, or duplicate licensing input blocks generation.
Non-SPDX `SEE LICENSE` declarations are never recast as permissive SPDX terms:
installed build-only license files are digest-verified, while exact
platform-excluded declarations remain non-shipped exclusions.

The release bootstrap statically includes Go's standard library; the release
Engine includes that standard library plus the two exact modules in
`engine/go.mod` and `engine/go.sum`. The generated notice asset includes their
exact checksums, source locations, attributions, license files, and the
MPL-2.0 source-availability statement for
`github.com/go-sql-driver/mysql`.

## Channel and runtime behavior

- Installing or updating the default `main` branch compiles exactly that clean
  checkout's Engine during declared candidate preparation. Candidate,
  contract, executable, and notices identity are bound before activation. This
  named behavior is not a fallback.
- Tagged alpha, beta, and stable releases accept only the committed published
  schema-2 descriptor, canonical GitHub Release URLs, exact source Candidate,
  linux-amd64 target, and non-empty Engine/notices/Dolt sizes and SHA-256
  values. They verify before execution, never compile, and never fall back to
  source.
- Plugin reload never compiles. It adopts or retries the already prepared exact
  channel artifacts under the plugin-owned supervisor.
- Every channel uses the canonical precompiled Dolt `2.3.2` asset and
  content-addressed atomic caches outside the checkout. Missing, partial,
  unsafe, stale, or conflicting state fails closed while known-good data and
  cache content remain preserved.

## Provider and tool compatibility

Git `2.47.3`, Dolt `2.3.2`, and GitHub CLI `2.97.0` are the tested tool
compatibility points. The admitted provider tuples are Codex CLI `0.147.0`,
Claude Code `2.1.258`, and OpenCode `1.18.18`, each on Paseo `0.7.2` with its
exact discovered model/variant, policy, session stdio MCP, authentication, and
rootless-OCI capability. Generic ACP and automatic provider/tool fallback are
unsupported.

## Restart-free activation

Supported installation, update, and configuration activation must be live and
plugin-scoped and must preserve unrelated agents and workspaces. Use
`paseo plugin reload director` to adopt or retry prepared artifacts. Stopping
the Paseo daemon or host is not a supported activation or recovery step.
Director installs no system service and the supervisor never kills a listener
or process whose ownership it cannot prove.

See [installation and update](installation-update.md) for packaging behavior and
[SUPPORT.md](../SUPPORT.md) for diagnostics and escalation.

# Support policy

Director for Paseo has not published a public beta or stable channel. This
policy defines the `1.0` support boundary and the named `main` versus tagged
release behavior. No channel may stop the Paseo daemon, restart the host,
install a system service, or interrupt unrelated agents or workspaces.

## Supported compatibility point

The only supported `1.0` host target is Linux amd64 with glibc, exact stable Paseo
`0.7.2`, Node.js 22 or newer, and npm with lockfile-version-3 support. Another
`0.7.x` version is not admitted by semantic-version inference. Paseo `0.8` is a
preview with incompatible packaging and is unsupported. Other operating
systems and architectures are outside `1.0`.

On Paseo `0.7.2`, each Director RPC receives the daemon user's complete public
API from its plugin handler. Director uses only that handler-scoped object. It
does not configure a daemon URL or credential, copy a password, or open a
secondary Paseo connection or bridge. Read the authority warning in the
[installation policy](docs/installation-update.md) before installing.

The tested external-tool compatibility points are:

| Capability | Requirement |
|---|---|
| Git | `2.47.3`; required for source, Candidate, worktree, and delivery operations |
| Dolt | Exact canonical `2.3.2` archive and executable; precompiled, digest-bound, and plugin-supervised in every channel |
| GitHub CLI | `2.97.0`; required only for GitHub publication, checks, feedback, and integration |
| Codex CLI | Exact `0.147.0` provider tuple |
| Claude Code | Exact `2.1.258` provider tuple |
| OpenCode | Exact `1.18.18` provider tuple |
| Rootless OCI | Required for every governed provider path |

Provider availability alone is insufficient. Doctor must also prove the exact
model/variant, session-scoped stdio MCP, tool policy, authentication, resource,
and isolation capabilities selected by the Project. Director never installs,
authenticates, upgrades, or silently substitutes these external tools.

Go `1.26.5` on Linux amd64 is required by declared candidate preparation when
installing or updating the default `main` branch, and by release CI/builders.
It is never invoked by plugin reload. Users of tagged alpha, beta, or stable
releases do not need Go.

## Deterministic preparation and channel behavior

Paseo prepares a Git candidate with exactly:

```text
npm ci --omit=dev --ignore-scripts --no-audit --no-fund
node tools/packaging/verify-install.mjs
```

The committed lockfile is authoritative. Registry, integrity, graph,
lifecycle-script, license, or compatibility drift rejects the candidate and
preserves the prior installation. `@getpaseo/client@0.7.2` is part of the
seven-package production closure; it is installed from the lock and is not
vendored.

- Default `main` is a named development channel. Its declared install/update
  preparation compiles the exact checked-out Go bootstrap, which builds the
  exact Engine source, binds Candidate, contract, executable, and notices
  identity, and atomically prepares the private cache. This is never an
  ambient or silent fallback.
- Tagged alpha, beta, and stable releases download only their precompiled
  Engine and notices assets, verify exact source/size/SHA-256 identity, and
  never invoke Go or fall back to source.
- Both paths download and verify the canonical precompiled Dolt `2.3.2`
  archive/executable and run Engine and Dolt under the plugin-owned Go
  controller. Neither path discovers a runtime through `PATH` or systemd.
- `paseo plugin reload director` only adopts or retries already prepared exact
  artifacts. Reload never compiles.

Activation is live and plugin-scoped through `paseo plugin reload director`.
It preserves unrelated agents/workspaces and never restarts the daemon or host.

## Diagnose before requesting help

1. Record the exact Director commit or release tag, Paseo version, Linux target,
   and bounded error code.
2. Run Director Doctor and resolve only the capability it identifies. Do not
   switch host, provider, delivery mode, Paseo authority, or channel as an
   implicit fallback.
3. For install/update problems, run `paseo plugin ls --json` and
   `paseo plugin logs director --json`; retain the bounded code and current
   commit. A failed candidate should leave the prior commit active.
4. For runtime resolution, retain only the channel, version, source Candidate,
   target, Engine/notices/Dolt size and digests, connector commit, supervisor
   state, and contract version/hash.
5. If more context is necessary, Preview and then Generate the local redacted
   support bundle described in
   [operations and diagnostics](docs/operations-diagnostics.md). Inspect it
   before sharing; Director never uploads it.

Never paste credentials, private paths, remote URLs, repository contents,
prompts, transcripts, raw logs, TaskStore rows, or unreviewed support bundles
into an issue.

## Where to ask

- For a reproducible non-security Director defect or documentation problem,
  use [GitHub Issues](https://github.com/mcuadros/paseo-director/issues) after
  redacting the report.
- For a suspected vulnerability, follow [SECURITY.md](SECURITY.md) and use the
  private reporting path only.
- For an upstream Paseo, provider, Git, GitHub CLI, Dolt, Go, Node.js, npm, or
  Linux defect that does not cross a Director boundary, use that project's
  support channel.
- Requests outside the declared host, version, provider, tool, or authority
  boundary may be closed as unsupported. Expanding support requires new
  reviewed evidence and an explicit compatibility decision.

Support is best effort. There is no warranty, uptime SLA, paid support promise,
or automatic upload/telemetry channel.

# Licensing and third-party notices

Director source is licensed under the Apache License, Version 2.0. The
unmodified English terms are in the root [LICENSE](../LICENSE), and the root
`package.json` plus its lockfile declare the SPDX identity `Apache-2.0`.
Dependencies retain their own licenses.

Director for Paseo is an independent community plugin. It is not affiliated
with, endorsed by, maintained by, or sponsored by Paseo. `Paseo` is used only
to describe compatibility. Director does not use Paseo logos or claim to be
official, certified, or a partner.

## Exact dependency inventory

`third_party/license-policy.json` records the reviewed exception boundary:

- the exact Paseo `0.7.2` source and Apache-2.0 evidence for the seven npm
  packages whose published metadata/archive omits a license field, including
  the three shipped packages and CI-only `@getpaseo/cli@0.7.2`;
- every exact Go module, license identity, source location, attribution,
  reviewed repository license-text path, and license-file digest; and
- the exact Go `1.26.5` linux-amd64 standard-library license and patent text.

`tools/release/notices.mjs` combines that policy with `package-lock.json`,
installed package metadata from the integrity-locked npm archives,
the exact Go toolchain's cache-free parse of `engine/go.mod`, every checksum in
`engine/go.sum`, and the reviewed license texts under `third_party/licenses/`.
It fails closed with bounded codes on unknown or incompatible SPDX expressions,
a missing or stale exception, unsafe or changed license text, an unreviewed
non-SPDX declaration, an unlocked/replaced module, a changed toolchain,
duplicate entries, or an incomplete installed closure.

License inventory does not download or locate Go module sources. It succeeds
with an empty `GOMODCACHE` and `GOPROXY=off`, rejects replacements and extra or
missing checksums, and verifies the committed license-text copies against the
same hashes recorded from their exact upstream modules. Release compilation
remains a separate controlled step with its own pinned offline module closure.

The inventory distinguishes:

- the 580-entry npm lock, deduplicated to 546 exact coordinates;
- the seven-package shipped npm production closure, including
  `@getpaseo/client@0.7.2`;
- 539 CI/build-only coordinates, including exact CI-only
  `@getpaseo/cli@0.7.2`, and 38 supported-host platform exclusions;
- Go's standard library linked into both linux-amd64 release binaries and
  every module linked into Director Engine; and
- packages that are inputs but are not redistributed as `node_modules`.

After a Candidate is committed and the source is clean, generate and verify the
release asset with new absolute owner-only paths:

```text
node tools/release/notices.mjs generate --source . --candidate <candidate-sha> --installed-scope all --output <new-private-notices-path>
node tools/release/notices.mjs verify --source . --candidate <candidate-sha> --installed-scope all --notices <private-notices-path>
```

The output is deterministic: it has no timestamp or host path, binds the exact
source Candidate and lock/module/policy digests, emits one canonical inventory
containing only the shipped plugin and release-bootstrap/release-Engine
obligations, and
preserves their license and NOTICE texts. CI/build-only packages are audited
and classified but omitted from the shipped notice. Exact build-only
`SEE LICENSE` declarations retain their own non-SPDX terms and are never
described as permissive dependencies; platform-excluded variants are omitted.

Release CI passes the verified notice as the required `--notices` input to the
current release builder together with canonical Dolt `2.3.2` inputs. The
builder re-verifies notices against the exact source before compiling and
preserves binary/notices/Dolt sizes and SHA-256 values in schema-2 metadata.
Binary identity, release metadata, and the connector bind the same notice
digest.

The named `main` channel license-audits its production-only npm installation;
the Go bootstrap binds a compact exact-build notice to its source-built Engine.
The public `THIRD_PARTY_NOTICES.txt` asset is generated from the all-scope
release-CI closure and is not an installed-host fallback. Plugin reload never
generates notices or compiles.
Tagged alpha, beta, and stable releases always consume the all-scope release-CI
notice and precompiled Engine asset; they never fall back to source.

The engine includes `github.com/go-sql-driver/mysql@v1.10.1` under MPL-2.0 as
part of a Larger Work. The generated notice provides the exact Source Code Form
location and full MPL-2.0 terms. Director carries no modified copy of that
module.

## External prerequisites

Paseo, Git, GitHub CLI, provider CLIs, Go, Node.js, and npm remain separately
owned prerequisites where their feature requires them. Director's plugin-owned
runtime downloads only two declared artifact families: its channel-selected
Engine/notices closure and canonical upstream Dolt `2.3.2`. Tagged releases use
a precompiled bootstrap and Engine; default `main` compiles its exact bootstrap
and Engine only through declared preparation. Every Dolt channel uses the
precompiled canonical archive. There is no silent fallback or system-service
discovery.

| Prerequisite | Director feature and tested bound | Official source and terms owner | Operator responsibility |
|---|---|---|---|
| Paseo | Plugin host, exact `0.7.2` | [Paseo `v0.7.2` source and Apache-2.0 license](https://github.com/getpaseo/paseo/tree/v0.7.2) | Install Paseo and accept that each trusted handler receives the daemon user's complete public API; Director opens no second connection. |
| Git | Source/worktree/delivery, tested `2.47.3` | [Git source and GPL-2.0 terms](https://github.com/git/git/blob/master/COPYING) | Install on the daemon host and configure repository credentials. |
| GitHub CLI | Optional GitHub delivery, tested `2.97.0` | [GitHub CLI source and MIT terms](https://github.com/cli/cli) | Install and authenticate only when GitHub features are enabled. |
| Dolt | TaskStore runtime, exact `2.3.2` | [Dolt release and Apache-2.0 project terms](https://github.com/dolthub/dolt/tree/v2.3.2) | The plugin downloads the canonical precompiled archive, verifies archive/executable size and digest, and supervises it privately; no system Dolt is selected. |
| Codex CLI | Optional provider, exact `0.147.0` tuple | [OpenAI Codex source, installation, and Apache-2.0 terms](https://github.com/openai/codex) | Install and authenticate the exact admitted tuple; accept provider service terms. |
| Claude Code | Optional provider, exact `2.1.258` tuple | [Anthropic Claude Code setup and provider terms](https://docs.anthropic.com/en/docs/claude-code/getting-started) | Install and authenticate the exact admitted tuple; accept Anthropic/service-provider terms. |
| OpenCode | Optional provider, exact `1.18.18` tuple | [OpenCode installation and project terms](https://opencode.ai/v2/docs) | Install and authenticate the exact admitted tuple and selected model provider. |
| Node.js/npm | Connector runtime and locked npm preparation; Node `>=22` | [Node.js](https://nodejs.org/) and [npm CLI](https://github.com/npm/cli) projects | Install compatible runtimes; do not enable package lifecycle scripts for Director preparation. |
| Go | Default-main candidate preparation and release building, exact `1.26.5` linux-amd64 | [Go source and BSD-3-Clause terms](https://go.googlesource.com/go/+/refs/tags/go1.26.5) | Required to install/update `main` and in release CI; never used by reload or tagged releases. |
| Rootless OCI runtime | Mandatory governed-provider isolation | Host-selected implementation under its own license/terms | Install and configure the capability that Doctor verifies; no weaker fallback is supported. |

Any dependency, submodule, generated bundle, release asset, or copied upstream
content added later must enter the exact inventory and pass review before a
release. An unknown, unlicensed, incompatible, missing, stale, or duplicate
entry is a release blocker.

# `dir-m0.9` licensing and public-distribution evidence

- **Captured:** 2026-09-06
- **Task:** `dir-m0.9`
- **Base:** `origin/main` at `fb1257edc84fc8d1cf721a108113b96807d60699`
- **Scope:** source distribution of Director from a public Git repository through Paseo's supported Git plugin lifecycle
- **Topology:** an isolated Paseo daemon and disposable local Git remote on one Linux host; no public ref, package, plugin, or service was created
- **Result:** the hypothesis passed within the compatibility and release gates recorded in ADR-0009

This is engineering license due diligence, not legal advice. URLs were read on 2026-09-06.

The refreshed base includes accepted ADR-0002's exact-`0.7.2` public subset and ADR-0008's independently reviewed same-user security No-go. The former agrees with this evidence's exact-version boundary; the latter remains an independent M0 stop and is not relaxed by the licensing Go.

## Environment

```text
Debian GNU/Linux 13.6 (trixie), x86_64
Linux 6.12.107+deb13-amd64
Paseo CLI and daemon 0.7.2
Node.js v26.7.0
npm 11.19.0
Git 2.47.3
```

Commands:

```sh
sed -n '1,80p' /etc/os-release
uname -a
paseo --version
node --version
npm --version
git --version
```

## Primary sources

| Source | Version/date bound | Relevant fact |
|---|---|---|
| [Paseo plugin versions](https://paseo.sh/docs/plugins) | Accessed 2026-09-06 | `0.7` is current/stable; `0.8` is preview; the plugin API is experimental. |
| [Paseo v0.7 plugin quickstart](https://paseo.sh/docs/plugins/v0.7) | `0.7.x`, accessed 2026-09-06 | Git source syntax, explicit updates, host-provided modules, trusted/unsandboxed execution, and build preparation behavior. |
| [Paseo v0.7 plugin reference](https://paseo.sh/docs/plugins/v0.7/reference) | `0.7.x`, accessed 2026-09-06 | Runtime modules, Git ref tracking, pinned tags/commits, build ordering, candidate rejection, and lifecycle commands. |
| [Paseo `v0.7.2` release](https://github.com/getpaseo/paseo/releases/tag/v0.7.2) | 2026-09-02, commit `9400a49af670fdb5db4af58e73f8df98588dbea9` | Release added declared build commands and monorepo paths for Git-hosted plugins. |
| [Paseo `v0.7.2` license](https://github.com/getpaseo/paseo/blob/v0.7.2/LICENSE) | Exact release tag | Paseo-authored code is Apache-2.0; incorporated third-party components retain their licenses. |
| [Paseo terms](https://paseo.sh/terms) | Last updated 2026-08-29 | Terms govern official services, affirm that Paseo is Apache-2.0, and do not replace or restrict the open-source license. |
| [Paseo related projects](https://paseo.sh/docs/community) | Accessed 2026-09-06 | The official community page identifies independent integrations with descriptive names such as `Paseo for VS Code`; it is evidence of naming convention, not a trademark license. |
| [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0.txt) | Version 2.0 | Copyright/patent grant, redistribution duties, and no general trademark grant; section 6 only preserves limited customary description of the licensed work's origin and NOTICE. |
| [ASF third-party license policy](https://www.apache.org/legal/resolved.html) | Accessed 2026-09-06 | Apache-2.0 and MIT/X11 are Category A compatibility references; external tools need not be bundled. This is compatibility guidance, not a claim that Director is an ASF project. |
| [Applying Apache-2.0](https://www.apache.org/legal/apply-license) | Accessed 2026-09-06 | Include the English Apache-2.0 license in a source distribution and preserve required notices. |
| [npm `@getpaseo/plugin@0.7.2`](https://www.npmjs.com/package/@getpaseo/plugin/v/0.7.2) | Exact published package | Public development package used by the official scaffold; its package metadata and tarball caveat are recorded below. |

The official source was inspected at the exact release commit:

```sh
git clone --depth 1 --branch v0.7.2 \
  https://github.com/getpaseo/paseo.git <upstream-checkout>
git -C <upstream-checkout> rev-parse HEAD
git -C <upstream-checkout> describe --tags --exact-match
```

```text
9400a49af670fdb5db4af58e73f8df98588dbea9
v0.7.2
```

Selected source hashes:

```text
79d5aedce6aa0adc547336dc1bd34c5cc9308ba110fac7079ed97515ee573ad3  LICENSE
7394fce3edce2872d500cf7474248202d915d1172fd749827bfc5e03f13cf908  package.json
17f99b5bacf0beab0de98f1b06aba5883d1ac2c061109f6f0c094692a938334d  packages/plugin/package.json
16ebd83a7b5a39971544201e955ef54dabfedda4c33f317aaabd5d926f564f4b  public-docs/plugins/index.md
769c890c05bbcd5769e0f54a2709d35106ca5c63fe580723d1eb4e585bc0dd02  public-docs/plugins/reference.md
973e5bff1109b7d36cd9daff57e6b8f61b0414b568cb8f6f490c34e4ce50e33f  packages/server/src/server/plugins/managed-source.ts
646eb5ddde6f4de1d45e82cf999b2ab2b80d07bbf24f731ca9f22a7a0cb0fa6a  packages/server/src/server/plugins/index.ts
8664b52d8f2782fa6da1b45f76a755bd52f7e97c207cea0431b01aeeafc26271  packages/server/src/server/plugins/preparation.ts
ebe81940f103cad04e6eea9fbb70534976f99c618e421941ba132e407b1c3276  packages/server/src/server/plugins/managed-source.posix.test.ts
a6c1e33c0a6c45313e4a0b90cccc02e55d0855124daf14eb11c6f6611415ae73  packages/server/src/server/plugins/index.posix.test.ts
420412d861c70fbe803f463b52d30e766412123ce4345a18f62dfd33a2b45dc6  packages/cli/tests/e2e/plugin-lifecycle.test.ts
```

## Paseo and plugin SDK license chain

At tag `v0.7.2`, Paseo's root `package.json` identifies `Apache-2.0`, and the root `LICENSE` grants Apache-2.0 for Paseo-authored code. The official service terms independently state that the open-source software is Apache-2.0 and that those service terms do not restrict that license.

The plugin API is consumed through module specifiers such as `@getpaseo/plugin`. Paseo supplies the runtime instances. The generated scaffold installs them only as development dependencies for local typechecking and tests. A clean Git install of the generated fixture succeeded without installing its `node_modules`, confirming that the daemon compiles against its host-provided runtime.

Apache-2.0 permits use and distribution of the SDK and separate plugins under Apache-2.0. Director does not modify or redistribute Paseo itself. If any Paseo code is copied or any Paseo artifact is later bundled, Apache section 4 duties apply to that distribution.

### Published SDK package caveat

The exact npm package has no `license` field and ships no `LICENSE` file, even though its corresponding tagged source is Apache-2.0:

```sh
npm view @getpaseo/plugin@0.7.2 \
  name version license repository dist.tarball --json
npm pack @getpaseo/plugin@0.7.2 --json
```

```json
{
  "name": "@getpaseo/plugin",
  "version": "0.7.2",
  "dist.tarball": "https://registry.npmjs.org/@getpaseo/plugin/-/plugin-0.7.2.tgz"
}
```

```text
tarball sha1:   044293e87b4275c2dcd8d078d810c1cba2ff175d
tarball sha512: uOhaMdxqgIkETjw+yAGRyZ84yJrpb1ECwBRj9f6cSNHeUwNj/1ohwa/oCfmdnoiX57s3oYYkY+7uE8bUy2Q/Fg==
entries:        25
bundled:        []
LICENSE entry:  absent
package license field: absent
```

This omission does not block Director's Git source distribution because Director neither vendors nor redistributes this tarball: it is a development-only input, and Paseo provides the runtime module. Director must not vendor `@getpaseo/plugin` or ship `node_modules`. If a future artifact includes any Paseo code, it must carry the applicable upstream license and notices, and the dependency audit must be repeated.

## Generated scaffold dependency baseline

The installed `paseo plugin init` command generated these development dependencies. Exact npm registry metadata was queried on 2026-09-06:

| Package | Queried version | Registry license | Distribution role |
|---|---:|---|---|
| `@getpaseo/plugin` | `0.7.2` | Metadata absent; tagged source Apache-2.0 | Host-provided runtime; development types only |
| `@tanstack/react-query` | `5.90.11` | MIT | Host-provided runtime; development only |
| `@types/react` | `19.2.0` | MIT | Development only |
| `react` | `19.1.0` | MIT | Host-provided runtime; development only |
| `react-native` | `0.81.5` | MIT | Host-provided runtime; development only |
| `typescript` | `5.9.3` | Apache-2.0 | Development only |
| `zod` | `4.4.3` | MIT | Host-provided runtime; development only |

Commands:

```sh
paseo plugin init <fixture> --id dir-license-probe --json
npm view @getpaseo/plugin@0.7.2 name version license repository dist.tarball --json
npm view @tanstack/react-query@5.90.11 name version license repository --json
npm view @types/react@19.2.0 name version license repository --json
npm view react@19.1.0 name version license repository --json
npm view react-native@0.81.5 name version license repository --json
npm view typescript@5.9.3 name version license repository --json
npm view zod@4.4.3 name version license repository --json
```

MIT and Apache-2.0 are compatible with an Apache-2.0 Director distribution. This is only the official empty-scaffold baseline, not a license audit of future product dependencies. `dir-m6.4` already requires complete dependency notices before release.

## External prerequisite boundary

Director's planned external executables are not linked into or redistributed with the plugin:

| Prerequisite | Upstream license/source | Director rule |
|---|---|---|
| Paseo daemon/CLI | [Apache-2.0](https://github.com/getpaseo/paseo/blob/v0.7.2/LICENSE) | Host platform; compatible versions declared, never bundled by Director |
| Git | [GPL-2.0 family](https://github.com/git/git/blob/master/COPYING) | User-installed executable; never bundled |
| GitHub CLI | [MIT](https://github.com/cli/cli/blob/trunk/LICENSE) | User-installed executable for GitHub features; never bundled |
| Beads, if selected by its separate M0 gate | [MIT](https://github.com/gastownhall/beads/blob/main/LICENSE) | User-installed TaskStore executable; never downloaded silently |
| Dolt, if selected by its separate M0 gate | [Apache-2.0](https://github.com/dolthub/dolt/blob/main/LICENSE) | User-installed service/executable; never downloaded silently |
| Agent-provider CLIs | Provider-specific | User installs and authenticates; Doctor links to provider terms and install instructions |

Running a separate executable does not distribute that executable. Director documentation and Doctor must disclose every required tool, tested version/capability, install source, license/terms owner, and whether a feature is optional. No Director install or update may automatically download an undeclared binary.

## Reproduced Git install and update lifecycle

### Setup

The official scaffold was created in a disposable directory, committed to a local Git repository, and served to an isolated daemon through a `file://` Git remote. This exercises the same Git source manager used for network remotes while keeping the experiment private and deterministic.

```sh
paseo plugin init <probe-root>/source --id dir-license-probe --json
git -C <probe-root>/source init -b main
git -C <probe-root>/source config user.name 'Director Spike'
git -C <probe-root>/source config user.email 'director-spike@example.invalid'
git -C <probe-root>/source add -A
git -C <probe-root>/source commit -m 'initial plugin fixture'
paseo daemon start \
  --home <probe-root>/home \
  --listen 127.0.0.1:17679 \
  --foreground --no-relay --no-mcp --no-web-ui
```

The initial fixture commit was `55d19421ce09cde3cf8fbc765ca60d5a0f2b7b0e`.

### Default-branch install

```sh
paseo plugin add file://<probe-root>/source \
  --host 127.0.0.1:17679 --json
```

Sanitized output:

```text
Trusting plugin code: server code and Git build commands run unsandboxed on the daemon host; client code runs inside Paseo. Dependencies and future updates are part of the codebase you trust.
{
  "id": "dir-license-probe",
  "enabled": true,
  "status": "disabled",
  "source": "git",
  "remote": "file://<probe-root>/source",
  "ref": "main",
  "commit": "55d19421ce09cde3cf8fbc765ca60d5a0f2b7b0e"
}
```

`disabled` is expected because the isolated daemon's global plugin switch remained off. Installation still cloned, resolved, validated, and recorded the exact commit. No configuration was edited manually.

### Tracked update

After committing a safe fixture change as `1966e066adb1abc012e899242f20a309b4f292f2`:

```sh
paseo plugin status dir-license-probe --host 127.0.0.1:17679 --json
paseo plugin update dir-license-probe --host 127.0.0.1:17679 --json
```

```json
[
  {
    "id": "dir-license-probe",
    "source": "git",
    "ref": "main",
    "currentCommit": "55d19421ce09cde3cf8fbc765ca60d5a0f2b7b0e",
    "latestCommit": "1966e066adb1abc012e899242f20a309b4f292f2",
    "commitsBehind": 1,
    "updateAvailable": true
  }
]
```

```json
[
  {
    "id": "dir-license-probe",
    "previousCommit": "55d19421ce09cde3cf8fbc765ca60d5a0f2b7b0e",
    "currentCommit": "1966e066adb1abc012e899242f20a309b4f292f2",
    "commits": 1,
    "updated": true
  }
]
```

Status inspection did not install the newer commit; `update` was explicit. The installed record then reported the exact new SHA.

### Pinned tag

Tag `v1` was created at the second commit, and the same source was installed under another runtime ID:

```sh
paseo plugin add file://<probe-root>/source \
  --id dir-license-probe-pinned --ref v1 \
  --host 127.0.0.1:17679 --json
```

After `main` advanced to `1f2d675c078c8f91a48943e19043bafff057962f`, combined status returned:

```json
[
  {
    "id": "dir-license-probe",
    "ref": "main",
    "currentCommit": "1966e066adb1abc012e899242f20a309b4f292f2",
    "latestCommit": "1f2d675c078c8f91a48943e19043bafff057962f",
    "commitsBehind": 1,
    "updateAvailable": true
  },
  {
    "id": "dir-license-probe-pinned",
    "ref": "v1",
    "currentCommit": "1966e066adb1abc012e899242f20a309b4f292f2",
    "latestCommit": "1966e066adb1abc012e899242f20a309b4f292f2",
    "commitsBehind": 0,
    "updateAvailable": false
  }
]
```

This confirms that a branch is an update channel while a tag is an immutable pin.

### Failed build preserves installed commit

The third commit declared one safe failing build command:

```json
{"build":[["node","-e","process.exit(7)"]]}
```

```sh
paseo plugin update dir-license-probe \
  --host 127.0.0.1:17679 --no-color
```

```text
exit: 1
Error: Request failed: Plugin build command failed (exit 7): "node" "-e" "process.exit(7)" requestType=plugin.source.update.request code=handler_error
```

The following `paseo plugin ls --json` still reported the branch installation at `1966e066adb1abc012e899242f20a309b4f292f2`; the pinned installation was unchanged; and the managed `.staging` directory was empty. This matches the documented candidate-first replacement model.

### Cleanup

```sh
paseo plugin remove dir-license-probe --host 127.0.0.1:17679 --json
paseo plugin remove dir-license-probe-pinned --host 127.0.0.1:17679 --json
paseo plugin ls --host 127.0.0.1:17679 --json
# Send SIGINT to the foreground daemon and wait for "Server closed".
```

Final list output was `[]`, the daemon logged a graceful shutdown, and the entire disposable root was removed. The isolated daemon had downloaded approximately 986 MiB of host-owned speech-model data despite the unrelated `--no-relay --no-mcp --no-web-ui` flags; that data was contained under the disposable home and removed with it. Director itself downloaded no binary or model.

The upstream Paseo clone and npm tarball used for evidence inspection were also removed after hashes and observations were recorded.

## Distribution implications

The supported model is Git source distribution, not an npm publication of Director:

- `stable` and `beta` are reviewed moving Git branches created at release time. They are explicit update channels.
- Stable install documentation uses `paseo plugin add mcuadros/paseo-director --ref stable`.
- Beta documentation uses `--ref beta`.
- An exact release tag or commit is an immutable installation and does not advance through `paseo plugin update`; documentation must say how to switch refs or reinstall.
- `paseo plugin update director` is an explicit trust decision and installs the exact resolved channel commit. Director does not claim silent or automatic updates.
- Every channel promotion is tied to a reviewed release Candidate and an immutable semantic-version tag.
- Channel branches are protected and fast-forward-only; a release process never rewrites an installed update channel.
- The root plugin manifest exposes any preparation commands. Prefer no `build` commands. If one is required, use direct argv, a committed lockfile, and a release-audited dependency closure.
- Every Git submodule is part of the trusted source closure and must pass the same license/provenance audit; Director should avoid submodules unless justified.
- Never commit or distribute `node_modules`, credentials, generated local state, provider CLIs, Git, GitHub CLI, Beads, or Dolt.
- `dir-m1.2` must add the Apache-2.0 `LICENSE` and package SPDX metadata with the first product scaffold. `dir-m6.4` must revalidate them and add dependency notices for shipped content, prerequisite disclosures, and a reproducible license inventory before public beta.

## Naming and trademark boundary

Apache-2.0 section 6 does not grant general rights to Paseo names, marks, logos, or product identity. Its narrow exception concerns customary description of the licensed work's origin and reproduction of NOTICE content; Director does not treat that exception as a trademark license for its own brand. The approved name **Director for Paseo**, repository slug `paseo-director`, and plugin ID `director` instead use Paseo nominatively to describe compatibility and keep Director's own identity distinct. Paseo's official related-projects page uses comparable descriptive community names, including `Paseo for VS Code`, but does not publish a general permission.

Public materials must:

- say that Director is an independent community plugin and is not affiliated with, endorsed by, or maintained by Paseo;
- use `Paseo` only as necessary to describe compatibility;
- not use Paseo logos, copied trade dress, `official`, `certified`, or partnership claims without separate permission;
- preserve third-party names and notices only as required for attribution;
- re-run this check if Paseo publishes a specific trademark/plugin naming policy or requests a naming change.

No separate official Paseo trademark or community-plugin naming policy was found in the scoped official docs or repository on 2026-09-06. The conservative descriptive-use boundary therefore remains mandatory.

## Acceptance checks

| Check | Result |
|---|---|
| Paseo `v0.7.2` and plugin SDK have a public license compatible with Apache-2.0 Director source | Pass, with the npm tarball notice caveat and no-vendoring rule |
| Official service terms add no restriction to local open-source plugin distribution | Pass |
| Official, supported Git install/update mechanism exists | Pass |
| Branch tracking, tag pinning, exact-SHA reporting, failed-candidate preservation, and cleanup are reproducible | Pass on Linux with installed `0.7.2` |
| Dependency and external-binary disclosure model is defined | Pass; exact release inventory remains a `dir-m6.4` gate |
| Product naming remains within a conservative descriptive-use boundary | Pass with mandatory disclaimer/no-logo rules |
| Public artifact/ref was created during the spike | Pass: none created |

Windows runtime behavior was not exercised by this licensing spike, and no compatibility beyond exact Paseo `0.7.2` is inferred. The license result is platform-neutral; later stable Paseo releases and the plan's separate Windows/Linux lifecycle and release-candidate gates remain subject to fresh validation.

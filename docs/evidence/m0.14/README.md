# M0.14 Linux agent-authority-separation evidence

- **Task:** `dir-m0.14`
- **Result:** No-go for the smallest public per-provider rootless-OCI path
- **Evidence date:** 2026-09-06
- **Scope:** Linux x86-64 only; no other host operating system is evaluated or
  implied
- **Topology:** one Debian 13 host, one nonce-owned rootless Podman store, one
  isolated Paseo daemon, and only disposable no-remote Git repositories below
  an owned temporary root
- **Evidence class:** executable adversarial contract plus exact installed-
  artifact inspection

## Question and stopping rule

Can exact Paseo 0.7.2 use its public native-provider command override to put
every provider admitted by ADR-0005 inside one per-Run rootless OCI boundary
while preserving provider authentication and session-scoped stdio MCP, but
denying engine state and credentials, the source checkout and sibling
workspaces, raw Paseo controls, Git/GitHub delivery authority, runtime control,
and unapproved network access?

The path must also contain repository lifecycle code, preserve an isolated
Git checkout that can produce a Candidate, enforce resource limits, reconcile
interruption without duplication, and clean every owned resource. Any one
common pre-provider failure is a No-go for all three rows; it is not necessary
or safe to spend authenticated model turns after such a failure.

## Exact environment

| Component | Observed value |
|---|---|
| Host | Debian GNU/Linux 13 (trixie), x86-64 |
| Kernel | Linux `6.12.107+deb13-amd64` |
| Paseo | `0.7.2` |
| Node.js | `v26.7.0`; the product floor remains Node.js 22 |
| Git | `2.47.3` |
| util-linux `unshare` | `2.41.5` |
| Podman | `5.4.2`, rootless, cgroup v2 with `cgroupfs`, OverlayFS |
| Codex | `codex-cli 0.147.0` |
| Claude Code | `2.1.258 (Claude Code)` |
| OpenCode | `1.18.18` |
| OCI image | `docker.io/library/node@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5` |
| OCI image ID | `6e6261159fd399ebe5a3d556b7d89da9c85c873f3f270918aad6c8107da8b411` |

Exact provider executable fingerprints:

```text
134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477  Codex JavaScript launcher
cb0a15567e9a60a5820d54b0f6ae86d504dc3805c1eab21a47f70e3eb7b73a40  Codex Linux native executable
704f1334ac65d3e89e1c6c1d7663293ad786a6166afdb71b5075337df630f976  Claude Code executable
bb71f45b564f9234a97f54d6252a4a41d2f4388ae4b078918f691824cc3b3e54  OpenCode executable
```

The exact installed `provider-config.d.ts` hash is
`df160878fa43a0d3b397873e21053b1b8ac2dc3ccf48afc707203b46751b3a16`.
It declares array-valued provider command replacement. The exact installed
`worktree-session.js` and `utils/worktree.js` hashes are respectively
`9ba72cf9814e205285100843900fb0adfccbc46bebcff28b095817cb4839c771` and
`1cd0890689e666c062811e0cc3a76e35e4f653c75a0792640fd34bbb40121f30`.
The live lifecycle fixture, rather than those files alone, supplies the
authority result below.

## Primary public contracts

Consulted on 2026-09-06:

| Source | Contract used |
|---|---|
| [Paseo custom providers](https://paseo.sh/docs/custom-providers) | A provider `command` array fully replaces its default launch command and can name a wrapper or container image. |
| [Paseo provider options](https://paseo.sh/docs/sdk/provider-options) | Provider-native options are not a host boundary; provider CLIs run with the daemon user's authority. |
| [Paseo SDK reference](https://paseo.sh/docs/sdk/reference) | Public workspace creation, agent configuration, per-session MCP, observation, and archive surface. |
| [Paseo worktrees](https://paseo.sh/docs/worktrees) | Committed setup and teardown values are repository-controlled shell commands. |
| [Paseo security](https://paseo.sh/docs/security) | Local daemon reachability and host authentication assumptions. |
| [Podman rootless mode](https://docs.podman.io/en/stable/markdown/podman.1.html) | Rootless Podman creates a user namespace and owns user-specific container state. |
| [Podman run](https://docs.podman.io/en/latest/markdown/podman-run.1.html) | Filesystem, process tree, network namespace, mounts, read-only root, capabilities, cgroups, tmpfs, and limit options. |
| [Git repository layout](https://git-scm.com/docs/gitrepository-layout) | `HEAD` and `refs/worktree/*` are per-worktree while ordinary refs are shared; the linked-worktree gitdir is the owned writable administrative scope. |
| [Git environment](https://git-scm.com/docs/git) | `GIT_OBJECT_DIRECTORY` receives new objects and `GIT_ALTERNATE_OBJECT_DIRECTORIES` exposes immutable shared objects for read-only lookup. |
| [Git bundle](https://git-scm.com/docs/git-bundle) | Bundles provide public offline object/ref transfer, verification, listing, and fetch/import. Git documents no bundle-contract change from 2.41.1 through 2.47.3. |
| [systemd execution environment](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html) | A distinct service identity is possible only through a separately administered service boundary; it is not a Paseo per-session launch primitive. |

Moving documentation is not the compatibility authority. The exact installed
versions, schemas, hashes, and live observations above bound the decision.

## Reproduction

Run from a clean checkout on the exact Linux tuple. The parent environment
must permit rootless user namespaces; the full path also requires cgroup v2.
Both modes create resources only below one `mkdtemp` root, use no provider,
Git, GitHub, Paseo, or Beads credential, and remove that root after exact
ownership and absence checks. The full mode downloads only the digest-pinned
public OCI image.

```sh
node docs/evidence/m0.14/reproduce.mjs --git-only
node docs/evidence/m0.14/reproduce.mjs --full
```

The focused command runs only the corrected public-Git Candidate/bundle/import
fixture and its cleanup. The full command then runs, in order:

1. exact host and installed-artifact checks;
2. each admitted provider executable's `--version` within the same restricted
   rootless-OCI profile;
3. deterministic filesystem, credential, process, raw-control, network, and
   limit attacks;
4. a complete initialize/list/call/reject stdio MCP exchange through OCI;
5. the focused private-object Candidate/bundle/import fixture with an
   OS-enforced read-only shared common directory;
6. detached controller exit, exact label/ID reconciliation, termination,
   idempotent absence, and cleanup; and
7. real Paseo `kind: "worktree"` setup and teardown from a hostile disposable
   repository against an isolated daemon.

The final run exited zero with `result: "no-go"`. All reported paths are
logical categories; the output contains no temporary path, environment value,
credential, repository content, daemon log, or model transcript.

## Positive containment observations

The rootless OCI layer is useful but incomplete:

| Surface | Observed result |
|---|---|
| Codex, Claude Code, OpenCode exact executable | Each returned its exact version inside the restricted container. This is binary compatibility, not provider admission. |
| Writable scope | The owned workspace accepted a nonce write. Source, sibling, engine, and host-home reads/writes returned `ENOENT`. |
| Engine credential | The unmounted dummy engine credential returned `ENOENT`. |
| Provider credential | A nested read-only dummy provider-auth mount was readable. Read-only mounting prevents modification, not disclosure to a compromised provider. |
| Raw controls | `paseo`, `gh`, `docker`, and `podman` executables and Docker/Podman sockets were absent; host daemon loopback returned `ECONNREFUSED`. |
| Network | Direct public IPv4 returned `ENETUNREACH` under `--network=none`. This also prevents authenticated provider API traffic. |
| Stdio MCP | Initialize succeeded, the catalog contained only `read_scope_nonce`, one nonce call succeeded, an out-of-scope call returned `-32602`, and stdin closure was observed. |
| Process authority | UID/GID were 1000 inside a user namespace, `NoNewPrivs` was 1, and effective capabilities were zero. |
| Limits | `memory.max=268435456`, `pids.max=32`, `cpu.max="50000 100000"`, a file-size limit was present, `/tmp` was exactly 8 MiB, and the 22nd concurrent child was refused. |
| Recovery | After the detached launcher exited, one exact labeled container remained `running`; reconciliation found that ID, stopped/removed it, and retry observed absence status 1 with zero residual containers. |
| Git Candidate transfer | A mount namespace kept the shared common directory read-only while the Task worktree's own gitdir and a private object directory stayed writable. Git produced a real Candidate, exposed it through `refs/worktree/*`, bundled it, and the engine imported and verified the exact SHA and content. |

The exact stdio MCP proof reuses the already-reviewed M0.3 one-tool server as
an immutable fixture. This run proves the transport and scope behavior through
the OCI boundary. It does not pretend that an authenticated native-provider
turn occurred.

## Falsifying observations

### Repository lifecycle precedes the provider boundary

The hostile no-remote repository committed both setup and teardown commands.
Paseo's public `workspaces.create({source: {kind: "worktree", ...}})` ran each
command as the daemon user without any provider agent or wrapper.

Both phases independently observed all of the following:

```text
credentialReadable=true
engineWritable=true
sourceWritable=true
rawDaemonReachable=true
paseoExecutableReachable=true
```

The credential was a dummy sentinel. The process read it but did not record its
contents. The isolated daemon received a minimal environment and a temporary
`HOME`, so no real provider, Git, GitHub, or user-home credential was inherited.
Every mutation target belonged to the temporary fixture. This proves
that a per-provider command wrapper cannot contain `paseo.json` lifecycle code:
the code has already acquired engine/source/control authority before the
provider command starts. Refusing all four lifecycle surfaces avoids this
specific execution, but Paseo 0.7.2 has no public `kind: "worktree"` option that
makes that refusal an enforced per-Run property.

### Corrected Git result: private objects plus bundle/import pass

Reviewer comment `01a077b9-82e8-70fc-86c8-4a2bbe72637f` correctly identified
that the original fixture attempted only a direct shared-ref write and then
reported Candidate writes as denied without running Git. That inference is
removed.

The corrected focused fixture creates a detached linked worktree. A private
Linux user/mount namespace makes mount propagation private, remounts the shared
common directory read-only, and then exposes the owned per-worktree gitdir as a
nested writable bind. The focused fixture separately provides a private object
root to the agent-side Git process. Git reads the exact base
from the shared object store through `GIT_ALTERNATE_OBJECT_DIRECTORIES`, writes
all new objects through `GIT_OBJECT_DIRECTORY`, commits a real Candidate, and
anchors it in the public per-worktree `refs/worktree/*` namespace.

After that agent phase:

```text
candidateProducedWithCommonReadOnly=true
sharedHeadsUnchangedDuringAgentPhase=true
sharedCommonUnchangedOutsideOwnedGitdir=true
privateCandidateObjectPresent=true
sharedCandidateObjectAbsent=true
directSharedWriteDenied=true
```

After the agent phase, the engine creates and verifies a Git bundle in a
separate engine transfer directory from the private objects and per-worktree
Candidate ref. It fetches that bundle into a bare receiver seeded only with the
exact base and proves the imported commit SHA, parent, file content, and strict
object graph. The agent worktree is clean, and the focused run removes its
owned root with zero associated processes.

This is positive evidence for a bounded public Git submechanism. It disproves
the original claim that every linked-worktree mount mapping must choose between
Candidate production and shared-ref protection. It does not by itself approve
the complete production recovery/scale protocol or resolve any other authority
surface. In particular, it neither contains Paseo lifecycle commands nor
mediates provider credentials/egress nor applies an aggregate quota to writable
worktree bytes. Git is therefore removed from the No-go rationale rather than
presented as a new reason to keep M1 blocked.

### Authentication and network cannot both satisfy the threat model

`--network=none` is the observed OS boundary and necessarily blocks every
provider API. Giving a provider an ordinary rootless network restores arbitrary
egress; Podman's public run contract has no per-domain credential-aware egress
authorization. A Unix-socket HTTPS broker or host firewall can be built around
the container, but that becomes a new trusted broker/service and is outside the
approved no-sidecar monolith.

All three admitted CLIs authenticate by consuming provider-owned credentials.
The dummy nested mount proves any fully adversarial process in that boundary
can read the credential it must consume. Read-only mode, MCP policy, and domain
restriction do not prevent credential disclosure to an allowed provider
endpoint. The short-lived scoped token or authentication broker required by
ADR-0008 is not exposed by the tested public Paseo/provider contract.

No real provider credential was copied and no authenticated model turn was
made. That is the minimum safe run count after the common mechanism failed.
ADR-0005 remains the authority for authenticated MCP behavior in the
same-user topology; it explicitly does not prove host containment.

### Aggregate worktree disk remains unbounded

Cgroup memory, CPU, and process limits, a file-size limit, and bounded tmpfs
all worked. The Task worktree is a host bind mount, so those controls do not
cap aggregate bytes across many files. A filesystem project quota, bounded
volume with an export protocol, or privileged storage service would be a new
host prerequisite/architecture. The plan's 10% free-space stop is a necessary
detective control but is not OS enforcement against a fully adversarial writer.

## Provider matrix disposition

| ADR-0005 row | OCI executable | Authenticated MCP turn | Authority result |
|---|---|---|---|
| Codex CLI 0.147.0 | Exact version passed | Not run after common pre-provider failure | Not admitted for governed execution |
| Claude Code 2.1.258 | Exact version passed | Not run after common pre-provider failure | Not admitted for governed execution |
| OpenCode 1.18.18 | Exact version passed | Not run after common pre-provider failure | Not admitted for governed execution |

This does not revoke ADR-0005's session-MCP result. It means none of its three
rows may be admitted to a governed Director Run until the separate authority
gate is resolved. No provider, model, mode, permission, or delivery fallback is
selected.

## Cleanup evidence

The final report contained:

```json
{
  "daemonStopped": true,
  "ownedContainerCount": 0,
  "runtimeProcessCount": 0,
  "runtimeReset": "completed",
  "temporaryRuntimeRemoved": true
}
```

The focused Git-only report independently contained
`runtimeProcessCount: 0` and `temporaryRuntimeRemoved: true`.

An external post-run scan found no `director-m0.14-*` temporary root. The
one-off development probe store was separately verified container-free, reset,
and removed. No shared daemon, shared Beads/Dolt server, provider home, user
repository, remote, GitHub resource, branch outside a disposable repository,
or real credential was read or mutated.

## Fixture fingerprints

```text
028d3337c0bdfe5f7beaf4e06aa98f0d5d798457dc5892da6a8e0deb865f3cc5  container-probe.mjs
a7dd4ba98e3d27cd9070755ef3c22a865caf90dd68692e2163af068b2d025815  git-candidate-agent.mjs
500da5346deb41edffce9474b5d6a5d6ad8895f60e08d554035abea14e2bf785  git-candidate-namespace.mjs
eda9f49330082961f7983ab162fc8d95edd425b4ddfc97446c55b29a0acccd54  long-running-probe.mjs
3d5c6366e5a395eec2bfd1dbef85f581bd1cc1522de101d204a6ee3fc532bd72  lifecycle-escape.mjs
80a2afb92fe8432e8958195488af5fa8b9d5dc3ef1add8f6f027123fd259e395  reproduce.mjs
6a2baa520d96e57538f09ae0b441f3b19b9cfbac71c7221758e39def215bcf75  ../m0.3/probe-mcp.mjs
```

The exact Candidate and final hashes belong in the Beads handoff because adding
them here would change the Candidate.

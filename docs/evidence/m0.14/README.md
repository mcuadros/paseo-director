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
| [systemd execution environment](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html) | A distinct service identity is possible only through a separately administered service boundary; it is not a Paseo per-session launch primitive. |

Moving documentation is not the compatibility authority. The exact installed
versions, schemas, hashes, and live observations above bound the decision.

## Reproduction

Run from a clean checkout on the exact Linux tuple. The parent environment
must permit rootless user namespaces and cgroup v2. The procedure downloads
only the digest-pinned public OCI image, creates resources only below one
`mkdtemp` root, uses no provider/Git/GitHub/Paseo/Beads credential, and removes
that root after exact ownership and absence checks.

```sh
node docs/evidence/m0.14/reproduce.mjs
```

The single command runs, in order:

1. exact host and installed-artifact checks;
2. each admitted provider executable's `--version` within the same restricted
   rootless-OCI profile;
3. deterministic filesystem, credential, process, raw-control, network, and
   limit attacks;
4. a complete initialize/list/call/reject stdio MCP exchange through OCI;
5. linked-worktree common-directory mapping with absent, read-only, and
   writable mappings;
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

### A linked worktree cannot satisfy both Candidate production and separation

With only the linked worktree mounted, its `.git` indirection target was absent.
Mounting the shared Git common directory read-only made it readable but denied
the ref/object writes required to create a Candidate. Mounting it writable let
the adversarial process create a valid sibling ref, which the host Git process
immediately observed. The fixture then deleted only that exact owned ref.

Therefore the approved linked-worktree topology has no mount mapping that both
lets an adversarial Task Agent create a commit and denies mutation of shared
source/sibling Git state. A private clone/object broker or engine-owned commit
operation could change the tradeoff, but each changes an approved architecture
or workflow decision and needs a human-approved ADR plus new M0 evidence.

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

An external post-run scan found no `director-m0.14-*` temporary root. The
one-off development probe store was separately verified container-free, reset,
and removed. No shared daemon, shared Beads/Dolt server, provider home, user
repository, remote, GitHub resource, branch outside a disposable repository,
or real credential was read or mutated.

## Fixture fingerprints

```text
028d3337c0bdfe5f7beaf4e06aa98f0d5d798457dc5892da6a8e0deb865f3cc5  container-probe.mjs
eda9f49330082961f7983ab162fc8d95edd425b4ddfc97446c55b29a0acccd54  long-running-probe.mjs
3d5c6366e5a395eec2bfd1dbef85f581bd1cc1522de101d204a6ee3fc532bd72  lifecycle-escape.mjs
edb9bd8e64422f8245b7e0ec46655b5c361a4a8800c5930b5c137c9c7184c82d  reproduce.mjs
6a2baa520d96e57538f09ae0b441f3b19b9cfbac71c7221758e39def215bcf75  ../m0.3/probe-mcp.mjs
```

The exact Candidate and final hashes belong in the Beads handoff because adding
them here would change the Candidate.

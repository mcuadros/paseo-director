# M0.14 practical Linux agent-boundary evidence

- **Task:** `dir-m0.14`
- **Result:** Go for the human-approved trusted-engine/trusted-provider Linux
  1.0 boundary
- **Evidence date:** 2026-09-06
- **Scope:** Linux x86-64 only; no other host operating system is evaluated or
  implied
- **Topology:** one trusted Director engine/Paseo daemon, exact trusted provider
  CLI binaries, rootless OCI provider execution, fixed stdio MCP, disposable
  no-remote Git repositories, and deterministic policy contracts
- **Evidence class:** reviewed runtime observations, public Git execution, exact
  installed-artifact inspection, and focused policy assertions

## Human-approved scope

Director 1.0 trusts the exact reviewed Director engine and dependencies, the
Paseo daemon, and admitted installed provider CLI binaries. Model output,
repository content, lifecycle configuration, dependencies, and external tool
output remain untrusted inputs.

The Linux 1.0 claim expressly excludes compromise of the engine, Paseo daemon,
provider CLI, kernel, root account, or container runtime. Provider-native
authentication may be available to its trusted CLI and is not promised safe
from a compromised provider. This scope is an explicit human decision recorded
in Beads comment `01a077f1-3ed6-7740-9f9b-efbd49b91ced`.

Within that boundary Director still requires:

- rootless OCI for provider path, process, filesystem, and network defense in
  depth;
- no agent mount or channel to engine state, TaskStore/GitHub credentials,
  delivery authority, source/sibling paths, raw Paseo control, or runtime
  sockets;
- exact session-scoped stdio MCP and provider policy;
- refusal of non-empty automatic repository lifecycle configuration unless an
  exact human approval binds its scope and digest;
- engine-only Candidate import, delivery, integration, and cleanup; and
- operational disk/resource ceilings that fail closed to `Needs you` without
  authorizing cleanup.

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

The installed `provider-config.d.ts` SHA-256 is
`df160878fa43a0d3b397873e21053b1b8ac2dc3ccf48afc707203b46751b3a16`.
The exact installed `worktree-session.js` and `utils/worktree.js` hashes are
`9ba72cf9814e205285100843900fb0adfccbc46bebcff28b095817cb4839c771` and
`1cd0890689e666c062811e0cc3a76e35e4f653c75a0792640fd34bbb40121f30`.

## Primary public contracts

Consulted on 2026-09-06:

| Source | Contract used |
|---|---|
| [Paseo custom providers](https://paseo.sh/docs/custom-providers) | A provider command array can launch a wrapper or container while retaining the native adapter. |
| [Paseo provider options](https://paseo.sh/docs/sdk/provider-options) | Provider-native options constrain the trusted CLI but are not independently claimed as a hostile-process boundary. |
| [Paseo SDK reference](https://paseo.sh/docs/sdk/reference) | Public workspace/agent lifecycle, exact provider configuration, per-session MCP, observation, and archive. |
| [Paseo worktrees](https://paseo.sh/docs/worktrees) | Committed setup and teardown entries are shell commands, so Director must inspect lifecycle configuration before workspace creation. |
| [Paseo security](https://paseo.sh/docs/security) | Daemon/control reachability and host authentication assumptions. |
| [Podman rootless mode](https://docs.podman.io/en/stable/markdown/podman.1.html) | Rootless Podman uses a user namespace and user-owned container state. |
| [Podman run](https://docs.podman.io/en/latest/markdown/podman-run.1.html) | Mount, process, network, read-only-root, capability, cgroup, tmpfs, and limit controls used for defense in depth. |
| [Git repository layout](https://git-scm.com/docs/gitrepository-layout) | `HEAD` and `refs/worktree/*` are per-worktree while ordinary refs are shared. |
| [Git environment](https://git-scm.com/docs/git) | New objects can use `GIT_OBJECT_DIRECTORY`; immutable shared objects can be read through `GIT_ALTERNATE_OBJECT_DIRECTORIES`. |
| [Git bundle](https://git-scm.com/docs/git-bundle) | Public offline object/ref transfer, verification, listing, and fetch/import. |

Moving documentation is not the compatibility authority. The exact installed
versions, schemas, hashes, and observed behavior bound this result.

## Focused reproduction

The scope decision adds no product implementation. These two deterministic
commands are the required reproduction path for the changed Candidate:

```sh
node docs/evidence/m0.14/practical-boundary-contract.mjs
node docs/evidence/m0.14/reproduce.mjs --git-only
```

Both commands use only owned disposable state and leave zero temporary process
or path residue. They do not start Paseo, invoke a provider/model, use a
credential, or contact a remote.

The earlier full observation harness remains available when the unchanged
runtime evidence needs to be revalidated:

```sh
node docs/evidence/m0.14/reproduce.mjs --full
```

It is deliberately not required for this trust-scope-only correction. Its
runtime observations were reproduced by the prior exact-Candidate Opus 5 review.
Rootless Podman store reset can be load-sensitive; any cleanup uncertainty makes
the command exit non-zero, and the reproducer must reconcile exact labeled
containers, associated processes, and the nonce-owned temporary root before a
single retry. A failed cleanup is never evidence of success.

## Practical-boundary contract result

The focused contract returns `result: "go"` and asserts the following trusted
boundary:

```text
trustedComponents=director-engine,paseo-daemon,installed-provider-cli
rootlessOciMandatory=true
providerAuthenticationAvailableToTrustedCli=true
providerCredentialSafeFromCompromisedCli=false
```

It also fixes the authorities never exposed to governed agents:

```text
engine-state
taskstore
github-credentials
delivery-authority
source-checkout
sibling-workspaces
raw-paseo-control
container-runtime-control
```

These are launch-time mount, environment, executable, socket, MCP, and engine
authorization constraints. A model cannot expand them through prompt text or
repository content while the trusted engine/daemon/provider components behave
according to their admitted contracts.

### Automatic lifecycle refusal

The exact installed automatic lifecycle surfaces are:

```text
worktree.setup
worktree.teardown
worktree.terminals[*].command
worktree.servicePorts.portScript
```

The policy contract proves:

```text
empty_lifecycle_admitted_without_execution
setup_requires_exact_human_approval
teardown_requires_exact_human_approval
terminals_requires_exact_human_approval
portScript_requires_exact_human_approval
exact_human_scope_and_digest_admitted_without_automatic_execution
agent_supplied_non_human_stale_and_cross_scope_approvals_parked
configuration_change_invalidated_prior_approval
```

The admission evaluator executes zero commands. Without approval, every
non-empty surface returns `decision: "park"`, `route: "needs_you"`, and a
path/content-free reason. Approval is valid only from an authenticated engine
command with a server-derived human actor, the exact Project/Workspace/Task/Run
scope, and the canonical lifecycle digest; an agent-supplied actor is never
authority. A command change invalidates approval. An approved operation is
explicitly human-authorized and is not classified as automatic execution.

This gate runs before the public Paseo `kind: "worktree"` call. That order is
mandatory because the observed 0.7.2 implementation runs setup/teardown with
the daemon identity before a provider wrapper.

### Operational limit parking

The contract uses small fixture values to test inclusive boundaries rather
than establish product defaults:

| Observation | Fixture limit |
|---|---:|
| Free disk | minimum 10% |
| Aggregate observed worktree bytes | maximum 1,048,576 |
| Processes | maximum 32 |
| Memory | maximum 268,435,456 bytes |
| Elapsed time | maximum 60,000 ms |

Exact boundaries continue. One unit beyond every maximum, a disk percentage
below the floor, and a missing value for every field each return `park` and
`needs_you`, with launch/continuation false and cleanup authorization false.
Every result is marked `enforcement: "operational"` and
`hostileProviderGuarantee: false`.

Product policies may tighten these values. They must always be finite, observed
at preflight and periodically while active, and must retain the PLAN's 10% disk
floor. Operational parking protects availability and recoverability under the
trusted runtime boundary; it is not an OS quota guarantee against a compromised
provider.

## Rootless OCI observations retained as defense in depth

The existing bounded runtime evidence established:

| Surface | Observed result |
|---|---|
| Provider executable | Exact Codex, Claude Code, and OpenCode versions executed in the restricted profile. |
| Writable scope | The owned worktree accepted a nonce write; source, sibling, engine, and host-home attempts returned `ENOENT`. |
| Engine credential | The unmounted dummy engine credential returned `ENOENT`. |
| Provider credential | A deliberately mounted dummy provider-auth file was readable, matching the trusted-CLI scope decision. |
| Raw controls | `paseo`, `gh`, `docker`, and `podman` executables plus Docker/Podman sockets were absent; host daemon loopback returned `ECONNREFUSED`. |
| Network | Direct public IPv4 returned `ENETUNREACH` under `--network=none`. A governed provider profile must explicitly select its required network availability. |
| Stdio MCP | Initialize succeeded, only `read_scope_nonce` was advertised, one exact nonce call succeeded, an out-of-scope call was rejected, and stdin closure was observed. |
| Process/resource defense | UID/GID mapping, `NoNewPrivs`, zero effective capabilities, cgroup memory/PID/CPU limits, RLIMIT file size, and bounded tmpfs were observed. |
| Recovery/cleanup | An exact labeled detached container was rediscovered, terminated, removed idempotently, and left zero owned processes/containers. |

Rootless OCI remains mandatory. These controls contain accidental behavior and
untrusted work routed through the trusted CLI; they do not claim resistance to
the explicitly excluded compromise of that CLI, the engine, kernel, root, or
container runtime.

## Corrected Git Candidate transfer retained

The public Git fixture creates a detached linked worktree, makes mount
propagation private, remounts the shared common directory read-only, exposes the
owned per-worktree gitdir as a nested writable bind, and provides private object
storage. Git 2.47.3 reads the exact base through
`GIT_ALTERNATE_OBJECT_DIRECTORIES`, writes every new object through
`GIT_OBJECT_DIRECTORY`, creates a real commit, and anchors it under
`refs/worktree/*`.

The agent phase proves:

```text
candidateProducedWithCommonReadOnly=true
candidateParentMatched=true
perWorktreeRefMatched=true
privateCandidateObjectPresent=true
sharedCandidateObjectAbsent=true
sharedHeadsUnchangedDuringAgentPhase=true
sharedCommonUnchangedOutsideOwnedGitdir=true
directSharedWriteDenied=true
agentWorktreeClean=true
```

The engine then creates the bundle in a separate transfer directory, verifies
it, imports it into a bare receiver seeded only with the exact base, and proves
the exact SHA, parent, content, and object graph.

The focused mount fixture is self-administered by its mapped-root process. It
proves the Git mechanics under a read-only mapping, not adversary-resistant
mount enforcement. Governed Runs obtain that enforcement from the separately
required rootless OCI profile with dropped capabilities, `NoNewPrivileges`, and
no runtime socket. This qualification preserves the prior Opus review's P3
observation without weakening the positive Git result.

## Provider disposition

| ADR-0005 row | Linux 1.0 disposition |
|---|---|
| Codex CLI 0.147.0 | Admitted only at the exact proven tuple, trusted CLI, mandatory rootless OCI, fixed MCP/policy, and preflight gates above. |
| Claude Code 2.1.258 | Admitted only at the exact proven tuple, trusted CLI, mandatory rootless OCI, fixed MCP/policy, and preflight gates above. |
| OpenCode 1.18.18 | Admitted only at the exact proven tuple, trusted CLI, mandatory rootless OCI, fixed MCP/policy, and preflight gates above. |

Generic ACP remains excluded by ADR-0005 because exact tool preapproval is
absent. No model, provider, mode, permission, delivery, or trust fallback is
implicit.

## Cleanup evidence

The focused practical-boundary contract creates no external resource. The
focused Git fixture reports:

```json
{
  "runtimeProcessCount": 0,
  "temporaryRuntimeRemoved": true
}
```

The retained full runtime evidence reports zero owned containers/processes and
removes its nonce-owned root on success. Failed development and review attempts
were reconciled against exact markers/labels; every owned temporary root was
removed. No shared daemon, Beads/Dolt server, real provider home, user
repository, public remote, GitHub resource, unrelated branch, real credential,
or persistent service was mutated by the evidence procedures.

## Fixture fingerprints

```text
028d3337c0bdfe5f7beaf4e06aa98f0d5d798457dc5892da6a8e0deb865f3cc5  container-probe.mjs
a7dd4ba98e3d27cd9070755ef3c22a865caf90dd68692e2163af068b2d025815  git-candidate-agent.mjs
500da5346deb41edffce9474b5d6a5d6ad8895f60e08d554035abea14e2bf785  git-candidate-namespace.mjs
3d5c6366e5a395eec2bfd1dbef85f581bd1cc1522de101d204a6ee3fc532bd72  lifecycle-escape.mjs
eda9f49330082961f7983ab162fc8d95edd425b4ddfc97446c55b29a0acccd54  long-running-probe.mjs
2a61be6e2f3a5d5ec48d89ddcc7fff2bf664bef003ad5561a91d126b619e3071  practical-boundary-contract.mjs
c143f993dc6332cf25662bd16b67deb8a20f22d45738887167a0b1ca17d55f0c  reproduce.mjs
6a2baa520d96e57538f09ae0b441f3b19b9cfbac71c7221758e39def215bcf75  ../m0.3/probe-mcp.mjs
```

The exact changed Candidate and final validation are recorded in Beads because
embedding that SHA here would change the Candidate.

# dir-m0.17 scalable ignored-tree recovery evidence

- Evidence date: 2026-09-06
- Supported topology: Linux
- Provisional contract base:
  `af489d04d4f84bd60d39e575718884e0b645ee36` (`dir-m0.6` Candidate, binding
  until that Task closes)
- Local topology: Debian 13.6, Linux `6.12.107+deb13-amd64`, x86-64
- Local tools: Node.js `v26.7.0`; Git `2.47.3`
- Harness compatibility floor: Node.js 22 or newer
- Outcome: Go on the supported Linux topology

## Result and boundary

The Linux harness measures and fault-tests the private ignored-material
artifact selected by provisional ADR-0007. It starts after the unified
`dir-m0.6` contract has proved an integrated, prospective-tree-clean owned
worktree and discovered normalized ignored/Git-invisible roots. It does not
replace that contract's Git, worktree, base, ref, lifecycle, special-file, or
pre-destructive ownership gates.

The `af489d04...` base remains provisional until `dir-m0.6` closes. If its
Candidate changes before closure, this Task stops, rebases, and reruns the
complete contract. This evidence does not modify or publish `dir-m0.6`.

All source, artifact, state, restore, oversize, squatter, report, and timeout
fixtures live beneath `mkdtemp` roots named `director-m0.17-*`. The harness uses
only those exact roots and removes them in `finally`. It creates no network
remote, credential, hook, service, repository branch, or long-lived process.

Artifacts:

- [`tools/spikes/dir-m0.17/ignored-tree-policy.mjs`](../../../tools/spikes/dir-m0.17/ignored-tree-policy.mjs)
- [`docs/adr/0013-scalable-ignored-tree-recovery-policy.md`](../../adr/0013-scalable-ignored-tree-recovery-policy.md)

Final tracked executable hash:

```text
6a1297d18f901fa1ec7f0ed342c0adc8394dccb80cfd4e61558a325be386e0e4  tools/spikes/dir-m0.17/ignored-tree-policy.mjs
```

Local reproduction:

```text
node --check tools/spikes/dir-m0.17/ignored-tree-policy.mjs
node tools/spikes/dir-m0.17/ignored-tree-policy.mjs
```

The default command first asserts the supported Linux host, then runs one
scaled timeout fixture and starts one allowlisted-environment worker with
`--expose-gc` under a 540-second hard deadline. The worker writes only aggregate
JSON into an owner-only parent-created `director-m0.17-report-*` root because
the local sandbox can drop captured stdout despite a successful child status.
The parent verifies the report leaf and structure and removes that complete
protocol root in `finally` before returning the same JSON on stdout.
`--benchmark-worker` is an internal evidence mode used to isolate RSS.
`--timeout-probe` reruns only the scaled supervisor fixture.

## Release policy

| Resource | Default and maximum without new Linux evidence |
|---|---:|
| Recovery entries, including files and directories | 10,000 |
| Inspected worktree entries | 25,000 |
| Aggregate ignored content | 536,870,912 bytes |
| One regular file | 268,435,456 bytes |
| Sequential stream buffer | 65,536 bytes |
| One phase | 180,000 ms |
| Full lifecycle | 480,000 ms |
| External worker | 540,000 ms |
| Worker RSS growth | 201,326,592 bytes |
| Retention | 604,800,000 ms |
| Projected free-space floor | 10% |
| Fixed disk reserve | 65,536 bytes |
| Per-entry disk reserve | 4,096 bytes |
| Unified gate byte revalidations | Five |

Policy inheritance can only tighten maxima, shorten retention, or raise the
disk floor. Expansion is rejected until a later ADR supplies representative
Linux evidence. The disk calculation is:

```text
required = remaining content bytes + 65,536 + entry count * 4,096
admit only when available bytes - required >= filesystem bytes * 10 / 100
```

Creation and every retry recompute the artifact-filesystem facts, and restore
checks the destination filesystem before writing. A verified existing artifact
charges zero remaining content bytes; an absent or partial artifact and a
restore reserve the full amount. A refusal preserves the worktree and every
unknown artifact and routes one path/content-free Needs-you reason.

## Representative composite fixture

The single full lifecycle minimizes disk/time while covering all three target
shapes at exact defaults:

| Shape | Layout | Entries |
|---|---|---:|
| `node_modules` | 550 package directories, ten small files each | 6,051 |
| `.venv` | 300 module directories, ten small files each | 3,302 |
| `target` | 107 unit directories, object files, two large binaries, one empty directory | 647 |
| **Total** | 9,037 regular files and 963 directories | **10,000** |

The two target binaries make aggregate content exactly 512 MiB; the larger is
exactly 256 MiB. Small files contain deterministic nonzero JavaScript, Python,
object-fixture, and disposable secret-marker bytes. Git `check-ignore
--no-index` proves samples under all three roots match the fixture `.gitignore`.

After the exact-default enumeration, one extra real directory proves the
10,001-entry refusal and is removed before restoring the parent mtime. Sparse
metadata-only fixtures prove 512 MiB plus one aggregate byte and 256 MiB plus
one per-file byte are rejected before hashing/copying. Configuration tests
reject one extra inspected entry, buffer byte, retention millisecond, or any
other maximum expansion and reject a 9% floor.

## Unified artifact lifecycle

The evidence reuses the ADR-0007 lifecycle semantics:

1. Persist an atomic owner-scoped intent before artifact effects. Public state
   has an opaque artifact ID, Task/Run/nonce/Candidate, phase, policy, aggregate
   counts/digests, identity tuple, and expiry—no absolute entry/artifact path,
   entry name, individual hash, or content.
2. Reprove the state root and recovery root canonical/filesystem identities.
   Enforce mode `0700` on directories and `0600` on private files.
3. Enumerate deterministically without following links. Enforce inspected,
   recovery-entry, per-file, aggregate-byte, disk, phase, lifecycle, and RSS
   limits. Unsupported material returns Needs you.
4. Stream source to a private staging payload with one 64 KiB buffer, hashing
   while copying. Re-enumerate metadata and stream source hashes again before
   writing the private manifest.
5. Atomically rename staging to its opaque final leaf. Reverify owner,
   permissions, identity, private manifest, metadata, and every payload byte.
6. Inject a crash before result persistence. Retry derives the final path from
   the owned root plus opaque ID, revalidates the complete artifact and source,
   adopts the same filesystem identity, and creates no duplicate.
7. Run five complete source-plus-artifact gates, matching ADR-0007's move,
   remove, local-ref, remote-ref, and retained-artifact destructive boundaries.
8. Restore to a disposable destination and verify aggregate metadata and every
   content hash. File/directory modes, millisecond mtimes, and the empty
   directory are included.
9. Keep the artifact scheduled before seven days. At expiry, repeat exact
   ownership/content proof, persist removal-ready, remove, inject a crash, and
   adopt verified absence idempotently.

Creation and retention squatters replace the expected artifact with an unknown
owner/sentinel. Both are refused and preserved. Test-only cleanup moves the
real owned artifact aside, verifies the unknown sentinel, removes only the
fixture-owned squatter, and restores the same owned inode before normal expiry.
This does not claim protection from the hostile same-user race in ADR-0008.

The private manifest contains recovery entry paths and individual SHA-256
hashes because exact restoration requires them. It remains below the owner-only
artifact. A known disposable entry name, its content marker, and temporary
absolute roots are scanned out of public state and the final report.

## Aggregate result

One successful measured worker returned:

```json
{
  "host": {
    "os": "linux",
    "release": "6.12.107+deb13-amd64",
    "arch": "x64",
    "node": "v26.7.0",
    "git": "2.47.3"
  },
  "fixture": {
    "entryCount": 10000,
    "fileCount": 9037,
    "directoryCount": 963,
    "totalBytes": 536870912,
    "largestFileBytes": 268435456,
    "gatePasses": 5
  },
  "measurements": {
    "fixtureSetupWallMs": 2754.658,
    "artifactCreateAndRetryWallMs": 128922.212,
    "lifecycleWallMs": 255711.271,
    "baselineRssBytes": 65540096,
    "peakRssBytes": 174936064,
    "rssIncreaseBytes": 109395968,
    "gateWallMsRange": [1539.822, 1572.213],
    "restoreWallMs": 116612.368
  }
}
```

All figures are observations on the disposable Linux host and run, not latency
promises for every filesystem. The enforced caps are deliberately above the
observation. Content work remains sequential; no reported memory bound is
inferred from nominal buffer size alone.

The run asserted exact/beyond policy behavior, disk-floor boundaries,
owner-only permissions, crash adoption, five full byte gates, RSS/time limits,
exact restoration, creation and retention squatter refusal, scheduled
retention, interrupted expiry reconciliation, idempotency, secrecy, and root
cleanup. The supervisor fixture separately killed an intentionally stuck
worker after a scaled three-second deadline, observed byte-identical state and
source, and left no worker/root. It passed twice during author validation.

## Compatibility and exclusions

- Evidence applies to the recorded Linux kernel, distribution, filesystem,
  Node.js, Git, and architecture tuple. Director preflight must prove required
  Linux capabilities rather than infer them from an OS name alone.
- The harness uses Node.js 22-or-newer public `fs`/`statfs`/process APIs, direct
  argv execution, POSIX modes, native real paths, and filesystem device/inode
  identity.
- The artifact supports regular files, directories, and empty directories.
  Symlinks, nested repositories, FIFOs, sockets, devices, xattrs, source
  ACL/owner restoration, sparse-layout fidelity, network filesystems, and
  unstable file identities remain unadmitted and stop before cleanup.
- A process may block inside one filesystem syscall until the external worker
  deadline. The worker deadline is authoritative over the softer in-loop
  phase/lifecycle checks. Termination never becomes deletion evidence.
- ADR-0008/`dir-m0.14` remains the containment gate for hostile same-user
  processes. This result proves only the cleanup policy and recovery logic.

## Primary contracts

Consulted on 2026-09-06:

- <https://nodejs.org/docs/latest-v22.x/api/fs.html> — Node.js 22.23.2
  filesystem, directory iteration, streaming, `statfs`, modes, and `fsync`
  contracts.
- <https://git-scm.com/docs/gitignore> — current Git ignore precedence and
  intentionally-untracked semantics.

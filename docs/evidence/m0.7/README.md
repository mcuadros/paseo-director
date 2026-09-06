# dir-m0.7 exact-SHA GitHub delivery evidence

- Evidence date: 2026-09-06
- Outcome: Go on the recorded Linux and GitHub topology
- Task: `dir-m0.7`
- Sandbox: private `mcuadros/paseo-director-github-delivery-sandbox`
- GitHub repository database ID: `1359322331`
- GitHub repository node ID: `R_kgDOUQWc2w`
- Stable sandbox `main`: `22239f3b3448fad2e928fd3421c7adfd58f59b6f`
- Successful logical run: `run-20260906-b`
- Safe-failure logical run: `run-20260906-a`
- Local topology: Debian 13, Linux `6.12.107+deb13-amd64`, x86-64
- Local tools: Node.js `v26.7.0`, Git `2.47.3`, GitHub CLI `2.97.0`
- REST API version: `2022-11-28`
- Harness compatibility floor: Node.js 22 or newer

## Result and boundary

The live private-sandbox experiment proved a supported exact-SHA mechanism
for an owned branch push, pull-request lookup/create, Candidate check
observation, SHA-bound human review feedback, base movement, atomic
expected-head merge, retry reconciliation, merge verification, and exact
remote-ref cleanup.

This is an M0 contract harness, not a Director product adapter. It does not
resolve the general durable effect-state-machine work in `dir-m0.10`, the
worktree/recovery contract in `dir-m0.6`, or the authority-separation stop in
`dir-m0.14`. It creates no Director Task Candidate in the sandbox and never
uses the public `mcuadros/paseo-director` repository as an experiment target.

The human project owner authorized creating repository ID `1359322331`, using
it destructively only for `dir-m0.7`, retaining it through independent review,
and deleting it only after the Task is approved and delivered. The harness
hard-codes both its canonical full name and database ID and separately rejects
the public plugin repository.

Artifacts:

- [`tools/spikes/dir-m0.7/github-delivery.mjs`](../../../tools/spikes/dir-m0.7/github-delivery.mjs)
- [`docs/adr/0006-exact-sha-github-delivery.md`](../../adr/0006-exact-sha-github-delivery.md)
- Sandbox [PR #1](https://github.com/mcuadros/paseo-director-github-delivery-sandbox/pull/1),
  the closed safe-failure record
- Sandbox [PR #2](https://github.com/mcuadros/paseo-director-github-delivery-sandbox/pull/2),
  the successfully merged record

Final tracked harness hash:

```text
d2a249eda695b00c28282315d990f9a697436b430b1704132f9410104e717c5f  tools/spikes/dir-m0.7/github-delivery.mjs
```

## Primary contracts

Consulted on 2026-09-06:

- <https://git-scm.com/docs/git-push/2.47.3> — Git `2.47.3` documents that
  `--force-with-lease=<ref>:<expect>` fails unless the remote ref has the
  explicit expected value and that an empty expected value requires absence.
- <https://cli.github.com/manual/gh_pr_merge> — installed GitHub CLI `2.97.0`
  exposes `--match-head-commit SHA`; tag `v2.97.0` resolves to source commit
  `55dbb4dc6b7edb10b48e3d7fc5bccd32318d1b55`.
- <https://cli.github.com/manual/gh_pr_create> — explicit `--repo`, `--base`,
  and `--head` avoid contextual repository/branch selection.
- <https://docs.github.com/en/rest/pulls/pulls?apiVersion=2022-11-28> — the
  versioned pull-request list/read contract and exact head/base repository/ref
  facts.
- <https://docs.github.com/en/rest/checks/runs?apiVersion=2022-11-28#get-check-runs-for-a-git-reference>
  — check-run lookup for one exact Git reference.
- <https://docs.github.com/en/rest/pulls/reviews?apiVersion=2022-11-28> —
  pull-request review list/create and the review `commit_id` binding.

The current official `github/docs` source revision inspected for the REST
pages was `ec3629a841129ae28189d7bb2274a7b3d40c5095`, committed
2026-09-04. Installed CLI help and runtime behavior, rather than an online
version guess, define the tested CLI surface.

## Reproduction

The safety-only checks do not use GitHub:

```text
node --check tools/spikes/dir-m0.7/github-delivery.mjs
node tools/spikes/dir-m0.7/github-delivery.mjs --self-test
```

The live command requires the authenticated owner, the retained exact private
sandbox, `repo` plus `workflow` token scopes, and a new human-authorized unique
logical run ID:

```text
DIRECTOR_SPIKE_RUN_ID=<authorized-unique-run> \
  node tools/spikes/dir-m0.7/github-delivery.mjs
```

Do not reuse `run-20260906-a` or `run-20260906-b`. A new run deliberately
creates a new historical PR/review/check evidence set and therefore requires a
new unique idempotency scope and authorization.

The harness uses direct argv execution with prompts/pagers disabled. It creates
one owner-only `mkdtemp` root, configures a credential-free HTTPS remote URL,
uses the host GitHub CLI credential helper, never prints a token, and removes
the exact local root in `finally`. It starts no service or persistent process.

## Exercised protocol

1. Re-read the repository by canonical name, database ID, privacy, enabled
   state, permissions, and merge-branch-deletion setting. Reject every other
   repository, including the public plugin repository.
2. Observe stable `main`; initialize it only when the sandbox is empty, using
   an absent-ref lease. On every later run, fetch and verify its exact one-file
   bootstrap tree without changing it.
3. Create unique owned base/head refs. Before each push, use `ls-remote
   --exit-code` to distinguish exact presence, status-2 absence, and
   transport/authentication failure. Dispatch only when the live ref equals
   the explicit expected OID. Inject an effect-before-result interruption and
   prove retry adopts the desired live OID without a second push.
4. Lookup the PR before creation by canonical base/head repository IDs, exact
   refs, owner, and durable run marker. Inject result loss after creation and
   prove retry finds exactly one PR.
5. Run a no-dependency GitHub Actions job on each pushed Candidate. Lookup
   checks by Candidate SHA and require exactly one completed successful
   `github-actions` run whose `head_sha` equals that Candidate.
6. Seed one externally modeled human `COMMENTED` review with an exact
   `commit_id` and durable marker. Inject result loss and prove retry returns
   the same review ID without creating another review or issue comment.
7. Fast-forward the live owned base from Base 1 to Base 2. Compare the durable
   Base 1 against a fresh remote-ref observation; invalidate readiness even
   though the PR `base.sha` projection remains Base 1 at that point.
8. Merge Base 2 into the head, creating Candidate 2. Push only with an exact
   Candidate-1 lease. Require Base 2 ancestry, a successful exact-Candidate-2
   check, and observe that the earlier human review remains bound to Candidate
   1 rather than becoming evidence for Candidate 2.
9. Invoke `gh pr merge --match-head-commit <Candidate-1>` and require nonzero
   rejection while the live PR head is Candidate 2. Then invoke the same public
   mechanism with Candidate 2, inject result loss, and prove retry observes the
   already merged PR instead of dispatching another merge.
10. Require the merged PR flag/timestamp, exact stored head SHA, merge commit,
    live base ref, Candidate ancestry, and exact two merge parents. A non-null
    `merge_commit_sha` alone is not accepted: closed unmerged PR #1 has a
    GitHub-generated test merge SHA despite never being integrated.
11. Move a separate owned guard ref after recording its expected SHA. Prove a
    stale compare-delete refuses before dispatch and preserves the changed ref.
    The fixture then records the new owned OID and removes it exactly.
12. Compare-delete guard, head, and base refs with explicit
    `--force-with-lease=<ref>:<expect>`. Inject result loss for every deletion;
    retry accepts only status-2 authoritative absence and never sends a second
    delete. Verify no run ref remains and stable `main` is unchanged.

## Safe-failure run

`run-20260906-a` intentionally remains as evidence that a plausible but wrong
base observation failed closed:

| Fact | Value |
|---|---|
| PR | `#1`, closed, unmerged |
| Candidate | `23aeb678d3012dca2e2b4bdf71af081936b2661b` |
| Candidate check | `34046058824`, completed `success` at the exact Candidate |
| Human review | `5125958303`, `COMMENTED`, exact Candidate `commit_id` |
| Merge | none (`merged_at` is null) |
| Cleanup | all run refs absent; local root absent; stable `main` preserved |

The original harness waited for the PR REST `base.sha` to change after the
base ref moved. It did not change within the bounded interval, so the harness
closed the PR and compare-deleted the exact refs. This is not a GitHub outage:
it is evidence that the PR projection is not a safe live-base authority.

## Successful run exact facts

| Fact | Exact value |
|---|---|
| Stable main | `22239f3b3448fad2e928fd3421c7adfd58f59b6f` |
| Base 1 | `4aa02c6282987e798bce6a9c944ebaae1e7bd971` |
| Candidate 1 | `a30da7c1f8b90021c0302265151aa53f7af634c2` |
| Base 2 | `134bc4b30ac651114fffd9393bae0299627d6b18` |
| PR base projection immediately after movement | `4aa02c6282987e798bce6a9c944ebaae1e7bd971` |
| Candidate 2 | `2411bb4e6f83ac32f8db8da62222f67e66689439` |
| Merge commit | `34786ee2643eca9419f2b5d1b8e262d53ea9bbd8` |
| Merge parents | Base 2, Candidate 2 |
| PR | `#2`, merged at `2026-09-06T16:42:56Z` |
| Human review | `5125969416`, Candidate-1 `commit_id` |
| Candidate-1 check | `101521834928`, `success`, exact Candidate-1 head |
| Candidate-2 check | `101521881953`, `success`, exact Candidate-2 head |

Aggregate harness output:

```json
{
  "outcome": "go",
  "task": "dir-m0.7",
  "topology": {
    "repository": "mcuadros/paseo-director-github-delivery-sandbox",
    "repositoryId": 1359322331,
    "runId": "run-20260906-b"
  },
  "effects": {
    "pushDispatches": 6,
    "pullRequestCreateDispatches": 1,
    "feedbackCreateDispatches": 1,
    "staleMergeDispatches": 1,
    "mergeDispatches": 1,
    "branchDeleteDispatches": 3
  },
  "cleanup": {
    "runOwnedRemoteRefsAbsent": true,
    "openPullRequestStateAbsent": true,
    "stableMainPreserved": true,
    "localRootRemoved": true,
    "credentialBearingArtifactCreated": false,
    "persistentProcessCreated": false,
    "sandboxRepositoryRetainedForIndependentReview": true
  }
}
```

The final independent read showed only `refs/heads/main` at the stable SHA,
zero open PRs, PR #1 closed/unmerged, PR #2 closed/merged, one review and zero
issue comments on PR #2, the two exact successful check runs, and merge parents
`[Base 2, Candidate 2]`. No `director-m0.7-*` root remained under `/tmp`.

## Decision constraints

- GitHub PR `base.sha` is contextual/cached data, not proof of the current base
  branch tip. Director must freshly observe/fetch the live base ref and compare
  it with the Candidate's recorded base before readiness, merge, and cleanup.
- PR `head.sha`, check-run `head_sha`, and review `commit_id` are separate
  bindings and must all match the intended Candidate where applicable.
- A Candidate change makes Candidate-1 checks and feedback historical. This
  harness does not reinterpret them as Candidate-2 readiness evidence.
- `merge_commit_sha` is not sufficient merge proof. Require merged state and
  timestamp plus exact head/base/graph verification.
- Same-intent retries always observe first. Exact desired state is adopted;
  expected absence is accepted only from Git status 2; a changed ref or
  external ambiguity stops without mutation.
- Every ref update/deletion uses the explicit expected-value form of
  `--force-with-lease`; the tracking-ref shorthand is not part of the contract.
- This evidence proves one authenticated-owner private-repository topology.
  Fork PRs, merge queues, branch protection, required human approval,
  squash/rebase merge, GitHub Enterprise, SHA-256 Git repositories, outage
  recovery, and token rotation require later adapter/release coverage.
- Repository rename/replacement was not fault-injected between initial
  database-ID verification and every Git transport. The harness explicitly
  selects the hard-coded canonical repository/ref for every operation;
  `dir-m0.10` still owns just-in-time repository-identity revalidation and
  effect-boundary recovery.

## Independent verification

Pending. Review must use a detached checkout of the exact Task Candidate SHA,
rerun the local safety checks, reconcile the retained private GitHub evidence,
and verify that the Go decision does not absorb `dir-m0.6`, `dir-m0.10`, or
`dir-m0.14`. Publication, Task integration, closure, sandbox deletion, and
workspace removal remain post-review gates.

# Controlled helper lifecycle

A helper is optional internal decomposition by the active Task Agent. It is
never a Task, Run, Candidate producer of record, root-workspace Worker, or
independent Reviewer. The Go Director Engine owns admission, accounting,
observation, handoff verification, and cleanup; the Task Agent alone invokes
the admitted parent-bound helper mechanism. The Paseo connector only observes
and archives an exact native identity through the fixed host contract.

## Durable sequence

```text
Worker MCP request
→ Run-scoped request and capacity reservation
→ fresh global capacity and operational observations
→ writer checkout or proved read-only share
→ mandatory rootless-OCI boundary observation
→ one-use parent-bound admission
→ lease-fenced helper-turn budget reservation
→ Task-Agent bootstrap invocation consumed before possible handoff
→ exact native parent/Run/workspace/label/timeline observation
→ optional writer contribution observation
→ exact commit-object import and parent handoff
→ exact helper archive
→ writer-checkout removal after termination and handoff proof
```

Request replay with the same immutable payload returns the original durable
result. A changed payload conflicts. A consumed admission is never reissued.
If native creation or commit import may have happened but cannot be proved,
the Run parks without retry or cleanup authority. Later observation may adopt
the exact parent-bound helper and continue containment.

Soft or hard runtime-budget exhaustion leaves the one-use admission unconsumed
and prevents native helper dispatch. A dispatched helper retains its exact
reservation until one current provider observation accounts input plus output
tokens, one turn, optional reported cost, and Run wall time. Missing or
ambiguous usage parks without releasing the reservation or losing the helper;
evidence-only reconciliation remains allowed while model dispatch stays
blocked. Cleanup requires that exact released reservation.

## Isolation and authority

Writer helpers receive an engine-derived checkout with their own refs and
object store. The checkout has no origin back to the primary repository.
Commit handoff imports only the verified exact commit object; it does not move
the primary `HEAD`, index, or worktree. A read-only helper may share the
primary checkout only when its current boundary observation proves a read-only
mount.

Every helper uses the exact admitted Worker provider tuple and a smaller MCP
role: bounded Task/Run read plus writer contribution submission. It cannot
submit the Task outcome or verdict, choose another Run/workspace/repository,
or perform delivery or lifecycle effects.

The admission carries only the fixed zero-work helper bootstrap, a unique
message ID, the exact provider tuple, a mode-specific permission setting, and
the strict helper MCP descriptor. Director records the native ID only after
the connector proves that bootstrap in the helper timeline. Real helper work
cannot precede that observed identity.

Admission and active/cleanup reconciliation repeat ADR-0014 requirements:
trusted exact provider tuple, rootless OCI, read-only root, dropped
capabilities, `NoNewPrivileges`, private network, no runtime socket, no raw
control, no TaskStore/GitHub/delivery credential, exact lifecycle digest, and
finite disk/worktree/process/memory/time/output/temp telemetry. A missing,
stale, exceeded, or changed fact parks and never authorizes deletion.

The effective Task quota and Project-wide agent capacity are frozen into the
Run. Requests are voluntary, each live or ambiguous helper retains its slot,
and TaskStore compare-and-swap makes concurrent reservation single-winner.

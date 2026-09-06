# ADR-0016: Defer TaskStore scale proof to M5

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.20`
- **Plan gate:** M0 TaskStore evidence and M5 scale validation
- **Decision owner:** Human project owner
- **Amends:** PLAN §§8.1, 21.4, and 23

## Context

PLAN §§8.1 and 23 originally included the agreed TaskStore scale in the
M0 proof obligation. ADR-0004 approved the direct-Dolt functional contract,
and ADR-0012 approved its operational and recovery contract on a bounded
Linux topology. Neither decision measured the PLAN §21.4 scale target.

The M0 exit audit identified that mismatch as a P2 decision requiring the
human project owner. The owner explicitly chose to defer scale proof to the
existing `dir-m5.10` Task in Beads comments
`01a078b6-9382-72ab-a645-52a479ec7d7a` and
`01a078b6-a6ea-7638-b7c4-3b6ba40d9abc`.

## Question or hypothesis

Can TaskStore scale validation move from M0 to `dir-m5.10` without extending
the M0 evidence, making a production-scale claim, or weakening a product
invariant?

## Acceptance criteria

- M0 retains the approved direct-Dolt functional, concurrency, operational,
  recovery, and Linux-topology evidence without claiming unmeasured scale.
- M1 uses only those exact approved contracts and evidence bounds.
- No correctness, security, recovery, or exact-SHA invariant changes.
- PLAN §21.4 remains the required scale target.
- `dir-m5.10` remains the explicit mandatory owner of scale validation before
  the M5 exit and stable release gates.

## Evidence

- ADR-0004 Candidate `15914ef50b5a7b280153d3ba04296881830dd3c3`
  was independently approved and integrated as
  `ae06c376002b079f55f9fdbd92b9b0e70466f422`.
- ADR-0012 Candidate `d55f7dbccad902bab15261ef98d22f8dcea2e2e3`
  was independently approved and integrated as
  `212dcbab176ac0ebbb84eec37610fdf23d9efe45`.
- `dir-m5.10` requires validation at 25 Workspaces, 10,000 historical Tasks,
  500 open Tasks, and eight concurrent agents without unbounded resources.
- The two cited Beads comments record the same explicit human decision.

No technical experiment was rerun for this governance-only decision.

## Alternatives considered

### Run an M0 scale experiment

Not selected. The human owner explicitly assigned that proof to `dir-m5.10`.

### Drop the scale target

Rejected. PLAN §21.4, the M5 exit gate, the stable-release gate, and
`dir-m5.10` continue to require it.

### Infer scale from the existing M0 evidence

Rejected. The approved direct-Dolt evidence is bounded to its recorded
topology and creates no production-scale support claim.

## Decision

**Go.** Defer only the timing of TaskStore agreed-scale proof from M0 to
`dir-m5.10` in M5.

M1 may implement only the exact direct-Dolt contract and bounded Linux
behavior approved by ADR-0004 and ADR-0012. It must not represent that evidence
as production-scale validation. All correctness, security, recovery, and
exact-SHA invariants remain mandatory.

`dir-m5.10` is the mandatory owner of the PLAN §21.4 scale proof. That proof
remains required for the M5 exit gate and stable `1.0.0` release.

## Consequences

- TaskStore scale is no longer an M0 stop condition.
- The M1 TaskStore gate does not require a scale result, but M1 gains no scale
  claim or relaxed invariant.
- Failure to meet the target blocks the M5 exit and stable release gates; it
  is not retroactively accepted by this deferral.
- The PLAN §21.4 target and `dir-m5.10` acceptance criteria remain unchanged.

## Independent verification

The human authority is durably recorded in the two cited Beads comments. This
ADR's Accepted status becomes authoritative only when its exact Candidate is
independently approved and integrated with its reviewed base. The owning
`dir-m0.20` record must carry that Candidate, base, verdict, integration
commit, checks, and residual risks. This ADR introduces no new technical
evidence and authorizes no experiment rerun.

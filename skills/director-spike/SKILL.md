---
name: director-spike
description: Run a time-bounded, evidence-driven technical spike for a paseo-director M0 risk and record a reproducible go, no-go, or scope decision. Use when validating Paseo APIs, provider MCP behavior, Beads/Dolt, Git/GitHub, platform support, licensing, or another roadmap stop condition.
---

# Director Technical Spike

Answer one risky question with the smallest reproducible experiment. Do not disguise product implementation as research.

## Frame the question

Before changing files, record in the Beads Task:

- one falsifiable question or hypothesis;
- the plan invariant or milestone gate it affects;
- success and failure criteria;
- time/effort boundary;
- supported versions, operating systems, and topology;
- allowed fallback decisions.

Stop and split the work when the spike contains more than one independently decidable risk.

## Gather evidence

1. Read current primary documentation and record its URL/version/date.
2. Inspect the installed runtime rather than assuming documentation and installation match.
3. Prefer public APIs and disposable repositories/data.
4. Record exact commands, inputs, versions, outputs, and failure states.
5. Test interruption, retry, and cleanup when the question concerns lifecycle or side effects.
6. Redact credentials, user code, and private paths from committed evidence.
7. Clean all temporary processes, worktrees, branches, repositories, and data after the experiment.

Do not rely on undocumented endpoints, internal Paseo databases, UI click automation, or log parsing as product infrastructure.

## Decide

Produce an ADR using `docs/adr/0000-template.md` with exactly one outcome:

- **Go:** the public supported mechanism meets every criterion.
- **No-go:** the mechanism fails; select an approved contingency or reduce scope.
- **Inconclusive:** identify the missing evidence and keep the gate blocked.

Include alternatives considered, raw evidence locations, consequences, compatibility bounds, and follow-up Tasks.

Update and close the Beads Task only when another agent can reproduce the evidence and the ADR resolves its gate. A negative result is successful spike output when it prevents a fragile implementation.

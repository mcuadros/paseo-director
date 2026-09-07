# Beads usage retrospective: eleven days of agent-driven tracking in a sibling project

- **Status:** Evidence for a future Task
- **Date:** 2026-09-06
- **Beads Task:** `dir-m5.12`
- **Plan gate:** none; input for repository governance (`AGENTS.md`, skills) and for the TaskStore, effect and sync contracts (`dir-m0.4`, `dir-m0.10`, `dir-m0.5`)

This document records what happened when a sibling Go platform project ("the source project") tracked eleven days of almost fully agent-driven work in Beads, and what Director should adopt or avoid as a result. Issue identifiers from the source project (`brain-…`) are kept because they make the evidence checkable by the owner; hostnames, personal data, credential names, customer names and repository links were deliberately removed. Quotations are verbatim fragments of tracker text.

## 1. Why this document exists

Director will store Epics, Tasks, Runs, Candidates, reviews and audit records for every Project it manages (PLAN §5, §8). Its own development is also tracked in Beads (PLAN §22). The source project is the closest available real-world sample of agents writing to a tracker at scale: 227 issues, 200 closed, 846 KB of notes, 91 comments, 279 audited status changes and 145 pull requests between 2026-08-26 and 2026-09-06, produced by roughly fifteen Paseo agent identities and one human.

Director's own backlog already shows the first symptoms one day into M0: 53 KB of notes on `dir-m0.2` and `dir-m0.4`, a 9 KB review report pasted as a comment on `dir-m0.4`, four review cycles narrated in comments on `dir-m0.6`, and every audit row attributed to the human because no agent passed `--actor`.

## 2. Corpus and method

Sources read in full:

- `.beads/interactions.jsonl`: every status, assignee and priority change with its recorded reason (298 rows).
- All 91 comments on 24 issues.
- `bd list --all` metadata for all 227 issues (timestamps, labels, dependencies, sizes).
- Description, acceptance criteria and notes of ~225 issues, read by three independent read-only passes split by epic; the strongest claims were then re-verified directly against the database and the repository.
- The dispatch prompt that launched the source project's agents, its pull-request template and the bodies of 145 pull requests.

Numbers below are counts over that corpus; they are not extrapolations.

## 3. Quantitative signals

| Signal | Value |
|---|---|
| Median time from creation to closure | 2.8 h; 58 of 207 closures under one hour; three audit tasks show 16–18 s between start and close |
| Batch closures (three or more issues within two minutes by one actor) | 14 batches; five issues closed within five seconds sharing one pasted note (`brain-zbj.13/.14/.16/.17/.18`) |
| `blocked` transitions that carried a reason | 0 of about 30; blocked spans ranged from 3 minutes to 68 hours |
| Issues with more than one status change | 30; one issue went through 11 transitions in five days (`brain-nit.6.8`) |
| Closed issues closed by their own assignee | 164 of 200 (82%); every ad-hoc child had `created_by == assignee` |
| Close reasons that are literally `Closed` or empty | 6; the real reasoning lives in comments that `bd show` does not print |
| Notes size | median 2.2 KB, p90 8.2 KB, maximum 39.7 KB; 17 issues above 10 KB; 47 issues are dated journals |
| Closed tasks with empty description **and** empty acceptance criteria | at least 39 (35% of one epic group) |
| Notes stored with a literal `\n` instead of a newline | 8 issues, plus one acceptance-criteria field |
| Ephemeral or private strings in tracker text | 75 temporary-directory paths in 51 issues, 6 home-directory paths, 158 agent UUIDs, service hostnames, credential file names, vault item identifiers, chat and identity-provider subject identifiers, personal names. No credential values. |
| Labels | 130+ distinct values; about 45 `area:*`; `area:` and `component:` describe the same axis; five labels sit on every issue of one epic; 34 labels used once; `needs:*` used eight times, inconsistently |
| Dependencies | 236 `blocks` (44 of them pointing at a single publication gate), 213 `parent-child`, 98 `discovered-from`, 1 `related` |
| Pull-request bodies citing a bead identifier | 88 of 145 (61%) |
| Audit actor attribution | 76 of 298 rows say the human's name although many are agent-written ("Taken by …", "Implemented per Plan …"); other rows carry ad-hoc strings such as `paseo:/root` or `codex:root/…` |
| Audit completeness | 15 `open → in_progress` rows against 192 `in_progress → closed` rows: claims were not journaled; `bd history` is flooded by a five-minute export-and-push job that touches every issue, so it is unusable as a journal |

## 4. Anti-patterns

Each item states the pattern, the evidence, and the rule Director should adopt.

### 4.1 Notes as an unbounded append-only work log

Notes were treated as a diary: dated blocks appended forever, never compacted. Corrections were stacked on top of wrong text ("The prior note describing a public low-level transport is superseded", `brain-zbj.11`; "Status correction: this child Bead's acceptance explicitly ends at exact-head reviewed normal PRs", `brain-prod0.40.1`), so the current truth is only at the tail. About a third of all note bytes narrate superseded candidates and review rounds. Twelve consecutive entries on `brain-nit.1.7` are pull-request and CI status lines. Whole paragraphs were pasted verbatim into three issues (`brain-nit.3.4`, `brain-nit.3.6`, `brain-nit.6.8`) and even twice into the same issue (`brain-prod0.8`).

Rule: notes are a bounded, overwritten current-state summary. Chronology goes into append-only journal entries with a fixed header. Nothing is pasted across records; records link to each other.

### 4.2 Receipt tasks created after the work

Dozens of tasks were created mid-execution as records of steps already done or about to be run, with empty description and acceptance criteria, and closed within minutes: 66 s (`brain-prod0.26`), about one minute (`brain-prod0.20`, `brain-prod0.37`), two minutes (`brain-prod0.60`). One closed issue has no description, criteria or notes at all (`brain-prod0.42`). Two tasks split one operation into "release" and "promote and deploy"; the second was superseded and never executed (`brain-prod0.53`, `brain-prod0.54`).

Rule: a Task exists before its work and carries a description, verifiable acceptance criteria and a Definition-of-Done rung. Operational steps performed during a Run are journal entries or Run evidence, not Tasks.

### 4.3 Undefined Definition of Done

The tracker never said which rung counts as done: code complete, reviewed, published, integrated or deployed and verified. Consequences: an issue closed and reopened one minute later ("a completed code milestone, not repository delivery", `brain-24s`); a premature close under "the approved rule requiring merge" (`brain-prod0.56`); an issue closed as verified before the acceptance criterion's own user-interface check ran and later disproved (`brain-prod0.55`); an issue closed although its notes state "the no failed/blocked/dead criterion is objectively false" (`brain-prod0.7`); acceptance criteria re-read after the fact to permit closure ("its earlier note saying it remained open pending merge/release was overly broad", `brain-prod0.40.1`).

Rule: every acceptance criterion names its terminal rung on a fixed ladder, and closure requires that rung with evidence. Director's product already separates Candidate approval from Task completion (PLAN §13, §24.1); the repository skills must say the same thing in one vocabulary.

### 4.4 Acceptance criteria that bundle code with live rollout

Where one task required both merged code and a live receipt from an environment nobody could authorize, the task stayed blocked for its whole life: "The operator forbids creating/reading the cloud recovery set needed for that fact" (`brain-nit.5.4`); "a no-op import cannot be proven" without external identifiers that were never supplied (`brain-nit.3.2`); a missing governance environment (`brain-nit.4.8`). One task drifted into building an unrelated provider bootstrap that was later reversed unmerged.

Rule: code delivery and live activation are separate Tasks with separate rungs. A rollout Task depends on the code Task; it never shares its acceptance criteria.

### 4.5 Batch closure with pasted evidence

Five acceptance criteria were "verified" by one pull-request merge, closed within five seconds with the identical sentence in every record (`brain-zbj.13`–`.18`, then `brain-zbj.15/.19`–`.22`).

Rule: each closure cites its own evidence. The engine never closes several Tasks from one evidence record; a human bulk action still produces one audited closure per Task.

### 4.6 Status used as a signal channel

`blocked` was toggled every few minutes while waiting for CI or a reply (3 and 6 minutes on `brain-stg0.37`); periodic "queue sanitation" sweeps flipped stale `in_progress` issues to `blocked` and back when someone resumed (`brain-nit.3.4`, `brain-nit.3.6`, `brain-nit.5.4`, `brain-nit.6.8`); `open` was used to release a claim rather than to describe the work (`brain-zbj.4/.5/.8`); not one `blocked` transition carried a reason; in another epic group `blocked` was never used and blockers lived in prose headed "BLOCKER:" (`brain-prod0.4`, `brain-zbj.7`).

Rule: status describes work state only. `blocked` requires a typed reason (`human-decision`, `dependency:<id>`, `external:<system>`, `capacity`). Short waits stay `in_progress`. Claims are released by clearing the assignee. No sweeps.

### 4.7 Human gates invisible as state

Decisions were requested and answered inside notes and comments ("[DECISION BLOCKER — frozen by operator order]", `brain-nit.6.8`; "Human decision required: approve KEEP or INTEGRATE", `brain-nit.2.6`). Nothing in status, labels or fields said "waiting for a human", so no list could show the human's inbox. One task burned two design cycles before a reversal arrived ("capture is a simple installation-level on/off switch", `brain-prod0.58`). Risk acceptances were buried in prose (`brain-stg0.33`). Humans also bypassed agent gates twice, honestly recorded but only as sentences (`brain-nit.3.3`, `brain-nit.3.4`).

Rule: a human question is a durable record with a state (PLAN §9.2 `Needs you` gains an explicit waiting-human-decision cause) and the answer is stored verbatim as a decision record. Human overrides are audited commands, not comments.

### 4.8 Scope creep turning Tasks into Epics

A deployment task with no description absorbed a paid model benchmark, a CI cache fix, three release attempts, credential work, an agent takeover and an architecture proposal that spawned a 27-issue epic (`brain-prod0.36`, 20 KB of notes). A lifecycle feature absorbed an entire release-lineage convergence (`brain-nit.3.4`, 39 KB). A bug became the parent of six children at a third hierarchy level (`brain-prod0.40`). A validation task became the design thread for a new subsystem before spawning six siblings (`brain-stg0.14`).

Rule: when a Task discovers a second independently decidable outcome, it files a sibling and stops growing. Hierarchy stays at two levels; Director's configuration already enforces `max-depth: 2`.

### 4.9 Incidents and systemic findings absorbed, never filed

A credential exposure during an operational step was recorded inside a running task with the instruction that the identity "must be treated as compromised and rotated", and no rotation task exists. A production worker crash loop was narrated mid-note in `brain-prod0.40`. Five "required systemic contracts" listed in `brain-prod0.32` were never filed. A diagnosis that "was wrong twice and in the same direction" (`brain-tdw`) was caught only by independent review.

Rule: incidents, security findings and systemic follow-ups are always their own Task with `discovered-from`; a running Task may not carry them silently.

### 4.10 Coordination by prose

Migration-number collisions between concurrent tasks were negotiated in comments ("brain#420 carries migration 000011 … brain#419 also claims 000011; renumber pending migrations at rebase", `brain-nit.6.7`). A handoff that said "read the other bead's notes" failed: "My miss: I read this bead's dependency graph and notes but not 3.6's own notes, where the naming decision was already written" (`brain-nit.6.8`), costing two pull requests. A worktree was archived while another agent was reading it ("was archived while I was reading it", `brain-nit.3.6`), and the work had to be recovered from a temporary clone.

Rule: shared resources that can collide (migration numbers, branch names, worktrees, release identifiers) are reserved through durable records owned by the engine, never through comments. Handoffs use a fixed schema and the receiver acknowledges before editing.

### 4.11 Decisions trapped in the tracker

The five human architecture review sessions were recorded in 27 KB of notes on `brain-50k.13` while the repository workbook still reads "decisions pending" with every choice blank. Four `decision` issues holding 8–18 KB of threat models and measurements have no repository artifact (`brain-nit.2.5`, `brain-nit.2.6`, `brain-nit.2.7`, `brain-nit.4.7`). One task description is a 350-word root-cause analysis, effectively an ADR body (`brain-nit.8`).

Rule: durable rationale lives in ADRs or documents in Git; the tracker stores the pointer and a one-line outcome. A `decision` Task closes only with the path of its ADR.

### 4.12 Sub-epic status hand-maintained

Parent status was not derived; agents ran "program audit" sweeps that flipped parents between `open`, `blocked` and `closed` ("parent moved from OPEN to BLOCKED until .4.8 external gates are supplied", `brain-nit.4`). One sub-epic was closed, reopened from live evidence and closed again (`brain-nit.6`). The root epic was `open` while three children were `blocked` and four `closed`.

Rule: parent and Board state are projections of child facts (PLAN §9.3, invariant 15). No agent edits a parent's status.

### 4.13 Identity hygiene

No agent passed `--actor`, so agent writes were attributed to the human's Git name, and a human approval could not be distinguished mechanically from an agent narrating one. Other rows carried improvised actors (`paseo:/root`, `codex:root/…`). Assignee UUIDs were pasted into prose; reassignments were narrated ("Taken by …", "Dedicated fixer … uses …") while the field said something else.

Rule: every write carries a typed actor (`human:<name>`, `paseo:<agent-id>`, `system`). Identity lives in fields, never in prose. Director's audit model (PLAN §5.2) should attribute agent actions to the agent identity, not only to `organizer` or `system`.

### 4.14 Private and ephemeral data in a synchronized tracker

Temporary worktree paths, home-directory paths, credential file names, vault item identifiers, service hostnames, chat identifiers, identity-provider subjects and personal names entered notes and descriptions. The database was exported and pushed to a remote every five minutes. No credential value leaked, but nothing enforced that; the boundary was convention.

Rule: redaction applies to more than credentials (PLAN §18.1): ephemeral paths, hostnames, personal data and agent identifiers are not written into records that synchronize. Notes with a literal `\n` (eight issues) show that agents also need a safe write path for multi-line text.

### 4.15 Label explosion

130+ labels for 227 issues; two parallel taxonomies for the same axis; boilerplate labels inherited onto every child; 34 single-use labels; the only decision-relevant family (`needs:*`) used eight times and inconsistently. No entry anywhere shows a label ever selecting or routing work.

Rule: one small fixed taxonomy declared in `AGENTS.md`; routing state lives in status and typed records, not labels.

### 4.16 A dispatch contract that induced the drift

The prompt that launched the source project's agents told them to "complete the requested outcome end to end, including every deployment", to "create linked Beads and execute it automatically", to "close the Bead when its outcome and acceptance criteria are genuinely satisfied", and to "continue automatically with the next unambiguous ready task". That contract explains most of the receipt tasks, the self-closures, the deployment scope creep and the identity reuse across tasks.

Rule: Director's Task Agent contract stays the opposite (PLAN §12.3, §22): one Task per agent, engine-owned lifecycle effects, closure only after independent review and verified integration.

## 5. Practices to keep

These worked and should become mandatory rather than optional habits.

- **Immutable candidate locked in the first child** and re-quoted everywhere: `brain-stg0.1` recorded the source revision, chart version and every image digest, and refused an unverifiable inherited reference ("did not resolve as a commit … rejected and was not used as evidence").
- **Artifact-citing one-line close reasons**: reviewed head, merge commit, CI run and review identifier (`brain-nit.4.10`, `brain-prod0.40.4`, `brain-prod0.33`).
- **Exact base and head on every claim, independent review re-anchored after every push**: three rounds on `brain-prod0.58`, five blockers found on `brain-zbj.2`, a wrong diagnosis caught on `brain-tdw`.
- **A negative-scope sentence closing every entry** ("no merge, release, deployment or live mutation"), and **pre/post fingerprints** of protected resources as proof of non-destruction (`brain-stg0.27`).
- **"Intentionally absent" distinguished from "untested"** (`brain-stg0.1`, `brain-stg0.15`).
- **A seed constraint written at creation** that the executor must honor ("Never restore over active staging.", `brain-stg0.16`).
- **Numeric, mechanically checkable acceptance criteria** ("removes at least 60 non-test lines and 12 control-flow nodes", `brain-nit.2.8`) and **machine-checkable coverage sets** (51 improvement identifiers listed on the roadmap epic).
- **Refusing to close on unmet criteria and saying why** (`brain-prod0.31`, `brain-prod0.40`, `brain-zbj.23`, `brain-nit.3.2`), and **respecting the tool's dependency gate** ("bd close correctly refused because declared dependency … remains open", `brain-nit.3.6`).
- **Verbatim, dated human decisions** ("Human decision required …", "Human correction …", "Human approved KEEP …") including reversals and overrides.
- **A structured handoff note**: STATE, CONSTRAINTS, TWO DECISIONS FOR A HUMAN, ACCEPTANCE (`brain-nit.3.4` opening note).
- **Pre-spend freeze and recorded cost for paid provider runs** ("NO ejecutar nuevas llamadas pagadas todavía", `brain-zbj.10`; a measured stop boundary and final spend on `brain-prod0.35`).
- **Dependency graph designed before execution** with named ready leaves and verified acyclicity (`brain-zbj`), and **review findings filed as sibling Tasks that block the release Task** (`brain-zbj.9` → `brain-zbj.13`–`.18`).
- **Decision Tasks that may end in KEEP** with the cost quantified (`brain-nit.2.5`, `brain-nit.2.6`, `brain-nit.4.7`).
- **Review verdict bound to the exact head** through a pull-request review trailer and a status check, so a moved head automatically invalidates the verdict.

## 6. Proposed rule set for `skills/director-task-record`

The future skill should be short and procedural. The schemas below are the substance.

### 6.1 Definition-of-Done ladder

`candidate` → `reviewed` → `published` → `integrated` → `deployed/verified`. Every acceptance criterion names the terminal rung. Code Tasks normally end at `integrated`; activation Tasks end at `deployed/verified` and depend on the code Task.

### 6.2 Record before work

- A Task has a description, bulleted verifiable acceptance criteria, a rung and a parent Epic before it is claimed. Bugs need acceptance criteria too.
- No Task is created to describe work already done. Steps of a Run are journal entries.
- Discovered work becomes a sibling with `discovered-from` only when the discovery is causal; incidents always get their own Task.

### 6.3 Notes: bounded current state (overwritten, real newlines, at most 3 KB)

```text
Objective delta: <what changed versus the description, or none>
Candidate: <sha> on <base sha>, branch <name>
Gates: review <pending|done ref> · CI <pending|done ref> · PR <pending|#n> · integration <pending|merge sha> · cleanup <pending|done>
Blockers: <typed reason or none>
Decisions: <ADR or document paths>
Next: <single next action>
```

### 6.4 Comments: append-only journal entries (at most 12 lines each)

```text
<UTC timestamp> <actor> <kind>
<facts with exact identifiers>
Not done: <one negative-scope line>
```

`kind` is one of `candidate`, `review`, `correction`, `decision`, `handoff`, `blocked`, `unblocked`, `evidence`, `incident`, `closure`. Full review reports go to the pull-request review or to `docs/evidence/`, never into a multi-kilobyte comment. Pull-request and CI status is recorded once per gate change, not mirrored.

### 6.5 Close reason

```text
<rung>; <artifact: PR number / merge sha / release id>; <review reference>; <residual risks or none>
```

Never `Closed`. Never one pasted block for several Tasks. Never a closure whose notes or criteria contradict it.

### 6.6 Status semantics

- `blocked` only with `--reason` naming the kind: `human-decision`, `dependency:<id>`, `external:<system>`, `capacity`.
- Waiting less than an hour for CI or a review stays `in_progress`. No sweeps.
- A human question uses `bd human <id>` plus a `blocked` entry; the answer is recorded verbatim in a `decision` entry.
- A claim is released with an empty assignee, never by reopening.
- Parents are never edited by hand.

### 6.7 Handoff entry

```text
<UTC> <actor> handoff
STATE: <rung reached, exact Candidate and base>
OPEN GATES: <list>
BLOCKERS: <typed>
DECISIONS FOR A HUMAN: <list or none>
NEXT: <first action for the receiver>
```

The receiver posts an acknowledgement entry before editing anything.

### 6.8 Redaction and identity

- No temporary or home-directory paths, hostnames, credential file names, vault identifiers, personal data or agent UUIDs in prose. Identity lives in the assignee field and the actor.
- Every `bd` write passes `--actor paseo:<agent-id>` (or exports `BEADS_ACTOR`); humans write as `human:<name>`.
- English only.

### 6.9 Labels and cost

- One declared taxonomy; no ad-hoc labels.
- Paid provider runs need a `decision` entry with the authorized budget before the run and an `evidence` entry with the measured cost after it.

## 7. Inputs for the product contract

| Lesson | Where it lands |
|---|---|
| Free-text notes cannot be the journal; corrections and duplicates hide the truth | `dir-m0.4`: immutable typed Event records (candidate, review, validation, publication, integration, cleanup, decision, handoff, blocked, unblocked, human_feedback, incident); any notes surface is a bounded derived summary |
| Closures need their own evidence; batch closure destroys it | `dir-m0.10`: closure command requires linked evidence records and a structured reason; one audited closure per Task (extends PLAN §9.3, §24.1) |
| Waiting for a human is invisible | PLAN §9.2: `Needs you` gains a `waiting_human_decision` cause carrying the question; the answer is persisted as a decision record |
| Agent writes attributed to the human | PLAN §5.2: audit entries carry `task_agent:<id>` / `reviewer:<id>` alongside `human`, `organizer`, `system` |
| Ephemeral and personal data in synchronized records | PLAN §18.1: redaction covers paths, hostnames, personal data and agent identifiers in prompts, audit payloads and summaries |
| Export/push jobs that rewrite every record make history useless | `dir-m0.5`: synchronization must not rewrite unchanged records; history is per-change |
| Migration numbers, branch names and worktrees collided | `dir-m0.10`: shared identifiers are reserved through engine-owned intents, never negotiated by agents |
| Parents edited by hand drift from children | PLAN invariant 15 and §9.3: Epic progress is derived only |
| Code and live rollout bundled in one acceptance | Organizer `templates/task.md`: rung field; scheduler treats activation Tasks as dependents of code Tasks |
| Decisions trapped in the tracker | Organizer `decisions/` is the durable home; TaskStore stores the pointer |

## 8. Deliberately left out

Hostnames, personal names and e-mail addresses, credential and vault identifiers, chat and identity-provider identifiers, customer names, pull-request links to the private repositories, and two security-relevant observations that were reported privately to the owner. Nothing in this document requires access to the source project to be understood; everything in it can be re-derived from the source project's Beads database by its owner.

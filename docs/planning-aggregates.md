# Epic, Task, and dependency planning contract

The standalone Go Director Engine owns the planning model and every dependency
decision. Director for Paseo may transport typed commands and render later
engine projections, but it cannot validate a graph, release a blocked Task, or
interpret an override. The engine-only derived-state query described below was
absorbed from `dir-m2.4`; it does not expand the existing connector or UI.

Each Project may contain zero or more Epics and Tasks. An Epic is the only
grouping level. A Task either has one Epic parent or is standalone, and it
targets exactly one of the Project's canonical Workspaces. A Task dependency
may target another Task or an Epic in the same Project. An Epic dependency may
target only another Epic. Consequently, a Task in one Workspace may depend on
a Task in another Workspace without weakening either Workspace's canonical
repository identity.

Task IDs are canonical Director identities. External references are bounded
provider/key metadata and use a distinct type; a GitHub issue or another
external key can never become a hierarchy or dependency endpoint. Priority is
`urgent`, `high`, `normal`, or `low`, with `normal` as the default when a
proposal omits it.

`domain.EvaluatePlanning` is a pure deterministic operation. It validates the
complete Project graph and emits closed machine-readable explanation codes.
It rejects unknown endpoints, invalid dependency kinds, self edges, duplicate
edges, cycles, Tasks with zero or multiple Workspace bindings, Task parents
which are not Epics, and any parent on an Epic. Direct and transitive Task
blockers and inherited Epic blockers are evaluated separately. An incomplete
blocking Epic propagates to every Task in the dependent Epic; a standalone
Task receives no implicit Epic blocker.

The independently integrated `engine/internal/planningtestkit` remains test
infrastructure only. Production code does not import it. Domain property tests
translate its public facts into the production model and compare observable
validity, Task blocking, and closed explanation-code presence against its
black-box oracle across deterministic seeded corpora and exhaustive small
states.

## Derived Board/List state and queries

`projection.DeriveTaskProjection` consumes a closed set of normalized durable
and external facts. Every fact is explicitly `missing`, `current`, `stale`, or
`contradictory` and binds the canonical Task version plus the relevant Run or
Candidate identity. The reducer derives Needs you, Queued, Building,
Validating, In review, and Ready. Done is separate List/filter membership, not
an editable lane. There is no state or card-position field in the reducer
input.

Only a current, fully typed pending human-input fact yields Needs you. Agent
outcome claims can block unsafe advancement when missing, stale, contradictory,
or incorrectly bound, but they never prove a lifecycle state. Validation and
independent Review remain sibling obligations; delivery, cleanup, and terminal
facts must all be current and consistently bound before Done is emitted.
Missing or unusable proof keeps the Task in the last state supported by durable
facts and emits closed blocker and attention codes in canonical order.

`projection.QueryTaskProjections` filters by Board/Done membership, Project,
Workspace, Epic or standalone status, priority, labels, state, and attention.
It sorts canonically by lane display order, priority, queue time, and Task ID.
Opaque cursors bind the decimal TaskStore event snapshot, normalized filters,
and the complete last-row ordering key; a cursor cannot cross a snapshot or
filter set.

The application `DerivedReader` samples the event high-water mark before and
after loading engine-side normalized facts and returns only a stable snapshot.
`TaskStoreFactSource` converts existing Project/Workspace/Epic/Task/Run/
Candidate records into facts and uses the planning graph for dependency waits.
Later lifecycle evidence remains explicitly missing until its owning aggregate
exists; neither the source nor TaskStore fabricates Validation, Review,
delivery, or cleanup proof. The durable Task completion flag supplies only its
terminal fact and cannot prove Done without those other records. The M1 host
contract and policy-free connector are unchanged.

Runtime projection tests translate facts and results at the test boundary and
compare stages, adversarial matrices, seeded cases, ordering, and pagination
against the independently integrated
`engine/internal/testkit/projectionoracle`. Production packages never import
that test-only oracle.

## Audited dependency overrides

A dependency is skipped for one Task only when exactly one immutable override
fact matches all of the following:

- the canonical Task ID and its current optimistic version;
- one exact dependency edge which affects that Task, including an inherited
  Epic edge;
- the fixed `human` actor kind and a bounded authenticated human actor ID;
- a non-empty stable audit ID, a positive TaskStore/server timestamp, and the
  granted flag.

Missing, stale, ambiguous, unaudited, ungranted, non-human, or irrelevant
override facts remain visible as closed findings but never release the Task.
One Task's override never releases a sibling Task affected by the same Epic.

The typed `GrantDependencyOverride` TaskStore operation is the only write path
for a qualifying fact. Its command targets the new override identity and binds
the expected Task version in the typed payload. The TaskStore supplies the
human actor kind, granted value, and server time; caller-selected actor-kind or
timestamp fields fail the exact-payload check. Overrides are append-only. A
later Task update makes an older fact stale rather than rewriting audit
history.

## Direct-Dolt persistence and concurrency

Epics remain versioned aggregates under their Project, Tasks remain versioned
aggregates under their Project, and override facts are append-only aggregates
under their Task. The existing generic `aggregates` table and schema-version-1
guards already represent these kinds, so no direct-Dolt schema migration is
required.

Every Epic or Task graph mutation locks the parent Project row before loading
and validating the complete Workspace/Epic/Task/dependency graph. Concurrent
writers are therefore serialized: reciprocal edges cannot both commit a
cycle, and stale Epic/Task replacements retain the existing durable
expected-version outcome. Reads validate the complete planning graph and fail
closed with the bounded stored-record health code if durable rows drift into
an impossible structure.

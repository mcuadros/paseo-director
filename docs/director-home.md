# Director Home and Project health

Director Home is a global React Native surface over the engine-owned
`director-planning/v1` contract. Paseo owns the route, header, close action,
host picker, error boundary, and query client. Director owns only the surface
body and uses Paseo's v0.7 `Modal`, `Icon`, and toast components for matching
desktop/mobile chrome. The client supplies the exact public Paseo
`host.id` with every request. The separately supervised engine is started with
the same explicit host ID and rejects a mismatch; Project IDs and Workspace
keys are never used to select or infer a host.

The query cache key contains the host ID. Every paginated response also binds
the host ID, engine instance ID, TaskStore event cursor, totals, and Project
identity. A later page from another host, a restarted engine, or another
snapshot is rejected. Resetting the exact-host cache starts again at page one;
no error path falls through to a different host.

## Projection

For each Project, Director Engine returns:

- Project identity, version, durable state, and bounded health reasons;
- every Director Workspace identity and only an explicitly observed native
  Paseo Workspace navigation target;
- Organizer mode, phase, active revision, and configuration digest, without
  its repository path;
- lease state, epoch, and expiry, without holder or process identity;
- the separate Organizer Git and TaskStore sync results;
- open, completed, active, and Needs-you counts derived from current Task/Run/
  Candidate facts; and
- actions declared by the engine with exact host, Project, version, native
  navigation, confirmation, and availability bindings.

`Healthy` requires current host, Organizer, lease, sync, and Workspace facts.
Missing operational facts are `Unknown` or `Unavailable` and degrade the
Project. A partial Git/TaskStore sync remains explicitly `Partial`; the
successful half is not repeated or hidden. Stale/disconnected snapshots may be
shown as last-known data, but every action is disabled.

Needs-you aggregation retains only bounded reason codes and counts. Home does
not include Task titles, objectives, acceptance text, wake-condition content,
repository remotes or paths, lease holder/process identities, credentials, or
raw adapter errors.

## Create and Adopt

`Create Project` and `Adopt Organizer` open a host-bound form. The client must
mint a secure request ID; absence disables both Preview and Apply. Preview is a
read-only call to the existing Director Engine Organizer service. It returns
the exact selected Organizer path, file digests, operations, revision/config
digests, and bounded validation issues directly to that authenticated human
surface.

Apply is a separate press which resubmits the unchanged request ID, inputs, and
exact Preview SHA-256 with explicit confirmation. The connector supplies the
server-authenticated human identity. Director Engine re-runs Preview before
performing the existing idempotent Create/Adopt saga. Editing any field clears
the Preview. A host mismatch, contract drift, missing authentication, changed
Preview, or unavailable TaskStore fails closed before mutation.

## Presentation and refresh

Home uses Paseo theme tokens and React Native primitives only, with no literal
colors. The information design takes AO's dense status emphasis without
copying its chrome: one summary strip, one clearly bounded Project list,
compact Work/Organizer/Operations groups, semantic success/warning/danger
tokens, and engine-declared actions. Wide layouts use three compact detail
groups per Project row; compact web/mobile layouts stack those groups. Create
and Adopt use the host Modal rather than another dashboard panel. Controls have
a 44-pixel minimum target, accessible roles/states and live announcements, and
no hover dependency.

The first page contains at most 25 Projects and subsequent pages are explicit.
The query refreshes every 30 seconds with no transport retry, on mount, and on
network reconnect. A refresh failure preserves the exact cached snapshot as
stale. Engine restart or cursor invalidation requires `Refresh exact host`,
which resets only that host's cache.

`Board` and `Organizer` call public Paseo navigation only when the engine
returned an exact native Workspace ID and the installed host exposes
navigation.

`Operations` opens the host-owned Project Operations modal. Its Health & Sync,
Audit, Logs, and Support tabs render the standalone engine's strict projection;
see [Sync, reconciliation, audit, logs, and support bundles](operations-diagnostics.md).
The existing `Sync now` and `Reconcile now` actions enter the same exact-host
surface rather than introducing a second operational shell.

## Doctor diagnostics

Doctor diagnostics are strictly read-only and never mutate engine, workspace,
or repository state. The client submits only exact host, Project, and expected
Project-version identity to `POST /v1/planning/doctor`. The Go application
service depends on a read-only Store interface and a policy-free operational
observer; it has no mutating port. It samples the TaskStore Event cursor around
the read and retries only the read at most three times when that cursor moves.
Selecting `Doctor` on any Project row opens a modal that displays the
engine-owned report for:

- Host and Paseo runtime compatibility (Paseo 0.7.2);
- Admitted provider CLIs (OpenAI Codex 0.147.0, Anthropic Claude Code 2.1.258,
  OpenCode 1.18.18);
- MCP stdio session bridge protocol and exact tool policy (no wildcards);
- Authoritative Project execution lease (epoch, expiration timer, observe-only
  status);
- Git and dynamic state sync stream alignment;
- Workspace recovery references and health.

Every non-passing diagnostic identifies one concrete missing capability and
one or more bounded installation/remediation steps. The closed engine mapping
covers exact Paseo/connector contracts, the admitted provider CLI versions,
provider authentication, session-scoped stdio MCP, exact MCP tool policy,
rootless OCI, repository identity, finite resource observations, Organizer,
lease, sync, and Workspace recovery facts. Adapters submit only closed
capability/state observations; they cannot author guidance or decide blocking
semantics. The report contains no path, remote, raw tool output, credential,
holder/process identity, log line, or fallback host. Doctor performs no
download, installation, repair, dispatch, retry, or write.

The modal has distinct loading, healthy, degraded, blocking, offline, stale,
and error presentations. An absent or stale capability observation is never
rendered as passing. Cached Home data never substitutes for a Doctor report.

## Repair preview and human confirmation

When a Project is degraded or sync streams are unaligned, Doctor offers
`Repair Project…` which transitions into a deterministic two-phase Repair
workflow:

1. **Preview**: Director Engine rereads current facts and computes only those
   operations for which the exact Project observation advertises a matching
   repair capability and an idempotent engine executor is wired. Supported
   operation vocabulary is Git-sync reconciliation, TaskStore/Dolt-sync
   reconciliation, Project-lease reconciliation, and owned Workspace-recovery
   reconciliation. The Preview is hashed with real SHA-256 over the exact host
   instance, Project/version, Event cursor, Doctor observation, request, and
   ordered operation records.
   The modal clearly displays:
   - Preview ID and base Project version;
   - Concrete repair operations to be performed;
   - Specific affected resources;
   - Human confirmation notice requiring explicit user action.

2. **Apply**: Applying the repair Preview requires explicit confirmation in the
   request and a server-authenticated human actor supplied by the connector
   (`x-director-actor-kind`, `-id`, and `-session`). The client cannot supply
   that identity. Director Engine recomputes the Preview from fresh facts,
   compares its SHA-256, dispatches the one idempotent exact plan, and reports
   success only after current durable Project-version and Event-cursor readback
   match the executor evidence.

Repair fails closed if:
- The host instance, Event cursor, Doctor observation, base Project version,
  or operation set has drifted;
- The `previewId` does not match the engine's freshly recomputed Preview;
- The request originates from a non-human actor or unconfirmed state;
- The host or engine is disconnected, stale, or archived;
- The operation lacks an exact executor capability, or its evidence/readback
  does not match.

Every Preview operation is explicitly non-destructive and
`automaticInstall=false`. Missing executables and authentication remain manual
guidance; Repair never installs software, switches provider/delivery/store,
selects another host, deletes a path, or retries an unknown effect. Exact
same-request replays return the first observed durable outcome; changed
payload bindings or terminal drift are refused without another effect.

Unavailable Sync, Reconcile, Repair, Pause/Resume, or Emergency-stop actions
remain visible with their engine reason and cannot be invoked. Pause, Resume,
and Emergency stop submit only the exact engine-issued planning command ticket.

The shared cross-platform focus, touch, announcement, reflow, contrast, and
reduced-motion contract is documented in
[Mobile and accessibility behavior](mobile-accessibility.md).

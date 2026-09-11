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
navigation. `Doctor` renders the engine's bounded current health summary.
Unavailable Sync, Reconcile, Repair, Pause/Resume, or Emergency-stop actions
remain visible with their engine reason and cannot be invoked. Pause, Resume,
and Emergency stop submit only the exact engine-issued planning command ticket.

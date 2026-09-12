# Sync, reconciliation, audit, logs, and support bundles

Project Operations is a native Paseo modal opened from Director Home. Paseo
owns the route, header, host picker, modal chrome, theme, and query client.
Director renders only the modal body with React Native primitives, the shared
Director accessibility provider/pressable, and Paseo theme tokens. Host compact
mode or a window narrower than 1,200 points stacks the same information
hierarchy without introducing a plugin-owned shell or palette. Controls keep
44-point targets, touch hit slop, pressed and keyboard-focus feedback, text
reflow, queued announcements, high-contrast boundaries, and static loading
icons when reduced motion is enabled.

The UI calls the standalone Director Engine through two generated,
Zod-validated operations contracts:

- `POST /v1/planning/operations` reads one exact host/Project/version report.
- `POST /v1/planning/operations-mutate` submits a manual Sync, Reconcile, or
  support-bundle Preview/Generate command under the server-authenticated human
  actor.

The TypeScript connector only validates, transports, and revalidates exact
bindings. It does not classify health, combine sync streams, redact output,
choose retries, or write bundles.

## Hybrid reconciliation

Director Engine projects the accepted hybrid model explicitly:

- daemon terminal events synchronously enqueue a wake with a target below
  1,000 ms;
- events remain wake signals and never prove completion;
- current external facts are observed every 30 seconds while Runs are active
  and every five minutes while all Runs are idle; and
- the five-minute multi-source watchdog is the only lost-event recovery path.

`Reconcile now` is a manual, idempotency-keyed request for the same
engine-owned reconciliation. It cannot bypass Project state, observation
freshness, an in-flight effect, or an unavailable executor. A possible prior
handoff is observed and adopted or refused; the UI never retries it.

## Independent Git and Dolt synchronization

Organizer Git and TaskStore/Dolt are two separate conditional effects. The
report preserves these stream states independently: `current`, `local_ahead`,
`remote_ahead`, `diverged`, `failed`, `identity_mismatch`, `not_configured`,
`unavailable`, and `stale`.

Each stream has one closed reason code, optional exact revision fingerprints,
freshness, last-success time, and an explicit retryable fact. A `current`
stream requires two equal SHA-256 revision fingerprints. Ahead or diverged
states require two different fingerprints. Transport, authentication, parse,
and identity failures are never converted to absence.

The combined state is `partial` only when one stream is current and the other
is not. A manual Sync observes two outcomes. Recovery never repeats the
successful half, force-overwrites the newer side, selects another remote, or
presents the pair as an atomic transaction. Automatic synchronization retains
the approved one-minute debounce and flushes at critical transitions.

TaskStore backup/migration/retention is a separate engine-owned maintenance
state machine documented in [TaskStore maintenance](taskstore-maintenance.md).
Its recurring failures reuse the bounded `taskstore_unhealthy` technical-log
code. Below the fixed free-space floor, maintenance appends no diagnostic log:
bounded cleanup runs first and a continuing violation consumes no more disk.

## Structured audit

The audit view scans a bounded recent window of immutable TaskStore Events and
returns at most 64 Project-scoped entries. It includes only the global sequence,
an engine-owned category/action/outcome, an available actor class, and SHA-256
Project-scope subject and optional Run fingerprints. The actor remains
`unavailable` when the immutable envelope has no verified actor identity.

Event IDs, aggregate IDs, Task/Run IDs, payloads, paths, source content, actor
claims, and raw values have no representation in this contract. Unknown event
types become `unclassified_event`; untrusted type text is not reflected into
the UI or support material. The projection marks truncation instead of
claiming complete history.

## Bounded technical logs

Director's technical log sink accepts only a closed level, component, code,
timestamp, and occurrence count. There is no arbitrary message or attribute
field. Human-facing wording is owned by Director Engine. Raw Git, Dolt,
provider, process, MCP, test, and command output cannot enter the store or UI.

Each Project uses a pseudonymous mode-`0600` JSON-lines file under the
platform cache directory. The containing directory is mode `0700`, checked for
owner and symlink identity on every access. The store persists across restart,
rejects unknown fields/codes and replaced paths, retains no entry older than 14
days, never grows beyond 100 MiB per Project, and returns at most 129 records so
the UI can expose a 128-entry tail plus an honest truncation marker. The report
always sets `rawOutputIncluded=false`.

## Manual redacted support bundles

Support material is never generated in the background. The first human action
produces a content-addressed Preview listing exactly `manifest.json`,
`health.json`, `audit.json`, and `logs.json`. It also lists permanent
exclusions: source/diffs, prompts/conversations, environment,
credentials/authentication configuration, raw logs/command output, TaskStore
rows/event payloads, Organizer contents, and full paths or remote URLs. Project,
host, and native IDs are replaced with SHA-256 fingerprints.

`Generate local bundle` is a second explicit human action bound to the same
request ID, exact Project version, Event cursor, host instance, observation,
and Preview SHA-256. Any drift refuses generation. Before writing, Director
serializes only the allowlisted schemas and scans every byte for private-path,
URL-userinfo, authorization, token, credential, and private-key shapes. A
failed scan emits only `support_bundle_redaction_failed` and writes nothing.

The local ZIP is installed atomically without overwriting an existing target,
uses mode `0600`, has a safe deterministic filename, and is verified after
write. On response loss the same request observes and adopts the exact existing
bundle; an identity mismatch parks instead of overwriting it. The sink has no
network or upload method, the contract fixes `uploadPolicy=never`, and every
result fixes `uploadAttempted=false`.

The default local output is under the platform user cache at
`director/support`. The UI intentionally displays only the safe filename,
digest, size, mode, and generation time; full daemon paths never enter the
bundle or client contract.

## Failure behavior

Offline, stale, loading, unavailable, partial-sync, degraded, divergent, and
refused states remain distinct. Cached data may remain visible, but every
manual control and support Preview is disabled until the exact host is current.
The engine refuses missing server-authenticated human identity, Project-version
drift, contract drift, invalid secure request identity, unavailable effect
capability, ambiguous prior outcome, unsafe local roots, failed redaction, and
unverified post-write evidence. It never falls through to another host,
remote, provider, store, or upload path.

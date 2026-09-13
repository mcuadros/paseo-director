# Production onboarding and workflow

This runbook is the supported single-user Linux path for exact Paseo 0.7.2,
Dolt 2.3.2, and Director Engine. It uses public commands, the native Director
surfaces, and the generated HTTP/plugin contracts. It requires no raw SQL,
test fixture, private mutation endpoint, Paseo state edit, or daemon/machine
restart.

## 1. Start a private direct-Dolt store

Create an owner-only directory, initialize an ordinary Dolt database, and run
the public loopback SQL server from that database directory. The directory
basename is the database name passed to Director.

```text
install -d -m 0700 /absolute/private/director-dolt/director
cd /absolute/private/director-dolt/director
dolt init --name "Director" --email director@example.invalid
dolt sql-server --host 127.0.0.1 --port 3307
```

In another terminal, ask the engine to create and verify its complete schema,
identity, event cursor, and Project readback while atomically writing the
owner-only runtime configuration:

```text
director-engine bootstrap-taskstore \
  --config-output /absolute/private/director-runtime/taskstore.json \
  --address 127.0.0.1:3307 \
  --database director \
  --store-id director-local \
  --control-user root
```

The successful JSON result is path- and credential-free. Repeating the exact
command is idempotent. A different store identity, an unknown/partial schema,
unsafe file ownership or mode, a dirty migration frontier, or changed
configuration is refused. Password files, when the local Dolt deployment uses
them, must be absolute owner-only regular files and are supplied with the
documented `--control-password-file` and `--writer-password-file` options.

`serve-board` does not guess or replace a store. It starts only after the
bootstrap readback succeeds:

```text
director-engine serve-board \
  --listen 127.0.0.1:7041 \
  --taskstore-config /absolute/private/director-runtime/taskstore.json \
  --host-id local-paseo \
  --host-label "Local Paseo" \
  --host-socket /absolute/private/director-runtime/host.sock \
  --runtime-root /absolute/private/director-runtime/work
```

Use the same owner-only host-socket path for the plugin connector. Follow the
safe non-secret defaults and live plugin activation procedure owned by M6.10;
do not restart the shared Paseo daemon. A missing host socket, runtime root,
credential, exact version, provider tuple, or TaskStore fact keeps production
readiness closed.

## 2. Create from a native Paseo Project

Open Director Home and choose **Create from Paseo Project**. The standard flow
starts with the authoritative native Paseo Project selector. For the selected
Project, the connector reads and displays these public Paseo facts:

- native Project ID and name;
- canonical repository root;
- the derived sibling Organizer candidate;
- the exact currently visible native Workspaces and their repositories.

Director Engine derives the Director Project/Workspace identities and complete
`paseo-director.json`; the standard flow has no duplicate Project/path fields
and no raw JSON input. Review the generated configuration and ordered file/Git
operations in **Preview**, then press authenticated **Apply**. Any changed
native fact invalidates the Preview. Apply creates one local Organizer commit,
records the native Workspace bindings, and activates the Project idempotently.
The generated safe local profile uses manual launch, manual direct integration,
finite budgets, no provider fallback, and independent Review.

**Advanced existing-setup import** is a separate flow. Use it only for an
already prepared Organizer repository, including a Project configured for
GitHub pull-request delivery and its exact CI workflow/check identities. It
reads the committed configuration from that repository; it is not the normal
native-Project onboarding form.

## 3. Plan and launch

In the Project Board:

1. press **Create Epic**;
2. press **Create Task**, select one exact Workspace and optional Epic, and
   enter the objective and one acceptance criterion per line;
3. use **Add dependency** to select an exact Task or Epic;
4. resolve the prerequisite, or use only the engine-issued authenticated
   dependency-override confirmation when the Project owner intends to waive it;
5. press **Launch now**.

The launch action carries an engine-issued request, expected Task version, and
authenticated human session. The engine rereads Organizer, dependency, lease,
provider, repository/base, disk, budget, and capacity facts. Its scheduler
atomically reserves Project/Workspace/agent capacity and consumes one exact
lease-bound permit before creating a Run. A stale ticket or concurrent winner
is refused; it cannot create a second Run.

The engine creates an owned Git worktree and native Execution Workspace, then
creates one visible parentless Task Agent with only the zero-work bootstrap.
After the native identity and fixed scoped MCP session are durable, it sends
the real Task prompt as a separate notified effect. No Organizer or planning
agent becomes its parent.

## 4. Candidate, CI, Review, and delivery

The Task Agent commits in its owned worktree and submits one closed outcome.
The engine independently observes and admits the exact Candidate/base/tree,
cleanliness, ownership, Task version, configuration, profile, context, and
lease facts. Response loss is recovered from the immutable MCP Command and
Candidate observation; the effect is not repeated.

For direct delivery, Director runs the deterministic direct gate and launches
one independent parentless Reviewer in a disposable detached exact-SHA
checkout. For a pull-request Project, it creates or updates its one owned draft
under the exact ref lease, records the one configured authoritative remote
Linux CI observation, and supplies that same observation to Review. Review
never starts a second complete CI.

Manual policy exposes **Integrate exact Candidate** only when current Review,
CI, feedback, base/head, mergeability, and ownership facts permit it. Automatic
policy uses the same gates. PR integration atomically asserts the reviewed head
SHA; direct integration uses the exact target-ref lease. A PR failure never
falls back to direct delivery, and a direct failure never creates a PR.

## 5. Feedback, interruption, and cleanup

Use **Send feedback** on the Task execution tab. The server-authenticated human
comment is bound to the exact current Candidate and severity. Actionable
feedback invalidates downstream delivery authority and routes one bounded
correction turn back to the same Task Agent. A changed correction Candidate
must pass fresh CI and a fresh independent Review.

Terminal callbacks are durable wake signals for both Task Agent and Reviewer.
After engine/plugin interruption, startup rechecks TaskStore Commands, leases,
native agents/Workspaces, Git refs/worktrees, provider usage, and pending
delivery effects. It adopts only an exact existing effect; ambiguity becomes
Needs you. The five-minute compound watchdog is only for a genuinely lost
callback and requires unchanged agent, tool, worktree, and usage facts.

Integrated work follows the configured cleanup policy. Cancellation or dirty
work is snapshotted or retained before any removal. Cleanup can touch only
owned agents, Execution Workspaces, refs, worktrees, review checkouts, caches,
and recovery artifacts proven by the current Run; it never treats Needs you as
destruction authority. Use Project Operations and Doctor for bounded status,
reconciliation, backup/maintenance, and guarded repair. Public UI, logs,
TaskStore, Organizer Git, Beads, and evidence contain no secret, credential,
private path, or raw provider output.

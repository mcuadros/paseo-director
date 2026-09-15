# Example Organizer

This directory is a safe, minimal, end-to-end Organizer for a fictional
two-repository Project. It contains no credential, usable remote, private host
path, TaskStore data, or executable lifecycle command.

The fixture demonstrates:

- two Workspaces with distinct canonical repository identities;
- explicit Project defaults and a tightening Workspace override;
- separate planning, Task Agent, and Reviewer profiles;
- empty provider fallback chains;
- finite capacity, time, token, turn, cost, and CI budgets;
- manual launch and integration policy;
- explicit skill and template references;
- durable decision and specification material; and
- Task prerequisites, priorities, labels, objectives, and acceptance criteria
  through the task template and walkthrough below.

## Layout

```text
examples/organizer/
├── paseo-director.json
├── README.md
├── decisions/0001-manual-delivery.md
├── skills/commits/SKILL.md
├── specs/release-checklist.md
└── templates/task.md
```

Only `paseo-director.json` is the discovery/configuration document. Director
loads only explicitly referenced skills and templates; the decision and
specification are durable human context, not executable input. Dynamic Epics,
Tasks, dependencies, Runs, Candidates, and audit records belong to TaskStore
and are not imported from this Git directory.

## Adapt the fixture

1. Copy the directory into a new dedicated Git repository.
2. Replace `example-multi-repo` and its display name.
3. Replace both `.invalid` remotes with the real credential-free canonical
   remotes already configured in the source checkouts.
4. Replace both `/srv/director-example/...` paths with clean absolute Linux
   source-checkout paths. Do not use an Execution Workspace or symlink.
5. Select exact provider/model tuples which Doctor discovers and admits on the
   target host. Do not add a fallback unless every entry is independently
   admitted and human-approved.
6. Replace the example GitHub workflow and App IDs with the exact IDs observed
   for the repository, or remove `githubCi` when the selected supported
   delivery path does not use it.
7. Commit only public configuration and context. Keep credentials, TaskStore
   files, logs, caches, recovery artifacts, and support bundles outside Git.
8. In Director Home, choose **Advanced import**, inspect Preview, and press
   **Apply exact Preview** separately. Product repositories remain read-only
   during adoption.

## Example planning walkthrough

Create Epic `WEB-1`, **Ship the first web release**, with priority `high` and
labels `release` and `web`.

Create the backend prerequisite Task first:

- Key: `API-1`
- Workspace: `api`
- Title: `Expose the release metadata endpoint`
- Priority: `urgent`
- Labels: `api`, `release`
- Objective: expose bounded public release metadata without credentials or
  private paths.
- Acceptance: the endpoint returns the exact released version and source
  Candidate; unknown fields and credential-shaped output are rejected; focused
  tests pass.

Create the dependent web Task:

- Key: `WEB-2`
- Workspace: `web`
- Epic: `WEB-1`
- Title: `Show release metadata in the web application`
- Priority: `high`
- Labels: `frontend`, `release`
- Dependency: Task `API-1`
- Objective: render exact release metadata using the public API.
- Acceptance: loading, success, unavailable, and stale states are accessible;
  no credential or private path is rendered; focused tests pass.

The dependency must keep `WEB-2` Queued until `API-1` is complete. Review the
effective Project → Workspace → Task configuration before **Launch now**.
Director freezes it into each Run; later Organizer edits affect only future
Runs after a separate Preview/Apply.

## Safety notes

The sample uses manual launch, pull-request delivery, manual integration,
disabled early draft publication, `snapshot_then_delete`, and seven-day
recovery retention. The `api` override tightens concurrency, disables automatic
correction, and selects `retain` for cancellation. It does not expand the
Project envelope.

The standard **Create Project** path does not require this fixture: it selects a
native Paseo Project and generates safe local configuration. The schema-valid
provider/model values here are illustrative advanced-import compatibility points,
not permission to skip fresh Doctor discovery. The `.invalid` remotes cannot be
used accidentally, and the `/srv/director-example` paths are public fixture
values rather than a real deployment location.

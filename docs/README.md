# Director documentation

Director for Paseo is an independent community plugin for coordinating
multi-repository software work. Start with the guide for your role:

- [Getting started](getting-started.md) covers the exact supported host,
  installation, restart-free activation, native-Project setup, and first Task.
- [User guide](user-guide.md) follows work through Board/List, Task detail,
  execution, Review, delivery, feedback, recovery, and cleanup.
- [Operator guide](operator-guide.md) covers runtime health, diagnostics,
  synchronization, TaskStore maintenance, retention, logs, and support bundles.
- [Security boundaries](security-boundaries.md) explains handler-scoped Paseo
  authority, credentials, rootless OCI, human/P2 gates, redaction, and
  destructive safeguards.
- [Developer guide](developer-guide.md) explains the standalone Go engine,
  generated contracts and APIs, local checks, and contribution workflow.
- [Example Organizer](../examples/organizer/README.md) is a schema-validated
  advanced-import example for two repositories.

The supported production walkthrough is
[Production onboarding and workflow](production-onboarding.md). Detailed
references cover [Organizer configuration](configuration.md),
[Board/List](board-list.md), [Task detail and Inspector](task-modal-inspector.md),
[operations and diagnostics](operations-diagnostics.md), and
[local recovery and cleanup](local-remote-cleanup.md).

Public governance and release material is maintained separately:

- [Licensing and third-party notices](licensing.md)
- [Security policy](../SECURITY.md)
- [Support policy](../SUPPORT.md)
- [Compatibility policy](compatibility.md)
- [Changelog](../CHANGELOG.md)
- [Release process](release-process.md)

## Supported boundary

Director 1.0 supports Linux amd64 with glibc and exact stable Paseo `0.7.2`
only. Another `0.7.x` release is not inferred compatible, and Paseo `0.8`
preview is not a production target. No tagged alpha, beta, or stable channel
has been published yet; default `main` is the explicit source-testing channel.

Never work around a compatibility, integrity, ownership, or health refusal by
editing Paseo state, using a private API, changing channels implicitly, or
stopping or restarting the Paseo daemon or machine. Use only the documented
plugin-scoped reload/update and the exact Doctor, Repair, or Needs-you action.

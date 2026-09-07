# Director for Paseo

Director is standalone Go software for planning, executing, reviewing, and delivering medium-to-large software products that span one or more repositories. It is usable and publishable without Paseo. Director for Paseo is its public Paseo host package, containing the complete planned React Native UI, the minimum policy-free connector, and other Paseo-only integration.

The project is currently in its evidence-gathering phase. Product implementation does not begin until the M0 gates in the approved plan are satisfied.

## Paseo 0.7.2 deployment authority

> **Read before installation:** on exact Paseo 0.7.2, Director for Paseo
> requires a full daemon-operator credential. Install only if you accept that
> the trusted connector can invoke the daemon user’s complete public API.

The accepted deployment boundary requires all of the following:

- Credential material lives outside repository and Paseo-managed plugin
  checkouts and is readable only by the Director for Paseo connector process.
- Credential bytes and location never enter Director Engine arguments,
  environment, inherited file descriptors, protocol events, UI, TaskStore,
  projections, support bundles, logs, or timeline records.
- Connector startup and reload fail closed when the credential is absent or
  empty; they never select a fallback authority.
- Before handling a command, the connector reports
  `credentialScope=full-daemon-operator`, its contract version and contract hash,
  and only its fixed capabilities. Missing or stale values fail closed.

This acceptance applies only to exact Paseo 0.7.2. Director will narrow the
connector authority when Paseo exposes and Director verifies a supported
headless connector-scoped credential or equivalently scoped startup-injected
API. That change will not move workflow logic out of the standalone Go engine.

Director Engine remains independently usable and publishable Go software and
owns all workflow truth and decisions. Director for Paseo retains the UI and
policy-free host connector; every Paseo call crosses the single engine-owned
host interface. Release deployments download the pinned engine binary,
development explicitly compiles it, independent review is always required,
and an in-process TypeScript engine is prohibited.

See [ADR-0017](docs/adr/0017-standalone-engine-connector-authority-boundary.md).

- [Approved product and engineering plan](docs/PLAN.md)
- [Contributing workflow](CONTRIBUTING.md)

Development is tracked with Beads. Run `bd ready` to see currently unblocked work.

# Missing-host-dependency provider classification correction

This Task corrects the classification divergence discovered by finding
`DIR-M5.13-F001` in the real incident conformance corpus.

## Root cause and resolution

In M5.13, the published bubblewrap incident excerpt:

```text
Codex could not find bubblewrap on PATH. Install bubblewrap with your OS package manager. See the sandbox prerequisites
```

was classified as `policy_rejection` (leading to `terminal_policy`) solely
because the excerpt contained the substring `sandbox`. The true host cause is
a missing host dependency/prerequisite (`bubblewrap` binary missing on `PATH`).

This change implements deterministic precedence for missing host prerequisite
facts:
1. **Missing host prerequisite detection**: Structured phrases indicating a
   missing host dependency or binary (such as `could not find \S+ on path`,
   `install \S+ with your (?:os )?package manager`, `missing (?:host )?(?:dependency|prerequisite)`,
   and `(?:host|sandbox) prerequisites?`) are classified under
   `configuration_rejection`.
2. **Deterministic precedence**: A missing host prerequisite takes precedence
   over bare `sandbox` keyword matches, preventing environmental prerequisites
   from being misattributed to policy violations.
3. **Closed vocabulary preserved**: No new failure signals or classes are
   introduced. `HostProviderFailureSignal` and `ProviderFailureClass` maintain
   their closed enum schemas.
4. **No policy default changes**: Default recovery policy retains
   `ReplaceTerminalConfiguration = false`. Missing host prerequisites produce
   `terminal_configuration` and resolve to `needs_you` with bounded reason
   `primary_recovery_failure_requires_human`.
5. **Fail closed on contradiction/unknown**: Contradictory error bodies (such
   as simultaneous authentication and configuration signals, or policy and
   configuration signals) emit multiple distinct signals that the engine
   classifies as `FailureClassAmbiguous`, halting automated replacement.
   Unrecognized prose fails closed as `unclassified` -> `FailureClassAmbiguous`.

## Findings update

- `DIR-M5.13-F001`: Marked `behaviorChanged: true`. The bubblewrap incidents
  `DIR-M5.13-INCIDENT-APP-SERVER-01` and `DIR-M5.13-INCIDENT-APP-SERVER-02` now
  emit `configuration_rejection` in connector and `terminal_configuration` in
  the standalone engine.
- `DIR-M5.13-F002`: Remains `behaviorChanged: false` pending `dir-m5.16`
  (evidence-backed terminal adoption).

## Validation proof

Focused validation commands:

```sh
npm run check:real-incident-corpus
node --experimental-strip-types --test tests/primary-host-connector.test.ts
GOCACHE=/tmp/dir-m5-15-go-cache go -C engine test ./domain/execution
npm run typecheck
npm run format:check
```

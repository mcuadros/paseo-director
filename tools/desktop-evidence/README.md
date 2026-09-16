# Desktop evidence capture — the capture is local only, its tests are not

`capture.mjs` drives the **real shipped Paseo desktop application** — the
packaged Electron binary from a GitHub release — on a display it creates and
destroys itself, opens the Director surface, and records what the real client
bundle does with it.

It exists because one Director verification question cannot be answered any
other way. The client bundle executed by the desktop application is a different
build from the web-UI bundle the daemon serves, so checking a Director surface
against the daemon can report a pass on a client that actually fails. Only the
real application answers that question.

## What is in CI and what is not

The distinction is precise, and getting it wrong in either direction is a bug:

- **The capture cannot run in CI.** Nothing in `npm run ci` launches it, and
  nothing should. `test:activation:real` remains retired and must not be revived
  to host it.
- **Its decision tests can, and do.** `npm run test:desktop-evidence` runs
  `capture.test.mjs` and **is part of `npm run ci`**. Those tests are
  dependency-free — `node:` builtins and the module under test only — and
  complete in well under a second with no dependency install, no X server, no
  daemon and no Playwright.

That second point is load-bearing. The safety rules below are the justification
for keeping an operator tool in the repository, so they are covered by tests
that a green build actually depends on.

Coverage here is claimed against a specific standard, because this tool has
repeatedly been wrong about it: **a mutation proves coverage only where a
property is enforced, not where it is defined.** Mutating a pure predicate
tests that predicate's own tests; the claim is almost always about the call
site. So every decision below lives in an exported unit with its effects
injected, and each property is verified by breaking the line that enforces it
and confirming the suite fails.

What that covers, and what it does not:

- **Decisions** — display selection and adoption, teardown target selection and
  escalation, the spawn-time identity pin, environment filtering, isolation
  verification, artifact construction and the surface assertions — are covered
  behaviourally at their enforcement sites.
- **The composition root** (`main`) can only be executed with an X server, a
  daemon and Playwright, which is why the capture is not in CI. Its decisions
  have been moved out into those units, so what remains is wiring, and wiring
  fails by omission. That is covered by an explicit wiring contract asserting
  `main` still calls each unit. It is a wiring assertion, not a behavioural
  one: it proves the calls are present, not that they are correct. The tests
  above prove the latter.

The capture itself cannot run on the GitHub Linux runner because it needs all of
the following on the machine executing it:

- a local X server (`Xvfb`) and permission to create a display;
- a Paseo daemon already running and reachable, with whatever plugin build is
  under test already installed in it;
- a Playwright installation that this repository does not and will not declare
  as a dependency, supplied through `PASEO_DESKTOP_PLAYWRIGHT`;
- roughly 300 MB of GitHub release downloads, one per desktop build compared.

Those are properties of an operator's workstation, not of a build runner. The
capture is therefore run deliberately, and its result is evidence attached to a
task rather than a build status.

## What it guarantees about the machine it runs on

- **Every resource is owned from the moment it exists, not once its creator
  returns.** The X server is handed to teardown the instant it is spawned, while
  its socket and lock are still appearing; the application's identity pin is
  handed over from inside the spawn, before the call resolves; and the
  interruption handlers are installed before anything is created at all. A
  resource that exists before its owner can see it is a resource an interruption
  orphans. The display, the application and the run's directories are handed
  over at creation; the Playwright connection is registered at the promise
  boundary instead, because Playwright owns the connect, so that one window is
  narrowed rather than closed — microseconds, and a socket rather than a
  process.
- **Interruption reaches teardown, and termination does not depend on a list.**
  Every signal whose default disposition ends this process is either handled or
  carries a recorded reason for not being — `SIGHUP` matters most, because a
  closing terminal or a dropped ssh session sends it during a capture that runs
  for minutes. `SIGKILL` and `SIGSTOP` cannot be caught by anything, and that is
  stated rather than papered over. Because no list is ever complete, a
  synchronous `exit` fallback runs one best-effort pass for every termination
  path that reaches process exit.
- **It does not leak the credential into its own children.** The application's
  environment is filtered, and the X server is given an explicit minimal
  allowlist rather than the ambient environment. The isolation check reads each
  spawned child's `/proc/<pid>/environ` and fails the run if either namespace
  reached it, because a check that passes on a leaking child is worth less than
  no check.
- **It only stops the processes it started.** The spawned process's identity is
  pinned immediately after spawn by reading its start time from `/proc`, and
  teardown signals nothing at all unless that pin still matches a live process.
  A dead or recycled root authorises no signal, and because descendants are
  derived from a confirmed root, a wrong root cannot select anything beneath it
  either. A descendant recorded earlier stays in scope after the kernel
  reparents it, but only while its own recorded start time still matches — and
  it stays in scope after the application itself has exited, because its proof
  does not depend on the root being alive. That matters: the bundled daemon
  outlives the application, so gating it on root liveness would abandon exactly
  the process this scan exists to catch. Identity is re-proved before every
  signal, not only when targets are chosen, because signalling frees process
  ids. When no application was spawned, teardown signals nothing. It deliberately does
  **not** select by executable path: `--app` is operator-supplied and has no
  floor, so a path prefix could match unrelated processes.

  It also deliberately does **not** signal a process group. The application
  spawns its bundled daemon with `setsid()`, so that daemon leaves both our
  process group and our session — measured on a shipped 0.7.2 build, its process
  group and session id both differ from the application's. Signalling a negative
  process group id would silently stop cleaning up the one process the scan
  exists to catch.
- **It owns its display, proves it, and removes it.** The display is claimed by
  racing the X server's own `O_EXCL` lock, so a number another process is using
  is skipped rather than stolen. The X lock file records the server's process
  id, so ownership is checked rather than assumed: a display is adopted only
  when its lock names our own server, and its socket and lock are removed only
  while that is still true. Numbers `:0`, `:1`, `:2` and `:8` are refused inside the
  allocator itself — not via an overridable parameter — because `:8` is a
  physical console on the machine this was written for, never a test fixture.
  The default search range starts at `:120`.
- **It owns its Electron profile and its daemon home, removes them, and checks
  both were honoured.** The application gets a private user-data directory and a
  private `PASEO_HOME`. Because those overrides fail *silently* if a build
  ignores them — and an ignored `PASEO_HOME` would point the bundled daemon at
  the operator's own — the run aborts unless the application actually wrote into
  **both** private directories, so an unisolated run cannot be mistaken for an
  isolated one.
- **It will not send a credential off the machine, and it opens nothing others
  can reach.** `--daemon-host` must be a loopback address: the password is typed
  into the connection form, so an arbitrary remote target would send both the
  credential and this host's identity away, and there is no opt-out flag. The
  bundled daemon it starts and the debugging endpoint it opens are likewise
  loopback-only, and the run's private directories are created owner-only.
- **It drops the ambient Paseo and Director environment.** Every `PASEO_*` and `DIRECTOR_*`
  variable is removed from the application environment before the six this tool
  sets, so a secret added to either namespace in future is excluded without
  editing a list. Other ambient variables — `PATH`, `HOME` and the rest — are
  inherited deliberately, because the application needs them; this is a filter
  on those two namespaces, not a general credential scrub. A daemon password,
  when one is needed, is read from this process's environment and typed into
  the connection form over the debugging channel.
- **A secret cannot survive in any emitted file.** Application output is
  buffered raw and scrubbed once when the file is written, never per chunk,
  because a secret split across an arbitrary chunk boundary would survive
  per-chunk scrubbing. Every artifact the run emits — the result document, the
  transcript and the application log — is scrubbed at the point it is built, so
  the property does not depend on a caller having scrubbed upstream.
- **It will not photograph a password.** Before the connection form is captured,
  the password field is asserted to be masked. If a build renders it as plain
  text the run fails instead of writing the screenshot.

  **The credential guarantees above have never been exercised end to end.**
  Every capture run so far has connected to a passwordless isolated daemon, so
  `credentialUsed` is `false` in every evidence manifest on every Candidate, and
  the first real run against a password-protected daemon will be the first time
  that path executes. What *is* covered by the test suite, and therefore by CI,
  is the decision logic those guarantees are made of: that the password field is
  asserted masked before the connection form is captured, that every emitted
  artifact is scrubbed where it is built including a secret split across stream
  chunks, that `--daemon-host` is floored to loopback during argument parsing,
  and that neither child process inherits anything this tool did not set. What
  is not covered is those parts working together against a real daemon that
  actually demands a password. Read the guarantees as well-tested intent, not as
  observed behaviour.

- **It does not weaken the application.** Remote debugging is enabled through the
  application's own shipped `PASEO_ELECTRON_FLAGS` channel rather than by
  appending to its argv, so the application's CLI parsing is untouched and the
  Chromium sandbox stays enabled. Playwright's own Electron launcher is not used
  because it injects `--inspect`, which the application rejects, and forces
  `--no-sandbox`.
- **Interruption cleans up too.** `SIGINT` and `SIGTERM` run the same teardown as
  a normal or failed exit, so Ctrl-C does not leave a display, lock, profile or
  process behind.
- **It connects, it does not pair.** Only the in-app Direct connection form is
  used. No external pairing offer is created and no daemon or server identity
  leaves the machine.

## Running it

Point `PASEO_DESKTOP_PLAYWRIGHT` at an existing Playwright package directory,
have a daemon running with the plugin build under test installed, then:

```console
$ export PASEO_DESKTOP_PLAYWRIGHT=/path/to/node_modules/playwright
$ node tools/desktop-evidence/capture.mjs \
    --app /path/to/Paseo-0.7.2-x64 \
    --daemon-host 127.0.0.1 --daemon-port 44613 \
    --label 0.7.2 --out /path/to/evidence
```

`--app` accepts the extracted release directory or the executable inside it.
`--label` must match `[A-Za-z0-9._-]+`, because it names output files and a
private run directory that is deleted recursively. `--daemon-host` must be a
loopback address and defaults to `127.0.0.1`; pass `--daemon-port` for the
daemon to connect to, and `PASEO_PASSWORD` in the environment when that daemon
requires one. `--display-min` and `--display-max` bound the display search,
which defaults to `:120` through `:199`.

`--keep-user-data` is the one flag that opts out of a guarantee above: it
retains the private run directory, including the Electron profile and the
private `PASEO_HOME`, after the run. Use it only to inspect a failure, and
delete the directory yourself afterwards. Everything else — the display, its
lock and every process — is still removed.

The run writes, into `--out`: a screenshot per step, `<label>-console.log` with
every renderer message, `<label>-app-main.log` with the application's own output,
and `<label>-result.json`:

```json
{
  "label": "0.7.2",
  "userAgent": "Paseo/0.7.2 Chrome/146.0.7680.179 Electron/41.2.0",
  "display": ":120",
  "directorSidebarTestId": "plugin-sidebar-director-home",
  "reducedMotionActive": true,
  "fallbackWarnings": 0,
  "rendererErrorsTotal": 0,
  "rendererErrorsBeforeProbe": 0,
  "rendererErrorsDuringProbe": 0,
  "react130": false
}
```

## What the capture actually exercises

After connecting, it opens the Director surface and waits for the engine
projection to resolve. It then emulates a reduced-motion preference and reopens
the surface.

That last step matters. Director Home renders the optional `Icon` host primitive
from `@getpaseo/plugin` in many places, but nearly all of them require a resolved
engine snapshot. The reduced-motion branch of the loading state renders `Icon`
directly, without a snapshot, so it is the one path that exercises the primitive
on a host where the engine projection cannot resolve — for example when another
Director engine already holds the fixed runtime ports and must not be disturbed.

The run fails if reduced motion did not actually take effect, so the probe cannot
silently degrade into a second ordinary capture. `reducedMotionActive` and
`fallbackWarnings` in the result record whether the primitive was reached and how
often a build reported rendering a defined fallback instead.

A run that never resolves a snapshot proves the primitive resolves or fails, but
does **not** cover every call site. Say which of the two a given result is when
reporting it.

## Tests

```console
$ npm run test:desktop-evidence
```

#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
//
// Tests for the decision logic of the local-only desktop capture tool. These
// cover the parts that protect the machine it runs on — display selection,
// teardown target selection and identity confirmation, environment isolation,
// loopback floor, label validation, secret scrubbing and result metrics.
//
// They are dependency-free: node: builtins and the module under test only, with
// no X server, no daemon, no Playwright and no download. That is precisely why
// they ARE part of `npm run ci` even though the capture itself cannot be. See
// ./README.md.

import assert from "node:assert/strict";
import osConstants from "node:os";
import { test } from "node:test";
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join as joinPath } from "node:path";

import {
  DEFAULT_DISPLAY_RANGE,
  LABEL_PATTERN,
  RESERVED_DISPLAY_NUMBERS,
  applicationEnvironment,
  assertLoopbackHost,
  buildRunArtifacts,
  claimDisplay,
  confirmedTeardownTargets,
  createTeardown,
  descendantPids,
  displayCandidates,
  driveDirectorSurface,
  DISPLAY_SERVER_ENVIRONMENT_NAMES,
  SIGNAL_POLICY,
  TERMINATING_SIGNALS,
  UNHANDLED_TERMINATING_SIGNALS,
  ENVIRONMENT_UNREADABLE,
  childEnvironmentTargets,
  readChildEnvironment,
  platformSignals,
  unclassifiedSignals,
  displayServerEnvironment,
  displayServerSpawnOptions,
  installExitFallback,
  installSignalHandlers,
  isolationTargets,
  intendedApplicationEnvironmentNames,
  unexpectedEnvironmentNames,
  pinnedRoot,
  verifyChildEnvironments,
  displayLockOwnedBy,
  occupiedDisplayNumbers,
  parseArguments,
  parseDisplayLockPid,
  parseProcessStartTime,
  processStartTime,
  resolveApplicationExecutable,
  rootIsConfirmed,
  PRIVATE_DIRECTORY_MODE,
  loopbackDebuggingEndpoint,
  loopbackListen,
  runDirectoryLayout,
  runPassword,
  scrubSecret,
  spawnApplication,
  stripComments,
  summarizeRendererEvents,
  survivingTargets,
  terminateTargets,
  verifyIsolation,
  verifyRunIsolation,
  validateLabel,
} from "./capture.mjs";

const pidsOf = (targets) => targets.map((target) => target.pid);

/** A process-table snapshot of the shape readProcessTable() produces. */
function processTable(entries) {
  return {
    parentByPid: new Map(entries.map(([pid, parent]) => [pid, parent])),
    startTimeByPid: new Map(entries.map(([pid, , startTime]) => [pid, startTime])),
  };
}

// --- display selection -----------------------------------------------------

test("reserved displays are never offered as candidates", () => {
  const candidates = displayCandidates({ minimum: 0, maximum: 20 });
  for (const reserved of RESERVED_DISPLAY_NUMBERS) {
    assert.equal(candidates.includes(reserved), false, `display :${reserved} must never be selected`);
  }
});

test("the physical console :8 stays refused even when it looks free", () => {
  assert.deepEqual(displayCandidates({ minimum: 8, maximum: 8, occupied: new Set() }), []);
});

test("the reserved list is frozen, so it cannot be widened at runtime", () => {
  // A caller cannot narrow the refusal by mutating the exported list: it is a
  // module-level frozen constant read inside the allocator, not a parameter.
  assert.equal(Object.isFrozen(RESERVED_DISPLAY_NUMBERS), true);
  assert.throws(() => RESERVED_DISPLAY_NUMBERS.push(999), TypeError);
  assert.throws(() => { RESERVED_DISPLAY_NUMBERS[0] = 999; }, TypeError);
  assert.deepEqual([...RESERVED_DISPLAY_NUMBERS], [0, 1, 2, 8]);
});

test("a supplied reserved list is ignored by the selection path", () => {
  // Passing the property must have no effect, through the same call the
  // allocator makes, across a range that spans every reserved number.
  const candidates = displayCandidates({ minimum: 0, maximum: 10, reserved: [], occupied: new Set() });
  for (const reserved of RESERVED_DISPLAY_NUMBERS) assert.equal(candidates.includes(reserved), false);
  assert.deepEqual(candidates, [3, 4, 5, 6, 7, 9, 10]);
});

test("displays already in use are skipped", () => {
  assert.deepEqual(displayCandidates({ minimum: 120, maximum: 124, occupied: new Set([120, 122]) }), [121, 123, 124]);
});

test("the default range sits outside every reserved display", () => {
  for (const reserved of RESERVED_DISPLAY_NUMBERS) {
    assert.equal(reserved < DEFAULT_DISPLAY_RANGE.minimum, true);
  }
});

test("an inverted display range is rejected", () => {
  assert.throws(() => displayCandidates({ minimum: 40, maximum: 10 }), /valid integer interval/u);
});

test("occupied display numbers are read from sockets and locks", () => {
  assert.equal(occupiedDisplayNumbers("/nonexistent-socket-dir", "/nonexistent-lock-dir").size, 0);
});

// --- teardown identity and target selection --------------------------------

test("a root is confirmed only when its pinned start time still matches", () => {
  const table = processTable([[100, 1, 5000]]);
  assert.equal(rootIsConfirmed({ pid: 100, startTime: 5000, exited: false }, table), true);
  assert.equal(rootIsConfirmed({ pid: 100, startTime: 4999, exited: false }, table), false);
  assert.equal(rootIsConfirmed({ pid: 100, startTime: 5000, exited: true }, table), false);
  assert.equal(rootIsConfirmed({ pid: 101, startTime: 5000, exited: false }, table), false);
  assert.equal(rootIsConfirmed({ pid: 100, startTime: null, exited: false }, table), false);
  assert.equal(rootIsConfirmed(null, table), false);
});

test("REGRESSION: a recycled root pid authorises no signal at all", () => {
  // The defect this replaces recorded the root's start time at cleanup from the
  // same snapshot it then compared against, so the check was a value against
  // itself and always passed. With the pid reused by an unrelated process, the
  // old shape selected that process and its children; the pinned start time
  // recorded at spawn makes the mismatch visible instead.
  const pinnedAtSpawn = { pid: 100, startTime: 5000, exited: false };
  const afterPidReuse = processTable([[100, 1, 9999], [200, 100, 9999], [300, 200, 9999], [400, 1, 9999]]);
  assert.equal(rootIsConfirmed(pinnedAtSpawn, afterPidReuse), false);
  assert.deepEqual(confirmedTeardownTargets({ root: pinnedAtSpawn, processTable: afterPidReuse }), []);
});

test("an exited root authorises neither itself nor anything under the live parent map", () => {
  // A dead or recycled root proves nothing about its apparent children, so
  // parent-map descendants lose their authorisation with it.
  const table = processTable([[100, 1, 5000], [200, 100, 5000]]);
  const exited = { pid: 100, startTime: 5000, exited: true };
  assert.deepEqual(confirmedTeardownTargets({ root: exited, processTable: table }), []);
});

test("REGRESSION: a tracked descendant survives the root and is still signalled", () => {
  // The bundled daemon is spawned with setsid(), so it outlives the
  // application. It was recorded only while the root was confirmed and carries
  // its own identity, so it proves itself; gating it on root liveness abandoned
  // exactly the process this scan exists to catch, while the private PASEO_HOME
  // underneath it was deleted.
  const reparented = processTable([[250, 1, 7000]]);
  const tracked = new Map([[250, 7000]]);
  for (const root of [
    { pid: 100, startTime: 5000, exited: true },
    { pid: 100, startTime: 5000, exited: false },
    null,
  ]) {
    assert.deepEqual(
      pidsOf(confirmedTeardownTargets({ root, tracked, processTable: reparented, selfPid: 999 })),
      [250],
      "a self-proving descendant must not depend on the root",
    );
  }
});

test("a tracked descendant whose pid was reused is still refused after the root is gone", () => {
  const reparented = processTable([[250, 1, 7000]]);
  assert.deepEqual(
    confirmedTeardownTargets({
      root: { pid: 100, startTime: 5000, exited: true },
      tracked: new Map([[250, 1234]]),
      processTable: reparented,
    }),
    [],
  );
});

test("every target carries the identity that authorised it", () => {
  const table = processTable([[100, 1, 5000], [200, 100, 5001]]);
  const targets = confirmedTeardownTargets({
    root: { pid: 100, startTime: 5000, exited: false }, processTable: table, selfPid: 999,
  });
  assert.deepEqual(targets, [{ pid: 100, startTime: 5000 }, { pid: 200, startTime: 5001 }]);
});

test("a confirmed root brings its transitive descendants", () => {
  const table = processTable([[100, 1, 5000], [200, 100, 5001], [300, 200, 5002], [400, 1, 5003]]);
  const root = { pid: 100, startTime: 5000, exited: false };
  assert.deepEqual([...descendantPids(100, table.parentByPid)].sort((a, b) => a - b), [200, 300]);
  assert.deepEqual(pidsOf(confirmedTeardownTargets({ root, processTable: table, selfPid: 999 })), [100, 200, 300]);
});

test("an unrelated process tree is never selected", () => {
  const table = processTable([[100, 1, 5000], [200, 100, 5001], [500, 1, 5002], [600, 500, 5003]]);
  const targets = pidsOf(confirmedTeardownTargets({
    root: { pid: 100, startTime: 5000, exited: false }, processTable: table, selfPid: 999,
  }));
  assert.equal(targets.includes(500), false);
  assert.equal(targets.includes(600), false);
});

test("nothing is selected when no application was ever spawned", () => {
  // The cleanup path always runs, including when the launch failed before any
  // process existed. It must then have an empty target set.
  const table = processTable([[100, 1, 5000], [200, 100, 5001]]);
  for (const root of [null, undefined, { pid: 0, startTime: 1 }, { pid: 1, startTime: 1 }]) {
    assert.deepEqual(confirmedTeardownTargets({ root, processTable: table }), []);
  }
});

test("this process and an init process are never selected", () => {
  const table = processTable([[100, 1, 5000], [777, 100, 5001], [1, 0, 1]]);
  const targets = pidsOf(confirmedTeardownTargets({
    root: { pid: 100, startTime: 5000, exited: false }, processTable: table, selfPid: 777,
  }));
  assert.equal(targets.includes(777), false);
  assert.equal(targets.includes(1), false);
});

test("a reparented descendant stays in scope only while its identity matches", () => {
  // The bundled daemon is spawned with setsid(); once the application exits the
  // kernel reparents it and it is no longer reachable through the parent map.
  const table = processTable([[100, 1, 5000], [250, 1, 7000], [260, 1, 8000]]);
  const root = { pid: 100, startTime: 5000, exited: false };
  const tracked = new Map([[250, 7000], [260, 1234]]);
  const targets = pidsOf(confirmedTeardownTargets({ root, tracked, processTable: table, selfPid: 999 }));
  assert.equal(targets.includes(250), true, "recorded identity still matches");
  assert.equal(targets.includes(260), false, "pid reused since it was recorded");
});

test("process start time is parsed past a command name containing spaces and parentheses", () => {
  const fields = Array.from({ length: 50 }, (unused, index) => String(index + 3));
  // Each token's value is its own field number, so a correct parse of field 22
  // returns 22. The command name deliberately contains a space and a nested
  // parenthesis, which is why parsing starts after the LAST closing one.
  const stat = `494732 (Paseo Supervisor (x)) S ${fields.slice(1).join(" ")}`;
  assert.equal(parseProcessStartTime(stat), 22);
  assert.equal(parseProcessStartTime("malformed"), null);
  assert.equal(parseProcessStartTime(undefined), null);
});

// --- environment, credential and input floors ------------------------------

test("the application environment drops every PASEO_ and DIRECTOR_ variable", () => {
  const environment = applicationEnvironment(
    {
      PASEO_PASSWORD: "super-secret",
      PASEO_HOST: "tcp://example:6767?password=super-secret",
      PASEO_HOME: "/home/someone/.paseo",
      PASEO_SOMETHING_ADDED_LATER: "also-secret",
      DIRECTOR_PASEO_CREDENTIAL_FILE: "/run/credential",
      DIRECTOR_ANYTHING_ADDED_LATER: "also-secret",
      PATH: "/usr/bin",
    },
    { display: ":120", paseoHome: "/tmp/run/home", userDataDir: "/tmp/run/profile", listen: "127.0.0.1:1", cdpPort: 2 },
  );
  assert.equal(environment.PASEO_PASSWORD, undefined);
  assert.equal(environment.PASEO_HOST, undefined);
  assert.equal(environment.PASEO_SOMETHING_ADDED_LATER, undefined);
  assert.equal(environment.DIRECTOR_PASEO_CREDENTIAL_FILE, undefined);
  assert.equal(environment.DIRECTOR_ANYTHING_ADDED_LATER, undefined);
  assert.equal(environment.PATH, "/usr/bin");
});

test("the application environment forces private isolation", () => {
  const environment = applicationEnvironment(
    { PASEO_HOME: "/home/someone/.paseo" },
    { display: ":137", paseoHome: "/tmp/run/home", userDataDir: "/tmp/run/profile", listen: "127.0.0.1:44613", cdpPort: 9333 },
  );
  assert.equal(environment.DISPLAY, ":137");
  assert.equal(environment.PASEO_HOME, "/tmp/run/home");
  assert.equal(environment.PASEO_ELECTRON_USER_DATA_DIR, "/tmp/run/profile");
  assert.equal(environment.PASEO_LISTEN, "127.0.0.1:44613");
  assert.equal(environment.PASEO_DISABLE_SINGLE_INSTANCE_LOCK, "1");
  assert.equal(environment.PASEO_ELECTRON_FLAGS, "--remote-debugging-port=9333");
});

test("the daemon host is floored to loopback, because a password is typed into the form", () => {
  for (const host of ["127.0.0.1", "127.1.2.3", "localhost", "::1", "[::1]", "LocalHost"]) {
    assert.equal(typeof assertLoopbackHost(host), "string");
  }
  for (const host of ["10.0.0.1", "198.51.100.7", "example.com", "0.0.0.0", "127.0.0.256", "", undefined]) {
    assert.throws(() => assertLoopbackHost(host), /must be a loopback address/u, `${host} must be refused`);
  }
});

test("a non-loopback daemon host is refused while parsing, before anything is typed", () => {
  assert.throws(
    () => parseArguments(["--app", "/tmp/app", "--out", "/tmp/out", "--daemon-port", "1", "--daemon-host", "10.0.0.1"]),
    /must be a loopback address/u,
  );
});

test("secrets are scrubbed from every occurrence", () => {
  assert.equal(scrubSecret("a hunter2 b hunter2", "hunter2"), "a <redacted> b <redacted>");
  assert.equal(scrubSecret("nothing to hide", ""), "nothing to hide");
  assert.equal(scrubSecret("nothing to hide", undefined), "nothing to hide");
});

test("a secret split across stream chunks is scrubbed once the chunks are joined", () => {
  // Chunk boundaries are arbitrary; scrubbing each chunk on arrival would leave
  // this secret intact in the durable log.
  const chunks = ["prefix hun", "ter2 suffix"];
  assert.equal(chunks.map((chunk) => scrubSecret(chunk, "hunter2")).join("").includes("hunter2"), true);
  assert.equal(scrubSecret(chunks.join(""), "hunter2"), "prefix <redacted> suffix");
});

// --- result metrics --------------------------------------------------------

test("metrics count renderer observations only, never captured page text", () => {
  // Surface text routinely contains the very strings the metrics look for; a
  // narrative line must not be able to inflate the tool's product.
  const events = [
    { kind: "console", type: "error", text: "Minified React error #130" },
    { kind: "pageerror", type: "error", text: "boom" },
    { kind: "console", type: "warning", text: "[Director] Optional host primitive Icon is unavailable" },
    { kind: "console", type: "log", text: "ordinary" },
  ];
  const summary = summarizeRendererEvents(events);
  assert.equal(summary.rendererErrorsTotal, 2);
  assert.equal(summary.react130, true);
  assert.equal(summary.fallbackWarnings, 1);
  assert.deepEqual(summarizeRendererEvents([]), {
    rendererErrorsTotal: 0, react130: false, fallbackWarnings: 0,
  });
});

// --- arguments -------------------------------------------------------------

test("labels that could escape the output directory are refused", () => {
  for (const label of ["../../..", "../evil", ".", "..", "with/slash", "", undefined]) {
    assert.throws(() => validateLabel(label), /--label must match/u, `${label} must be refused`);
  }
});

test("ordinary labels are accepted", () => {
  for (const label of ["0.7.2", "0.6.1", "capture", "run_1", "a-b.c"]) {
    assert.equal(validateLabel(label), label);
    assert.match(label, LABEL_PATTERN);
  }
});

test("a traversing label is refused while parsing, before any delete", () => {
  assert.throws(
    () => parseArguments(["--app", "/tmp/app", "--out", "/tmp/out", "--daemon-port", "1", "--label", "../../.."]),
    /--label must match/u,
  );
});

test("arguments parse into an explicit plan", () => {
  const options = parseArguments([
    "--app", "/tmp/Paseo-0.6.1-x64",
    "--out", "/tmp/evidence",
    "--label", "0.6.1",
    "--daemon-port", "44613",
    "--display-min", "130",
    "--display-max", "140",
  ]);
  assert.equal(options.app, "/tmp/Paseo-0.6.1-x64");
  assert.equal(options.label, "0.6.1");
  assert.equal(options.daemonHost, "127.0.0.1");
  assert.equal(options.daemonPort, 44_613);
  assert.equal(options.displayMinimum, 130);
  assert.equal(options.displayMaximum, 140);
  assert.equal(options.keepUserData, false);
});

test("missing required arguments are refused", () => {
  assert.throws(() => parseArguments(["--app", "/tmp/app"]), /required/u);
  assert.throws(() => parseArguments(["--app", "/tmp/app", "--out", "/tmp/out"]), /daemon-port is required/u);
});

test("malformed arguments are refused", () => {
  assert.throws(() => parseArguments(["--nope", "1"]), /unknown argument/u);
  assert.throws(() => parseArguments(["--app"]), /requires a value/u);
  assert.throws(
    () => parseArguments(["--app", "a", "--out", "b", "--daemon-port", "not-a-port"]),
    /requires an integer/u,
  );
});

test("a missing application path is refused before anything is launched", () => {
  assert.throws(() => resolveApplicationExecutable("/nonexistent/Paseo-0.0.0-x64"), /does not exist/u);
});

// --- the signal site: identity re-proved every round ------------------------

test("survivingTargets drops a target whose pid was reused since selection", () => {
  const targets = [{ pid: 100, startTime: 5000 }, { pid: 200, startTime: 5001 }];
  const afterReuse = processTable([[100, 1, 9999], [200, 100, 5001]]);
  assert.deepEqual(pidsOf(survivingTargets(targets, afterReuse)), [200]);
  assert.deepEqual(survivingTargets(targets, processTable([])), []);
});

test("REGRESSION: a target recycled between escalation rounds is never signalled again", async () => {
  // Signalling frees process ids, so the round that follows a SIGTERM is
  // exactly where a reused number can be picked up. Selecting by pid membership
  // alone signalled that unrelated process with SIGTERM and then SIGKILL.
  const signalled = [];
  const tables = [
    processTable([[100, 1, 5000], [200, 1, 5001]]),  // round 1: both ours
    processTable([[100, 1, 9999], [200, 1, 5001]]),  // round 2: 100's pid reused
    processTable([[100, 1, 9999], [200, 1, 5001]]),  // round 3: unchanged
    processTable([[100, 1, 9999]]),                  // final confirmation
  ];
  let round = 0;
  const survivors = await terminateTargets({
    targets: [{ pid: 100, startTime: 5000 }, { pid: 200, startTime: 5001 }],
    readTable: () => tables[Math.min(round, tables.length - 1)],
    kill: (pid, signal) => signalled.push(`${signal}:${pid}`),
    wait: async () => { round += 1; },
  });
  assert.deepEqual(signalled, ["SIGTERM:100", "SIGTERM:200", "SIGTERM:200", "SIGKILL:200"]);
  assert.equal(signalled.some((entry) => entry.endsWith(":100") && entry.startsWith("SIGKILL")), false);
  assert.deepEqual(survivors, []);
});

test("escalation stops as soon as nothing of ours remains", async () => {
  const signalled = [];
  let round = 0;
  const tables = [processTable([[100, 1, 5000]]), processTable([])];
  const survivors = await terminateTargets({
    targets: [{ pid: 100, startTime: 5000 }],
    readTable: () => tables[Math.min(round, tables.length - 1)],
    kill: (pid, signal) => signalled.push(`${signal}:${pid}`),
    wait: async () => { round += 1; },
  });
  assert.deepEqual(signalled, ["SIGTERM:100"]);
  assert.deepEqual(survivors, []);
});

test("escalation signals nothing when there are no targets", async () => {
  const signalled = [];
  const survivors = await terminateTargets({
    targets: [],
    readTable: () => processTable([[100, 1, 5000]]),
    kill: (pid, signal) => signalled.push(`${signal}:${pid}`),
    wait: async () => {},
  });
  assert.deepEqual(signalled, []);
  assert.deepEqual(survivors, []);
});

// --- the shell: the pin is read at spawn, against a real process ------------

test("the spawn-time pin matches /proc read independently, and stops matching once gone", async () => {
  // This is the property whose violation was the previous P1: the pin must come
  // from the moment of spawn, not from the snapshot the comparison later uses.
  // Asserting it against a real process is what keeps the docstring honest.
  const child = spawn(process.execPath, ["-e", "setTimeout(() => {}, 60000)"], { stdio: "ignore" });
  await new Promise((resolve, reject) => {
    child.once("spawn", resolve);
    child.once("error", reject);
  });
  const pinned = processStartTime(child.pid);
  const independently = parseProcessStartTime(readFileSync(`/proc/${child.pid}/stat`, "utf8"));
  assert.equal(Number.isInteger(pinned), true, "a spawned process must have a readable start time");
  assert.equal(pinned, independently);

  const table = { parentByPid: new Map(), startTimeByPid: new Map([[child.pid, independently]]) };
  assert.equal(rootIsConfirmed({ pid: child.pid, startTime: pinned, exited: false }, table), true);
  assert.equal(rootIsConfirmed({ pid: child.pid, startTime: pinned + 1, exited: false }, table), false);

  const exited = new Promise((resolve) => child.once("exit", resolve));
  child.kill("SIGKILL");
  await exited;
  // Once the process is gone the pin cannot match a live table entry.
  assert.equal(rootIsConfirmed({ pid: child.pid, startTime: pinned, exited: true }, table), false);
  assert.equal(processStartTime(child.pid), null);
});

test("a start time cannot be read for a process that does not exist", () => {
  assert.equal(processStartTime(2 ** 31 - 1), null);
  assert.equal(processStartTime(-1), null);
});

// --- display ownership ------------------------------------------------------

test("the X lock pid is parsed from the server's fixed-width field", () => {
  // The X server writes the pid right-aligned with a trailing newline.
  assert.equal(parseDisplayLockPid("   2470092\n"), 2_470_092);
  assert.equal(parseDisplayLockPid("1\n"), 1);
  for (const malformed of ["", "   \n", "abc", "12a", "-5\n", undefined, null]) {
    assert.equal(parseDisplayLockPid(malformed), null, `${malformed} must not parse`);
  }
});

test("a display is only ours while its lock names our own server", () => {
  const reader = (contents) => () => contents;
  assert.equal(displayLockOwnedBy("/tmp/.X999-lock", 4242, reader("      4242\n")), true);
  assert.equal(displayLockOwnedBy("/tmp/.X999-lock", 4242, reader("      4243\n")), false,
    "a foreign server that took the number must never be adopted or deleted");
  assert.equal(displayLockOwnedBy("/tmp/.X999-lock", 4242, reader(null)), false);
  assert.equal(displayLockOwnedBy("/tmp/.X999-lock", 4242, reader("garbage")), false);
});

test("spawnApplication establishes the pin at spawn and marks the root dead on exit", async () => {
  // The composition, not just its parts. If the start-time read were moved out
  // of spawn and into cleanup, the returned root would carry no pin and this
  // fails; if the exit handler were dropped, `exited` would stay false.
  const { child, root } = await spawnApplication(process.execPath, { ...process.env, NODE_OPTIONS: "" }, { display: ":120", paseoHome: "/tmp/run/home", userDataDir: "/tmp/run/profile", listen: "127.0.0.1:1", cdpPort: 2 }, () => {});
  try {
    assert.equal(root.pid, child.pid);
    assert.equal(Number.isInteger(root.startTime), true, "the pin must be read at spawn");
    assert.equal(root.startTime, parseProcessStartTime(readFileSync(`/proc/${child.pid}/stat`, "utf8")));
    assert.equal(root.exited, false);
    const table = { parentByPid: new Map(), startTimeByPid: new Map([[root.pid, root.startTime]]) };
    assert.equal(rootIsConfirmed(root, table), true);

    const exited = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGKILL");
    await exited;
    assert.equal(root.exited, true, "the exit handler must mark the root dead");
    // A dead root is refused even against a table that still lists its pid.
    assert.equal(rootIsConfirmed(root, table), false);
    assert.deepEqual(confirmedTeardownTargets({ root, processTable: table }), []);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }
});

test("spawnApplication rejects rather than leaking an unhandled spawn error", async () => {
  await assert.rejects(
    () => spawnApplication("/nonexistent/definitely-not-here", { ...process.env }, { display: ":120", paseoHome: "/tmp/run/home", userDataDir: "/tmp/run/profile", listen: "127.0.0.1:1", cdpPort: 2 }, () => {}),
    /ENOENT/u,
  );
});

// --- the enforcement sites --------------------------------------------------
//
// A mutation proves coverage only where a property is ENFORCED, not where it is
// defined. The tests below break the call sites, not the predicates: each one
// fails if the corresponding line in claimDisplay or createTeardown stops
// consulting the check it is supposed to consult.

/** A fake X server whose pid we control, for driving claimDisplay. */
function fakeServer(pid) {
  return { pid, on() {}, kill() {} };
}

/** Records what claimDisplay hands over and when. */
function recordingTeardown() {
  const events = [];
  return {
    events,
    adoptDisplay: (record) => events.push(["adopt", record === null ? null : record.serverPid]),
    releaseDisplay: async () => { events.push(["release"]); },
  };
}

function displayIo({ serverPid, lockPid, socketExists = true, occupied = new Set() }) {
  return {
    spawnServer: () => fakeServer(serverPid),
    socketExists: () => socketExists,
    readLock: () => (lockPid === null ? null : `   ${lockPid}\n`),
    occupied: () => occupied,
    wait: async () => {},
  };
}

test("a display whose lock names our own server is adopted", async () => {
  const owner = recordingTeardown();
  const claimed = await claimDisplay({ minimum: 120, maximum: 121 }, displayIo({ serverPid: 4242, lockPid: 4242 }), 50, owner);
  assert.equal(claimed.display, ":120");
  assert.equal(claimed.serverPid, 4242);
});

test("ENFORCEMENT: a foreign server's display is never adopted, even with a socket present", async () => {
  // Dropping the ownership term from the adopt condition must fail here. The
  // socket exists throughout, so only the lock check can refuse the display.
  await assert.rejects(
    () => claimDisplay({ minimum: 120, maximum: 122 }, displayIo({ serverPid: 4242, lockPid: 9999 }), 2, recordingTeardown()),
    /no free display/u,
    "a display whose lock names another server must not be adopted",
  );
  await assert.rejects(
    () => claimDisplay({ minimum: 120, maximum: 122 }, displayIo({ serverPid: 4242, lockPid: null }), 2, recordingTeardown()),
    /no free display/u,
    "an unreadable lock is not proof of ownership",
  );
});

/** Drives createTeardown with recorded effects and an injected process table. */
function teardownHarness({ table, lockPid, serverPid = 4242 }) {
  const killed = [];
  const removed = [];
  const effects = { killDisplay: 0, removeRunRoot: 0 };
  const unit = createTeardown({
    readTable: () => table(),
    kill: (pid, signal) => killed.push(`${signal}:${pid}`),
    wait: async () => {},
    readLock: () => (lockPid === null ? null : `   ${lockPid}\n`),
    removePath: (path) => removed.push(path),
  });
  const displaySignals = [];
  unit.adoptDisplay({
    display: ":120", socket: "/tmp/.X11-unix/X120", lock: "/tmp/.X120-lock", serverPid,
    process: { kill: (signal) => displaySignals.push(signal) },
  });
  return { unit, killed, removed, effects, displaySignals };
}

test("ENFORCEMENT: a descendant tracked during the run is still signalled after the root exits", async () => {
  // This is the third Review's P1 #2, asserted at the composition rather than
  // at the selector. It fails if the cleanup path stops carrying `tracked`, or
  // if trackDescendants stops recording.
  let table = processTable([[100, 1, 5000], [250, 100, 7000]]);
  const { unit, killed } = teardownHarness({ table: () => table, lockPid: 4242 });
  const root = { pid: 100, startTime: 5000, exited: false };
  unit.pinRoot(root);
  unit.trackDescendants();                       // daemon recorded while the root was alive
  assert.deepEqual([...unit.trackedSnapshot().keys()], [250]);

  root.exited = true;                            // application dies first
  table = processTable([[250, 1, 7000]]);        // daemon reparented to init, identity intact
  const outcome = await unit.run();
  assert.deepEqual(outcome.targets.map((t) => t.pid), [250]);
  assert.equal(killed.includes("SIGTERM:250"), true, "the setsid'd daemon must still be signalled");
  assert.equal(killed.some((entry) => entry.endsWith(":100")), false, "the exited root must not be signalled");
});

test("ENFORCEMENT: the cleanup path never re-derives the pin from its own snapshot", async () => {
  // This is the second Review's P1, asserted at the composition. If the root's
  // start time is recomputed from the table cleanup reads, the comparison
  // becomes a value against itself and this recycled root gets signalled.
  const table = processTable([[100, 1, 9999], [200, 100, 9999]]);
  const { unit, killed } = teardownHarness({ table: () => table, lockPid: 4242 });
  unit.pinRoot({ pid: 100, startTime: 5000, exited: false });   // pinned at spawn
  const outcome = await unit.run();
  assert.deepEqual(outcome.targets, []);
  assert.deepEqual(killed, [], "a recycled root and its apparent children must be signalled by nothing");
});

test("ENFORCEMENT: a display that is no longer ours has its socket and lock left alone", async () => {
  // Forcing the ownership result true must fail here.
  const table = processTable([]);
  const { unit, removed, } = teardownHarness({ table: () => table, lockPid: 9999, serverPid: 4242 });
  const outcome = await unit.run();
  assert.equal(outcome.displayRemoved, false);
  assert.deepEqual(removed, [], "a live foreign server's files must never be deleted");
});

test("a display still ours has exactly its own socket and lock removed", async () => {
  const table = processTable([]);
  const { unit, removed } = teardownHarness({ table: () => table, lockPid: 4242, serverPid: 4242 });
  const outcome = await unit.run();
  assert.equal(outcome.displayRemoved, true);
  assert.deepEqual(removed, ["/tmp/.X11-unix/X120", "/tmp/.X120-lock"]);
});

test("the display server is stopped whether or not its files are ours to remove", async () => {
  for (const lockPid of [4242, 9999]) {
    const table = processTable([]);
    let removeRunRootCalls = 0;
    const { unit, displaySignals } = teardownHarness({ table: () => table, lockPid });
    await unit.run({ removeRunRoot: () => { removeRunRootCalls += 1; } });
    assert.deepEqual(displaySignals, ["SIGTERM", "SIGKILL"]);
    assert.equal(removeRunRootCalls, 1);
  }
});

test("teardown with no root and no display signals and removes nothing", async () => {
  const unit = createTeardown({
    readTable: () => processTable([[100, 1, 5000]]),
    kill: () => assert.fail("nothing may be signalled"),
    wait: async () => {},
    readLock: () => null,
    removePath: () => assert.fail("nothing may be removed"),
  });
  const outcome = await unit.run();
  assert.deepEqual(outcome.targets, []);
  assert.equal(outcome.displayRemoved, false);
});

test("ENFORCEMENT: the spawned application really does not receive the Paseo environment", async () => {
  // Asserted against the child's own view, not against the filter's return
  // value: applying the filter is now part of spawning, so a caller cannot
  // hand the application an unfiltered environment.
  const { child, root, environment } = await spawnApplication(
    process.execPath,
    {
      ...process.env,
      PASEO_PASSWORD: "super-secret",
      PASEO_HOST: "tcp://example:6767?password=super-secret",
      DIRECTOR_PASEO_CREDENTIAL_FILE: "/run/credential",
    },
    { display: ":120", paseoHome: "/tmp/run/home", userDataDir: "/tmp/run/profile", listen: "127.0.0.1:1", cdpPort: 2 },
    () => {},
  );
  try {
    assert.equal(environment.PASEO_PASSWORD, undefined);
    assert.equal(environment.PASEO_HOST, undefined);
    assert.equal(environment.DIRECTOR_PASEO_CREDENTIAL_FILE, undefined);
    assert.equal(environment.PASEO_HOME, "/tmp/run/home");
    assert.equal(Number.isInteger(root.startTime), true);
    const leaked = Object.entries(environment).filter(
      ([name, value]) => /^(?:PASEO|DIRECTOR)_/u.test(name) && String(value).includes("super-secret"),
    );
    assert.deepEqual(leaked, []);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }
});

// --- isolation, signals and artifacts, at their enforcement sites ------------

test("ENFORCEMENT: both private directories are checked, not just one", () => {
  const targets = isolationTargets({ userDataDir: "/tmp/run/profile", paseoHome: "/tmp/run/home" });
  assert.deepEqual(targets.map((target) => target.directory), ["/tmp/run/profile", "/tmp/run/home"]);
  assert.equal(targets.length, 2);
});

test("ENFORCEMENT: a build that ignores either override fails the run", async () => {
  const io = (populated) => ({ listDirectory: (d) => (populated.has(d) ? ["x"] : []), wait: async () => {} });
  const targets = isolationTargets({ userDataDir: "/profile", paseoHome: "/home" });
  assert.equal(await verifyIsolation(targets, { ...io(new Set(["/profile", "/home"])), attempts: 1 }), 2);
  await assert.rejects(
    () => verifyIsolation(targets, { ...io(new Set(["/profile"])), attempts: 1 }),
    /PASEO_HOME/u,
    "an ignored PASEO_HOME would point the bundled daemon at the operator's own",
  );
  await assert.rejects(
    () => verifyIsolation(targets, { ...io(new Set(["/home"])), attempts: 1 }),
    /Electron user-data directory/u,
  );
});

test("ENFORCEMENT: interruption registers the same teardown as a normal exit", () => {
  const registered = [];
  const seen = [];
  const installed = installSignalHandlers({
    on: (signal, bound) => registered.push([signal, bound]),
    handler: (signal) => seen.push(signal),
  });
  // Asserted against the decision rather than a pinned literal list, so adding
  // or removing a signal is a change to the decision and its reasons.
  assert.deepEqual(registered.map(([signal]) => signal), [...TERMINATING_SIGNALS]);
  assert.equal(installed.length, TERMINATING_SIGNALS.length);
  for (const [, bound] of registered) bound();
  assert.deepEqual(seen, [...TERMINATING_SIGNALS]);
});

test("ENFORCEMENT: the application log is written scrubbed", () => {
  // The secret arrives split across two chunks, as it does from a real stream.
  const artifacts = buildRunArtifacts({
    label: "0.6.1",
    userAgent: "Paseo/0.6.1",
    display: ":120",
    outcome: { directorTestId: "plugin-sidebar-director-home", reducedMotionActive: true, errorsBeforeProbe: 0 },
    rendererEvents: [],
    notes: [{ at: "t", text: "note" }],
    mainLog: ["prefix hun", "ter2 suffix"],
    environment: { PASEO_PASSWORD: "hunter2" },
  });
  const appLog = artifacts.files.find((file) => file.name.endsWith("-app-main.log"));
  assert.equal(appLog.contents.includes("hunter2"), false, "a secret must never reach the durable log");
  assert.equal(appLog.contents, "prefix <redacted> suffix");
});

test("ENFORCEMENT: metrics are computed from renderer observations, not from notes", () => {
  // Captured page text routinely contains the very strings the metrics match;
  // the 0.6.1 surface literally renders "Minified React error #130".
  const artifacts = buildRunArtifacts({
    label: "0.6.1",
    userAgent: "Paseo/0.6.1",
    display: ":120",
    outcome: { directorTestId: "plugin-sidebar-director-home", reducedMotionActive: true, errorsBeforeProbe: 0 },
    rendererEvents: [
      { at: "t", kind: "console", type: "warning", text: "[Director] Optional host primitive Icon is unavailable" },
    ],
    notes: [
      { at: "t", text: "surface text: Plugin failed: Minified React error #130" },
      { at: "t", text: "probe surface text: Optional host primitive Icon is unavailable" },
    ],
    mainLog: [],
    environment: { PASEO_PASSWORD: "" },
  });
  assert.equal(artifacts.result.react130, false, "page text must not manufacture a React #130");
  assert.equal(artifacts.result.rendererErrorsTotal, 0);
  assert.equal(artifacts.result.fallbackWarnings, 1, "page text must not inflate the fallback count");
  assert.equal(artifacts.files.length, 3);
});

/** A stand-in for the Playwright page, enough to drive the surface routine.
 *  `evaluate` dispatches on what the injected function reads, so the stub does
 *  not depend on call order. */
function stubPage({ passwordInputType = "password", reducedMotion = true, testIds = ["plugin-sidebar-director-home"] } = {}) {
  const calls = { filled: new Map(), clicked: [], emulated: [], reloaded: 0 };
  return {
    calls,
    async waitForSelector() {},
    async waitForTimeout() {},
    async click(selector) { calls.clicked.push(selector); },
    async fill(selector, value) { calls.filled.set(selector, value); },
    async getAttribute() { return passwordInputType; },
    async emulateMedia(options) { calls.emulated.push(options); },
    async reload() { calls.reloaded += 1; },
    async evaluate(fn) {
      const source = String(fn);
      if (source.includes("data-testid")) return testIds;
      if (source.includes("innerText")) return "Project health";
      if (source.includes("matchMedia")) return reducedMotion;
      throw new Error(`stub page received an unexpected evaluate: ${source}`);
    },
  };
}

const shots = [];
const driveWith = (page, password = "hunter2") => driveDirectorSurface({
  page,
  daemonHost: "127.0.0.1",
  daemonPort: 44613,
  environment: password ? { PASEO_PASSWORD: password } : {},
  outputDirectory: "/tmp/evidence",
  label: "0.6.1",
  note: () => {},
  errorCount: () => 0,
  writeScreenshot: (target, path) => { shots.push(path); },
});

test("ENFORCEMENT: the connection form is not photographed when the password field is unmasked", async () => {
  // Removing the mask assertion must fail here: an unmasked field would put the
  // secret into a screenshot in the evidence directory.
  await assert.rejects(
    () => driveWith(stubPage({ passwordInputType: "text" })),
    /not masked/u,
  );
  await assert.rejects(
    () => driveWith(stubPage({ passwordInputType: null })),
    /not masked/u,
  );
});

test("ENFORCEMENT: the run fails if reduced motion never took effect", async () => {
  // Without this the Icon probe silently degrades into a second ordinary
  // capture and the result overstates what was exercised.
  await assert.rejects(
    () => driveWith(stubPage({ reducedMotion: false })),
    /reduced motion never took effect/u,
  );
});

test("the surface routine types the daemon target and the password into the form", async () => {
  const page = stubPage();
  const outcome = await driveWith(page);
  assert.equal(page.calls.filled.get('[data-testid="direct-host-input"]'), "127.0.0.1");
  assert.equal(page.calls.filled.get('[data-testid="direct-port-input"]'), "44613");
  assert.equal(page.calls.filled.get('[data-testid="direct-password-input"]'), "hunter2");
  assert.equal(outcome.directorTestId, "plugin-sidebar-director-home");
  assert.equal(outcome.reducedMotionActive, true);
  // The external pairing flow must never be used.
  assert.equal(page.calls.clicked.includes('[data-testid="add-host-method-direct"]'), true);
  assert.equal(page.calls.clicked.some((selector) => selector.includes("pair-link")), false);
});

test("no password is typed when none was supplied", async () => {
  const page = stubPage({ passwordInputType: "text" });
  await driveWith(page, "");
  assert.equal(page.calls.filled.has('[data-testid="direct-password-input"]'), false);
});

test("the surface routine fails when the Director entry never registers", async () => {
  await assert.rejects(() => driveWith(stubPage({ testIds: [] })), /never registered/u);
});

// --- the composition root ---------------------------------------------------
//
// `main` can only be executed with an X server, a daemon and Playwright, which
// is exactly why the capture is not in CI. Its DECISIONS have all been moved
// into the tested units above, so what remains is wiring — and wiring fails by
// omission: deleting a call leaves every unit test green while the property it
// enforces silently stops being enforced.
//
// This is therefore a wiring contract, not a behavioural test. It asserts that
// the composition root still calls each tested unit. It cannot prove the calls
// are correct; the tests above do that.

// Comments are stripped first: a substring match would otherwise be satisfied
// by a call that has been commented out.
const captureSource = stripComments(readFileSync(new URL("./capture.mjs", import.meta.url), "utf8"));
const mainBody = captureSource.slice(captureSource.indexOf("export async function main("));

test("WIRING: the composition root still calls every unit that enforces a safety property", () => {
  const required = [
    ["register interruption handlers", "installSignalHandlers({ handler: onSignal })"],
    ["register the exit fallback", "installExitFallback({ handler:"],
    ["give the exit fallback a synchronous teardown", "teardown.runSync({ removeRunRoot })"],
    ["hand it the spawned root and the adopted display", "root: launched.root, display }"],
    ["derive its private layout through the tested helper", "runDirectoryLayout(outputDirectory, options.label)"],
    ["create private directories owner-only", "mode: PRIVATE_DIRECTORY_MODE"],
    ["claim a display through the ownership-proving path", "await claimDisplay("],
    ["hand the display's owner to the claiming loop", "      teardown,"],
    ["spawn through the filtered-environment path", "await spawnApplication("],
    ["pin the root from inside the spawn", "(root) => teardown.pinRoot(root)"],
    ["bind the bundled daemon to loopback", "loopbackListen(await freePort())"],
    ["use a loopback debugging endpoint", "loopbackDebuggingEndpoint(cdpPort)"],
    ["record descendants while the root is alive", "teardown.trackDescendants()"],
    ["verify isolation through the one executable step", "await verifyRunIsolation({"],
    ["build artifacts through the tested builder", "buildRunArtifacts({"],
    ["take no injectable effect from the composition root", "createTeardown()"],
    ["write exactly those artifacts", "for (const file of artifacts.files) writeFileSync("],
    ["run teardown on every exit path", "await cleanup();"],
  ];
  for (const [property, call] of required) {
    assert.equal(mainBody.includes(call), true, `main must still ${property} (missing: ${call})`);
  }
});

test("WIRING: the contract is not satisfied by a commented-out call", () => {
  // The contract itself must resist the trick it is meant to catch.
  const live = "  teardown.trackDescendants();";
  assert.equal(stripComments(live).includes("teardown.trackDescendants()"), true);
  assert.equal(stripComments(`  // ${live.trim()}`).includes("teardown.trackDescendants()"), false);
  assert.equal(stripComments(`  /* ${live.trim()} */`).includes("teardown.trackDescendants()"), false);
});

test("WIRING: signal handlers are installed before any resource is created", () => {
  // A resource created before the handlers exist would be orphaned outright,
  // because the default signal disposition terminates without running teardown.
  const handlers = mainBody.indexOf("installSignalHandlers({");
  for (const [label, marker] of [
    ["directories", "mkdirSync(outputDirectory"],
    ["private directories", "mode: PRIVATE_DIRECTORY_MODE"],
    ["the display", "await claimDisplay("],
    ["the application", "await spawnApplication("],
  ]) {
    assert.equal(handlers < mainBody.indexOf(marker), true, `handlers must be installed before ${label}`);
  }
});

test("WIRING: the composition root holds no teardown state of its own", () => {
  assert.equal(/^\s*const tracked = new Map\(\);/mu.test(mainBody), false);
  assert.equal(/^\s*let root = null;/mu.test(mainBody), false);
  assert.equal(/^\s*let display = null;/mu.test(mainBody), false);
});

test("ENFORCEMENT: the launch options actually carry the minimal environment", () => {
  const options = displayServerSpawnOptions({
    PATH: "/usr/bin", PASEO_PASSWORD: "super-secret", DIRECTOR_PASEO_URL: "ws://h/ws",
  });
  assert.deepEqual(Object.keys(options.env), ["PATH"]);
  assert.equal(JSON.stringify(options).includes("super-secret"), false);
  assert.deepEqual(options.stdio, ["ignore", "ignore", "ignore"]);
});

test("TRIPWIRE: no read is swallowed into a value that means \"fine\"", () => {
  // A guard that reads source text is a tripwire, not a guarantee: a form can
  // always be rewritten, and one was. The guarantee is
  // "ENFORCEMENT: the isolation step owns its own arguments" below, which
  // executes the step. This stays as a cheap early warning, nothing more.
  // The defect this bans: `catch { return ""; }` around a /proc read made an
  // unreadable environment indistinguishable from a clean one, and the check
  // reported success having read nothing. The pattern is banned at source, not
  // only the one instance, because the instance moved once already.
  assert.equal(/catch\s*\{\s*return\s*""/u.test(captureSource), false, "a caught read must not become an empty string");
});

test("WIRING: no child is ever spawned with the ambient environment", () => {
  // The X server was previously spawned with no env argument at all, so the
  // whole source is asserted rather than one call site.
  assert.equal(/env:\s*process\.env/u.test(captureSource), false);
  assert.equal(captureSource.includes("displayServerSpawnOptions()"), true);
});

test("WIRING: no raw loopback address or directory mode is left inline in main", () => {
  assert.equal(/127\.0\.0\.1/u.test(mainBody), false, "loopback must come from the tested helpers");
  assert.equal(/0o700/u.test(mainBody), false, "the private mode must come from the tested constant");
});

// --- ownership from the moment a resource exists ----------------------------

test("ENFORCEMENT: claimDisplay hands the server to teardown before it starts polling", async () => {
  // The server is live for up to several seconds while its socket and lock
  // appear. Registering it only on return would orphan a running X server, its
  // socket and its lock if an interruption arrived in that window.
  const owner = recordingTeardown();
  const io = displayIo({ serverPid: 4242, lockPid: 4242 });
  let adoptedBeforeFirstPoll = null;
  const watched = { ...io, socketExists: () => { adoptedBeforeFirstPoll ??= owner.events.length > 0; return true; } };
  await claimDisplay({ minimum: 120, maximum: 121 }, watched, 50, owner);
  assert.equal(adoptedBeforeFirstPoll, true, "the server must be owned before the first poll");
  assert.deepEqual(owner.events[0], ["adopt", 4242]);
});

test("ENFORCEMENT: an abandoned candidate server is released, not left running", async () => {
  const owner = recordingTeardown();
  await assert.rejects(
    () => claimDisplay({ minimum: 120, maximum: 121 }, displayIo({ serverPid: 4242, lockPid: 9999 }), 1, owner),
    /no free display/u,
  );
  // Two candidates in range, each adopted then released.
  assert.deepEqual(owner.events, [["adopt", 4242], ["release"], ["adopt", 4242], ["release"]]);
});

test("claimDisplay refuses to run without the owner that will hold the server", async () => {
  await assert.rejects(
    () => claimDisplay({ minimum: 120, maximum: 121 }, displayIo({ serverPid: 4242, lockPid: 4242 })),
    /requires the teardown/u,
  );
});

test("ENFORCEMENT: spawnApplication pins the root before it resolves", async () => {
  const pinned = [];
  const { child, root } = await spawnApplication(
    process.execPath,
    { ...process.env, NODE_OPTIONS: "" },
    { display: ":120", paseoHome: "/tmp/run/home", userDataDir: "/tmp/run/profile", listen: "127.0.0.1:1", cdpPort: 2 },
    (value) => pinned.push(value),
  );
  try {
    assert.equal(pinned.length, 1, "the root must be registered by the time the promise resolves");
    assert.equal(pinned[0], root);
    assert.equal(Number.isInteger(pinned[0].startTime), true);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }
});

test("spawnApplication refuses to run without somewhere to record the root", () => {
  // Thrown synchronously, before any process exists, so a caller that forgot
  // the registration cannot create something teardown will never see.
  assert.throws(
    () => spawnApplication(process.execPath, { ...process.env }, { display: ":1", paseoHome: "/a", userDataDir: "/b", listen: "127.0.0.1:1", cdpPort: 2 }),
    /requires a function that records the spawned root/u,
  );
});

test("a released display is stopped and forgotten", async () => {
  const table = processTable([]);
  const { unit, removed, displaySignals } = teardownHarness({ table: () => table, lockPid: 4242 });
  assert.equal(await unit.releaseDisplay(), true);
  assert.deepEqual(displaySignals, ["SIGTERM", "SIGKILL"]);
  assert.deepEqual(removed, ["/tmp/.X11-unix/X120", "/tmp/.X120-lock"]);
  // Forgotten, so a later teardown does not try to stop it twice.
  const outcome = await unit.run();
  assert.equal(outcome.displayRemoved, false);
  assert.deepEqual(displaySignals, ["SIGTERM", "SIGKILL"]);
});

// --- evidence-file properties enforced in the builder -----------------------

test("ENFORCEMENT: every emitted artifact is scrubbed, not only the application log", () => {
  // Called directly, with the secret present in all three sources. The caller
  // also scrubs upstream, but this property must not depend on it.
  const artifacts = buildRunArtifacts({
    label: "0.6.1",
    userAgent: "Paseo/0.6.1 hunter2",
    display: ":120",
    outcome: { directorTestId: "plugin-sidebar-director-home", reducedMotionActive: true, errorsBeforeProbe: 0 },
    rendererEvents: [{ at: "t", kind: "console", type: "log", text: "connecting with hunter2" }],
    notes: [{ at: "t", text: "note containing hunter2" }],
    mainLog: ["log with hun", "ter2 split across chunks"],
    environment: { PASEO_PASSWORD: "hunter2" },
  });
  for (const file of artifacts.files) {
    assert.equal(file.contents.includes("hunter2"), false, `${file.name} must carry no secret`);
    assert.equal(file.contents.includes("<redacted>"), true, `${file.name} must show the redaction`);
  }
});

// --- security-relevant constants --------------------------------------------

test("private run directories are owner-only", () => {
  assert.equal(PRIVATE_DIRECTORY_MODE, 0o700);
});

test("the run layout stays inside the output directory and validates its label", () => {
  const layout = runDirectoryLayout("/tmp/out", "0.6.1");
  assert.equal(layout.runRoot, "/tmp/out/.run-0.6.1");
  assert.equal(layout.userDataDir, "/tmp/out/.run-0.6.1/electron-user-data");
  assert.equal(layout.paseoHome, "/tmp/out/.run-0.6.1/paseo-home");
  for (const path of Object.values(layout)) assert.equal(path.startsWith("/tmp/out/"), true);
  assert.throws(() => runDirectoryLayout("/tmp/out", "../../.."), /--label must match/u);
});

test("ENFORCEMENT: every listener and endpoint this tool creates is loopback", () => {
  assert.equal(loopbackListen(44613), "127.0.0.1:44613");
  assert.equal(loopbackDebuggingEndpoint(9333), "http://127.0.0.1:9333");
  for (const bad of [0, -1, 65536, 1.5, "44613", undefined, null]) {
    assert.throws(() => loopbackListen(bad), /valid TCP port/u, `${bad} must be refused`);
    assert.throws(() => loopbackDebuggingEndpoint(bad), /valid TCP port/u, `${bad} must be refused`);
  }
});

// --- the class of terminating signals, decided rather than accumulated ------

test("ENFORCEMENT: every signal the PLATFORM has is classified", () => {
  // The domain comes from os.constants.signals, not from a list in this file.
  // The previous version iterated fifteen hand-written names and so could not
  // see the eight default-terminating signals the policy omitted.
  const signals = platformSignals();
  assert.equal(signals.length > 20, true, "the platform must actually report its signals");
  assert.deepEqual(unclassifiedSignals(signals), [], "every platform signal must be classified");
  for (const signal of signals) {
    const policy = SIGNAL_POLICY[signal];
    assert.equal(
      ["terminate", "ignore", "stop", "continue"].includes(policy.disposition), true,
      `${signal} needs a known default disposition`,
    );
    if (policy.disposition !== "terminate" && signal !== "SIGSTOP") continue;
    assert.equal(["handle", "excuse"].includes(policy.decision), true, `${signal} terminates, so it needs a decision`);
    assert.equal(typeof policy.reason === "string" && policy.reason.length > 0, true, `${signal} needs a stated reason`);
  }
});

test("ENFORCEMENT: a signal the platform gains but the table has not classified fails", () => {
  // The guard that makes the domain derived rather than declared.
  assert.deepEqual(unclassifiedSignals(["SIGHUP", "SIGTERM"]), []);
  assert.deepEqual(unclassifiedSignals(["SIGHUP", "SIGNEWFANGLED"]), ["SIGNEWFANGLED"]);
});

test("the handled and excused sets are derived from the policy, not restated", () => {
  const handled = platformSignals().filter((s) => SIGNAL_POLICY[s].decision === "handle");
  const excused = platformSignals().filter((s) => SIGNAL_POLICY[s].decision === "excuse");
  assert.deepEqual([...TERMINATING_SIGNALS], handled);
  assert.deepEqual(Object.keys(UNHANDLED_TERMINATING_SIGNALS).sort(), excused.sort());
  for (const signal of handled) assert.equal(excused.includes(signal), false);
});

test("REGRESSION: the eight signals that previously reached neither set are handled", () => {
  // Measured: with the previous policy each of these terminated the process
  // without the exit handler running, taking a display, an application, its
  // setsid'd daemon and the profile with it.
  for (const signal of ["SIGALRM", "SIGIO", "SIGPOLL", "SIGPROF", "SIGPWR", "SIGSTKFLT", "SIGSYS", "SIGVTALRM"]) {
    assert.equal(TERMINATING_SIGNALS.includes(signal), true, `${signal} must reach teardown`);
  }
});

test("REGRESSION: SIGHUP is handled — a closing terminal must not bypass teardown", () => {
  // This is the defect: SIGHUP went to its default disposition and killed the
  // run with its display, application, that application's daemon and its
  // profile still live.
  assert.equal(TERMINATING_SIGNALS.includes("SIGHUP"), true);
  for (const signal of ["SIGINT", "SIGTERM", "SIGQUIT"]) {
    assert.equal(TERMINATING_SIGNALS.includes(signal), true);
  }
});

test("the uncatchable signals are recorded as uncatchable, not as handled", () => {
  for (const signal of ["SIGKILL", "SIGSTOP"]) {
    assert.equal(TERMINATING_SIGNALS.includes(signal), false);
    assert.match(UNHANDLED_TERMINATING_SIGNALS[signal], /cannot be caught/u);
  }
});

test("ENFORCEMENT: every handled signal is registered", () => {
  const registered = [];
  const seen = [];
  installSignalHandlers({ on: (signal, bound) => registered.push([signal, bound]), handler: (s) => seen.push(s) });
  assert.deepEqual(registered.map(([signal]) => signal), [...TERMINATING_SIGNALS]);
  for (const [, bound] of registered) bound();
  assert.deepEqual(seen, [...TERMINATING_SIGNALS]);
});

test("ENFORCEMENT: an exit fallback covers termination paths no signal list can", () => {
  const registered = [];
  let ran = 0;
  installExitFallback({ on: (event, bound) => registered.push([event, bound]), handler: () => { ran += 1; } });
  assert.deepEqual(registered.map(([event]) => event), ["exit"]);
  registered[0][1]();
  assert.equal(ran, 1);
});

test("ENFORCEMENT: the synchronous fallback signals, clears the display and removes the run root", () => {
  const table = processTable([[100, 1, 5000], [250, 100, 7000]]);
  const { unit, killed, removed, displaySignals } = teardownHarness({ table: () => table, lockPid: 4242 });
  unit.pinRoot({ pid: 100, startTime: 5000, exited: false });
  unit.trackDescendants();
  let removedRunRoot = 0;
  const count = unit.runSync({ removeRunRoot: () => { removedRunRoot += 1; } });
  assert.equal(count >= 1, true);
  assert.deepEqual(killed, ["SIGKILL:100", "SIGKILL:250"], "no waiting is possible at exit, so go straight to SIGKILL");
  assert.deepEqual(displaySignals, ["SIGKILL"]);
  assert.deepEqual(removed, ["/tmp/.X11-unix/X120", "/tmp/.X120-lock"]);
  assert.equal(removedRunRoot, 1, "a process that outlived the escalation must not leave the run root behind");
});

test("the synchronous fallback refuses an unconfirmed root and a foreign display", () => {
  const table = processTable([[100, 1, 9999]]);
  const { unit, killed, removed } = teardownHarness({ table: () => table, lockPid: 9999, serverPid: 4242 });
  unit.pinRoot({ pid: 100, startTime: 5000, exited: false });
  unit.runSync();
  assert.deepEqual(killed, [], "a recycled root authorises no signal, at exit as anywhere else");
  assert.deepEqual(removed, [], "a live foreign server's files are never deleted");
});

// --- the tool must not leak the credential into its own children ------------

test("ENFORCEMENT: the display server is given an explicit minimal environment", () => {
  const environment = displayServerEnvironment({
    PATH: "/usr/bin", HOME: "/home/x", TMPDIR: "/tmp",
    PASEO_PASSWORD: "super-secret", PASEO_HOME: "/home/x/.paseo",
    PASEO_HOST: "tcp://host:6767?password=super-secret", DIRECTOR_PASEO_URL: "ws://host:6767/ws",
    SOMETHING_ELSE: "also-dropped",
  });
  assert.deepEqual(Object.keys(environment).sort(), ["HOME", "PATH", "TMPDIR"]);
  assert.equal(JSON.stringify(environment).includes("super-secret"), false);
});

test("ENFORCEMENT: the isolation check can see a child that inherited the credential", () => {
  // The previous check looked only at directories, so it passed on a leaking
  // child. A check that passes on a leak is worth less than no check.
  const leaking = "PATH=/usr/bin\0PASEO_PASSWORD=super-secret\0DIRECTOR_PASEO_URL=ws://h/ws\0";
  const clean = "PATH=/usr/bin\0HOME=/home/x\0";
  assert.deepEqual(unexpectedEnvironmentNames(leaking), ["DIRECTOR_PASEO_URL", "PASEO_PASSWORD"]);
  assert.throws(() => unexpectedEnvironmentNames(ENVIRONMENT_UNREADABLE), /could not be read/u);
  assert.deepEqual(unexpectedEnvironmentNames(clean), []);
  assert.equal(verifyChildEnvironments([{ pid: 1, label: "the display server" }], () => clean), 1);
  assert.throws(
    () => verifyChildEnvironments([{ pid: 7, label: "the display server" }], () => leaking),
    /the display server \(pid 7\) carried DIRECTOR_PASEO_URL, PASEO_PASSWORD/u,
  );
});

test("ENFORCEMENT: what the tool deliberately sets is allowed; what is inherited is not", () => {
  // The first version of this check failed the real run by flagging the six
  // variables the tool sets itself. The allowance is derived from
  // applicationEnvironment, so it cannot drift from what is actually set, and
  // an inherited credential is still caught.
  const intended = intendedApplicationEnvironmentNames();
  assert.equal(intended.includes("PASEO_HOME"), true);
  assert.equal(intended.includes("PASEO_ELECTRON_USER_DATA_DIR"), true);
  assert.equal(intended.includes("PASEO_PASSWORD"), false);
  assert.equal(intended.includes("PASEO_HOST"), false);
  const applicationEnviron = `${intended.map((name) => `${name}=x`).join("\0")}\0PATH=/usr/bin\0`;
  assert.equal(verifyChildEnvironments([{ pid: 3, label: "the application", allowed: intended }], () => applicationEnviron), 1);
  assert.throws(
    () => verifyChildEnvironments(
      [{ pid: 3, label: "the application", allowed: intended }],
      () => `${applicationEnviron}PASEO_PASSWORD=super-secret\0`,
    ),
    /carried PASEO_PASSWORD/u,
  );
  // The display server is allowed nothing at all.
  assert.throws(
    () => verifyChildEnvironments([{ pid: 4, label: "the display server", allowed: [] }], () => "PASEO_HOME=/x\0"),
    /the display server \(pid 4\) carried PASEO_HOME/u,
  );
});

// --- guards and the two previously surviving mutants ------------------------

test("ENFORCEMENT: claimDisplay refuses every owner that cannot hold a server, before spawning", async () => {
  // Passed null it previously spawned a real server and only then threw, which
  // is exactly the orphan the guard exists to prevent.
  let spawned = 0;
  const io = {
    spawnServer: () => { spawned += 1; return fakeServer(1); },
    socketExists: () => false, readLock: () => null, occupied: () => new Set(), wait: async () => {},
  };
  for (const owner of [undefined, null, {}, 0, "x", { adoptDisplay() {} }]) {
    await assert.rejects(
      () => claimDisplay({ minimum: 190, maximum: 190 }, io, 1, owner),
      /requires the teardown/u,
      `owner ${JSON.stringify(owner)} must be refused`,
    );
  }
  assert.equal(spawned, 0, "nothing may be spawned before the owner is proved usable");
});

test("ENFORCEMENT: an unreadable start time refuses the run rather than pinning nothing", () => {
  // Without this refusal teardown confirms nothing and signals nothing, which
  // is indistinguishable from a clean run.
  assert.deepEqual(pinnedRoot(4242, () => 5000), { pid: 4242, startTime: 5000, exited: false });
  for (const unreadable of [null, undefined, Number.NaN, "5000", 1.5]) {
    assert.throws(() => pinnedRoot(4242, () => unreadable), /refusing to continue/u);
  }
});

test("an init process is never a teardown target, by either route", () => {
  // pid 1 must be excluded whether it arrives as a parent-map descendant or as
  // a recorded identity, so a fixture must exercise both.
  const table = processTable([[100, 1, 5000], [1, 0, 1]]);
  const root = { pid: 100, startTime: 5000, exited: false };
  assert.deepEqual(
    pidsOf(confirmedTeardownTargets({ root, tracked: new Map([[1, 1]]), processTable: table, selfPid: 999 })),
    [100],
  );
  assert.deepEqual(
    pidsOf(confirmedTeardownTargets({ root, tracked: new Map([[0, 0], [1, 1]]), processTable: table, selfPid: 999 })),
    [100],
  );
});

// --- the RANGE of the child-environment check, not only its unit ------------

test("ENFORCEMENT: the child-environment check ranges over both children", () => {
  // Replacing the call's argument with an empty array previously deleted the
  // check and left every test green. The range is a tested unit now.
  const targets = childEnvironmentTargets({ applicationPid: 11, displayServerPid: 22 });
  assert.deepEqual(targets.map((target) => target.pid), [11, 22]);
  assert.deepEqual(targets.map((target) => target.label), ["the application", "the display server"]);
});

test("ENFORCEMENT: each child is judged under its own policy and allowlist", () => {
  const [application, displayServer] = childEnvironmentTargets({ applicationPid: 11, displayServerPid: 22 });
  assert.equal(application.policy, "namespaces");
  assert.equal(application.allowed.includes("PASEO_HOME"), true);
  assert.equal(displayServer.policy, "exclusive");
  assert.deepEqual(displayServer.allowed, [...DISPLAY_SERVER_ENVIRONMENT_NAMES]);
  // Giving the display server the application's allowlist must not pass as
  // equivalent: it would admit PASEO_HOME and the rest.
  assert.equal(displayServer.allowed.includes("PASEO_HOME"), false);
});

test("ENFORCEMENT: an empty range is refused rather than silently passing", () => {
  assert.throws(() => verifyChildEnvironments([], () => ""), /no children to check/u);
});

test("the exclusive policy sees anything the tool did not set, not only two namespaces", () => {
  // The application can only be judged over PASEO_* and DIRECTOR_*, because it
  // must inherit PATH and the rest. The display server can be judged entirely.
  const environ = "PATH=/usr/bin\0SOMETHING_ELSE=x\0PASEO_HOME=/x\0";
  assert.deepEqual(unexpectedEnvironmentNames(environ, ["PASEO_HOME"], "namespaces"), []);
  assert.deepEqual(
    unexpectedEnvironmentNames(environ, [...DISPLAY_SERVER_ENVIRONMENT_NAMES], "exclusive"),
    ["PASEO_HOME", "SOMETHING_ELSE"],
  );
});

// --- the two guards that survived mutation ----------------------------------

test("ENFORCEMENT: a root pinned at an init pid authorises nothing", () => {
  // Relaxing the guard to pid <= 0 previously stayed green, and a root at pid 1
  // then authorised every transitive descendant of init.
  const table = processTable([[1, 0, 1], [2, 1, 2], [3, 2, 3]]);
  assert.equal(rootIsConfirmed({ pid: 1, startTime: 1, exited: false }, table), false);
  assert.equal(rootIsConfirmed({ pid: 0, startTime: 1, exited: false }, table), false);
  assert.deepEqual(confirmedTeardownTargets({ root: { pid: 1, startTime: 1, exited: false }, processTable: table }), []);
});

test("ENFORCEMENT: a target with no readable start time is refused, not admitted", () => {
  // Relaxing the refusal previously stayed green, and survivingTargets then
  // passed the target through on undefined === undefined.
  const table = processTable([[100, 1, 5000], [200, 100, 5001]]);
  table.startTimeByPid.delete(200);
  const targets = confirmedTeardownTargets({
    root: { pid: 100, startTime: 5000, exited: false }, processTable: table, selfPid: 999,
  });
  assert.deepEqual(pidsOf(targets), [100], "a descendant with no readable identity must not be signalled");
  for (const target of targets) assert.equal(Number.isInteger(target.startTime), true);
  assert.deepEqual(survivingTargets([{ pid: 200, startTime: undefined }], table), []);
});

// --- an unreadable input must not become a value meaning "fine" -------------

test("ENFORCEMENT: a child whose environment cannot be read refuses the run", async () => {
  // Measured before the fix: a real process really carrying PASEO_PASSWORD, put
  // through the tool's own reader after it exited, produced "2 children
  // checked, clean". A zombie raises EACCES and a reaped process ENOENT; both
  // were swallowed into "", which the check could not tell from a clean child.
  // A controlled environment, so the assertion does not depend on whatever the
  // test runner happened to inherit.
  const child = spawn(process.execPath, ["-e", "setTimeout(()=>{},300)"], {
    env: { PATH: process.env.PATH ?? "/usr/bin", PASEO_PASSWORD: "super-secret" }, stdio: "ignore",
  });
  await new Promise((resolve, reject) => { child.once("spawn", resolve); child.once("error", reject); });
  const alive = readChildEnvironment(child.pid);
  assert.equal(typeof alive, "string");
  assert.equal(alive.includes("PASEO_PASSWORD"), true, "the reader must actually see the environment");
  assert.throws(
    () => verifyChildEnvironments(childEnvironmentTargets({ applicationPid: child.pid, displayServerPid: child.pid }), readChildEnvironment),
    /PASEO_PASSWORD/u,
  );

  await new Promise((resolve) => child.once("exit", resolve));
  await new Promise((resolve) => setTimeout(resolve, 200));
  assert.equal(readChildEnvironment(child.pid), ENVIRONMENT_UNREADABLE, "a gone process has no readable environment");
  assert.throws(
    () => verifyChildEnvironments(childEnvironmentTargets({ applicationPid: child.pid, displayServerPid: child.pid }), readChildEnvironment),
    /could not be read, so isolation is unverified/u,
    "a check that read nothing must not report success",
  );
});

test("ENFORCEMENT: a reader that returns nothing at all is refused, not believed", () => {
  // Replacing the reader with () => "" previously left the whole suite green.
  const targets = childEnvironmentTargets({ applicationPid: 11, displayServerPid: 22 });
  assert.throws(() => verifyChildEnvironments(targets, () => ENVIRONMENT_UNREADABLE), /could not be read/u);
  assert.throws(() => verifyChildEnvironments(targets, () => undefined), /could not be read/u);
  assert.throws(() => verifyChildEnvironments(targets, () => null), /could not be read/u);
  // An empty string is a real read of a real empty environment, and is clean.
  assert.equal(verifyChildEnvironments(targets, () => ""), 2);
});

test("readChildEnvironment separates a failed read from an empty one", () => {
  assert.equal(readChildEnvironment(1, () => { throw new Error("EACCES"); }), ENVIRONMENT_UNREADABLE);
  assert.equal(readChildEnvironment(1, () => ""), "");
  assert.equal(readChildEnvironment(1, () => "PATH=/usr/bin\0"), "PATH=/usr/bin\0");
});

test("ENFORCEMENT: the X lock pid accepts decimal only, not every numeric literal", () => {
  // Number() would accept all of these; the X server writes none of them.
  for (const literal of ["0x10", "1e3", "0b11", "0o17", " +7 ", "7.0", "Infinity"]) {
    assert.equal(parseDisplayLockPid(literal), null, `${literal} must not parse as a pid`);
  }
  assert.equal(parseDisplayLockPid("   2470092\n"), 2_470_092);
});

// --- the isolation step, executed rather than read --------------------------

const isolationIo = ({ environs, table }) => ({
  listDirectory: () => ["x"],
  wait: async () => {},
  attempts: 1,
  readEnviron: (pid) => environs[pid] ?? ENVIRONMENT_UNREADABLE,
  readTable: () => table,
});

const pinnedPair = () => ({
  root: { pid: 100, startTime: 5000, exited: false },
  display: { serverPid: 200, serverStartTime: 6000 },
  table: processTable([[100, 1, 5000], [200, 1, 6000]]),
});

test("ENFORCEMENT: the isolation step owns its own arguments", async () => {
  // M1 and the range mutant both lived in this call's arguments. The reader is
  // a default inside the step and the target set is built inside it, so there
  // is nothing at the call site left to swap or empty.
  const { root, display, table } = pinnedPair();
  const environs = { 100: "PATH=/usr/bin\0PASEO_HOME=/x\0", 200: "PATH=/usr/bin\0" };
  assert.equal(
    await verifyRunIsolation({ userDataDir: "/p", paseoHome: "/h", root, display, ...isolationIo({ environs, table }) }),
    2,
  );
});

test("ENFORCEMENT: the step refuses when a child's environment cannot be read", async () => {
  const { root, display, table } = pinnedPair();
  await assert.rejects(
    () => verifyRunIsolation({
      userDataDir: "/p", paseoHome: "/h", root, display,
      ...isolationIo({ environs: { 100: "PATH=/usr/bin\0" }, table }),
    }),
    /could not be read, so isolation is unverified/u,
    "the display server was never read, so the step must not report success",
  );
});

test("ENFORCEMENT: the step refuses a leaking child it really read", async () => {
  const { root, display, table } = pinnedPair();
  const environs = { 100: "PATH=/usr/bin\0", 200: "PATH=/usr/bin\0PASEO_PASSWORD=super-secret\0" };
  await assert.rejects(
    () => verifyRunIsolation({ userDataDir: "/p", paseoHome: "/h", root, display, ...isolationIo({ environs, table }) }),
    /PASEO_PASSWORD/u,
  );
});

test("ENFORCEMENT: the step refuses when both children are the same process", async () => {
  // Passing display.serverPid for both previously deleted the application's
  // credential check entirely.
  const table = processTable([[200, 1, 6000]]);
  await assert.rejects(
    () => verifyRunIsolation({
      userDataDir: "/p", paseoHome: "/h",
      root: { pid: 200, startTime: 6000, exited: false },
      display: { serverPid: 200, serverStartTime: 6000 },
      ...isolationIo({ environs: { 200: "PATH=/usr/bin\0" }, table }),
    }),
    /cannot be the same process/u,
  );
});

test("ENFORCEMENT: an environment is only attributed to a confirmed process", async () => {
  // The identity layer this tool already owns, applied to the read: a pid whose
  // start time no longer matches is not the process this run spawned, so its
  // environment says nothing about this run.
  const { root, display } = pinnedPair();
  const environs = { 100: "PATH=/usr/bin\0", 200: "PATH=/usr/bin\0" };
  await assert.rejects(
    () => verifyRunIsolation({
      userDataDir: "/p", paseoHome: "/h", root, display,
      ...isolationIo({ environs, table: processTable([[100, 1, 9999], [200, 1, 6000]]) }),
    }),
    /application is no longer the process this run spawned/u,
  );
  await assert.rejects(
    () => verifyRunIsolation({
      userDataDir: "/p", paseoHome: "/h", root, display,
      ...isolationIo({ environs, table: processTable([[100, 1, 5000], [200, 1, 9999]]) }),
    }),
    /display server is no longer the process this run spawned/u,
  );
});

test("ENFORCEMENT: the step still refuses on an unpopulated private directory", async () => {
  const { root, display, table } = pinnedPair();
  await assert.rejects(
    () => verifyRunIsolation({
      userDataDir: "/p", paseoHome: "/h", root, display,
      ...isolationIo({ environs: {}, table }), listDirectory: () => [],
    }),
    /wrote nothing to its private/u,
  );
});

// --- the reduced-motion re-open must not skip silently ----------------------

test("ENFORCEMENT: a Director surface that never reappears refuses the probe", async () => {
  // Looking once and skipping produced an outcome byte-identical to a real
  // probe: reducedMotionActive true, screenshot 17 simply never taken.
  let opens = 0;
  const page = stubPage();
  const original = page.evaluate.bind(page);
  page.evaluate = async (fn) => {
    const source = String(fn);
    if (source.includes("data-testid")) {
      opens += 1;
      // Present for the first open's single look, gone for every look after the
      // reload — so the re-open must retry and then refuse, not skip.
      return opens === 1 ? ["plugin-sidebar-director-home"] : [];
    }
    return original(fn);
  };
  await assert.rejects(() => driveWith(page), /never reappeared after the reduced-motion reload/u);
});

test("ENFORCEMENT: the step's DEFAULT reader refuses a subject it cannot read", async () => {
  // Injecting a reader in every test left the default unexercised, so a
  // swallowing default survived. This calls the step without one, against a
  // real process, and lets it use its own reader and its own process table.
  const start = async (env) => {
    const spawned = spawn(process.execPath, ["-e", "setTimeout(()=>{},2000)"], { env, stdio: "ignore" });
    await new Promise((resolve, reject) => { spawned.once("spawn", resolve); spawned.once("error", reject); });
    return spawned;
  };
  // Two real children with the environments this tool would have given them.
  const child = await start({ PATH: process.env.PATH ?? "/usr/bin", PASEO_HOME: "/tmp/run/home" });
  const server = await start(displayServerEnvironment(process.env));
  const pin = { pid: child.pid, startTime: processStartTime(child.pid), exited: false };
  const self = { serverPid: server.pid, serverStartTime: processStartTime(server.pid) };
  try {
    // Both alive and really readable: the step completes.
    assert.equal(
      await verifyRunIsolation({
        userDataDir: "/p", paseoHome: "/h", root: pin, display: self,
        listDirectory: () => ["x"], wait: async () => {}, attempts: 1,
      }),
      2,
    );
    const exited = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGKILL");
    await exited;
    await new Promise((resolve) => setTimeout(resolve, 200));

    // The subject is gone, but the identity guard is told it is still there —
    // the zombie shape, where /proc/<pid>/stat still reads and
    // /proc/<pid>/environ raises. Only this reaches the DEFAULT reader, and it
    // must refuse rather than report clean. A reader that swallowed the failed
    // read into "" would make this pass.
    const stillListed = {
      parentByPid: new Map([[pin.pid, 1], [self.serverPid, 1]]),
      startTimeByPid: new Map([[pin.pid, pin.startTime], [self.serverPid, self.serverStartTime]]),
    };
    await assert.rejects(
      () => verifyRunIsolation({
        userDataDir: "/p", paseoHome: "/h", root: pin, display: self,
        listDirectory: () => ["x"], wait: async () => {}, attempts: 1,
        readTable: () => stillListed,
      }),
      /could not be read, so isolation is unverified/u,
    );
  } finally {
    for (const spawned of [child, server]) {
      if (spawned.exitCode === null && spawned.signalCode === null) spawned.kill("SIGKILL");
    }
  }
});

test("ENFORCEMENT: the application is judged on its own environment, not the display server's", async () => {
  // The range mutant passed the display server's pid for both children, which
  // deleted the application's credential check. Only a case where the
  // APPLICATION leaks and the display server is clean can see that.
  const { root, display, table } = pinnedPair();
  const environs = { 100: "PATH=/usr/bin\0PASEO_PASSWORD=super-secret\0", 200: "PATH=/usr/bin\0" };
  await assert.rejects(
    () => verifyRunIsolation({ userDataDir: "/p", paseoHome: "/h", root, display, ...isolationIo({ environs, table }) }),
    /the application \(pid 100\) carried PASEO_PASSWORD/u,
  );
});

test("ENFORCEMENT: claimDisplay pins the server's start time at spawn", async () => {
  // Without a pin there is nothing to re-prove the display server's identity
  // against, and the isolation check's second guard becomes vacuous.
  const owner = recordingTeardown();
  const io = { ...displayIo({ serverPid: process.pid, lockPid: process.pid }) };
  const claimed = await claimDisplay({ minimum: 120, maximum: 121 }, io, 50, owner);
  assert.equal(claimed.serverPid, process.pid);
  assert.equal(Number.isInteger(claimed.serverStartTime), true, "the pin must be read at spawn");
  assert.equal(claimed.serverStartTime, processStartTime(process.pid));
});

test("ENFORCEMENT: the first open retries rather than looking once", async () => {
  // Reducing the first open to a single look must fail: the Director entry
  // routinely appears several seconds after the surface is reachable.
  let looks = 0;
  const page = stubPage();
  const original = page.evaluate.bind(page);
  page.evaluate = async (fn) => {
    if (String(fn).includes("data-testid")) {
      looks += 1;
      return looks >= 3 ? ["plugin-sidebar-director-home"] : [];
    }
    return original(fn);
  };
  const outcome = await driveWith(page);
  assert.equal(outcome.directorTestId, "plugin-sidebar-director-home");
  assert.equal(looks >= 3, true, "the entry must be found by retrying, not on the first look");
});

// --- effects have production defaults, exercised with the argument absent ----
//
// The class this closes: a parameter with no default is supplied only where no
// test runs, so neutralising it at the composition root changes nothing any
// test can see. Eleven such parameters existed and all eleven survived
// mutation. Each injectable effect now defaults inside its own unit, and the
// tests below call each unit WITHOUT the argument so the real default runs.

test("ENFORCEMENT: verifyRunIsolation's own listDirectory sees an empty private directory", async () => {
  // The mutant was `listDirectory: () => ["ignored"]` at the call site, which
  // deleted the isolation guarantee stated one line above it.
  // Two genuinely empty private directories: the exact state of a build that
  // ignored both overrides.
  const { root, display, table } = pinnedPair();
  const base = mkdtempSync(joinPath(tmpdir(), "capture-isolation-"));
  try {
    await assert.rejects(
      () => verifyRunIsolation({
        userDataDir: base, paseoHome: base,
        root, display, attempts: 1, readTable: () => table,
        readEnviron: () => "PATH=/usr/bin\0",
        // listDirectory and wait deliberately absent: the real ones must run.
      }),
      /wrote nothing to its private/u,
      "the default listDirectory must really read the filesystem",
    );
    // Populated: the same default must then let the run proceed.
    writeFileSync(joinPath(base, "written-by-the-application"), "x");
    assert.equal(
      await verifyRunIsolation({
        userDataDir: base, paseoHome: base, root, display, attempts: 1,
        readTable: () => table, readEnviron: () => "PATH=/usr/bin\0",
      }),
      2,
    );
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test("ENFORCEMENT: createTeardown needs no effects from its caller", async () => {
  // `createTeardown()` with no argument must still be usable, so the caller has
  // no kill, readLock or removePath to neutralise.
  const unit = createTeardown();
  assert.equal(typeof unit.pinRoot, "function");
  assert.equal(typeof unit.runSync, "function");
  // No root and no display: signals nothing, removes nothing, and says so.
  const outcome = await unit.run();
  assert.deepEqual(outcome.targets, []);
  assert.equal(outcome.displayRemoved, false);
});

test("ENFORCEMENT: the signal and exit registrations default to the real process", () => {
  const before = process.listenerCount("SIGUSR2");
  const handlers = installSignalHandlers({ handler: () => {} });
  assert.equal(process.listenerCount("SIGUSR2"), before + 1, "the default `on` must be process.on");
  for (const [signal, bound] of handlers) process.off(signal, bound);
  assert.equal(process.listenerCount("SIGUSR2"), before);

  const exitBefore = process.listenerCount("exit");
  const bound = installExitFallback({ handler: () => {} });
  assert.equal(process.listenerCount("exit"), exitBefore + 1);
  process.off("exit", bound);
});

test("ENFORCEMENT: the run's secret comes from one derivation, not two arguments", () => {
  // `password` was a separate argument to two units, each neutralisable on its
  // own. Both now derive it from the environment through this one function.
  assert.equal(runPassword({ PASEO_PASSWORD: "hunter2" }), "hunter2");
  assert.equal(runPassword({}), "");
  assert.equal(runPassword(undefined), "");
  const artifacts = buildRunArtifacts({
    label: "x", userAgent: "u", display: ":120",
    outcome: { directorTestId: "d", reducedMotionActive: true, errorsBeforeProbe: 0 },
    rendererEvents: [], notes: [{ at: "t", text: "note hunter2" }],
    mainLog: ["log hun", "ter2"], environment: { PASEO_PASSWORD: "hunter2" },
  });
  for (const file of artifacts.files) assert.equal(file.contents.includes("hunter2"), false);
});

test("ENFORCEMENT: the surface routine writes its own screenshot paths", async () => {
  // `screenshot` was a closure the composition root supplied and could have
  // replaced with a no-op. The routine now builds it from page, output
  // directory and label.
  const written = [];
  const page = stubPage();
  await driveDirectorSurface({
    page, daemonHost: "127.0.0.1", daemonPort: 1,
    environment: {}, outputDirectory: "/tmp/evidence", label: "0.7.2",
    note: () => {}, errorCount: () => 0,
    writeScreenshot: (target, path) => { written.push(path); },
  });
  assert.equal(written.includes("/tmp/evidence/0.7.2-17-reduced-motion-icon.png"), true);
  assert.equal(written.every((path) => path.startsWith("/tmp/evidence/0.7.2-")), true);
});

// --- the remaining defaults, exercised against real resources ---------------

test("ENFORCEMENT: platformSignals reads the platform, not the policy table", () => {
  // A named required action from an earlier Review that I dropped: nothing
  // exercised the derivation, so rewriting the body to read SIGNAL_POLICY left
  // the suite green and the guarantee — that the domain comes from the system —
  // unproven.
  assert.deepEqual(platformSignals(), Object.keys(osConstants.constants.signals).sort());
  assert.notDeepEqual(platformSignals(), Object.keys(SIGNAL_POLICY).sort().filter((s) => s !== "SIGIOT"));
  // Injected: the parameter is honoured, which is what the eighth Review proved
  // externally with a loader hook.
  assert.deepEqual(platformSignals({ SIGB: 2, SIGA: 1 }), ["SIGA", "SIGB"]);
  assert.deepEqual(unclassifiedSignals(platformSignals({ SIGA: 1 })), ["SIGA"]);
});

test("ENFORCEMENT: teardown's own kill really signals, and its own removePath really removes", async () => {
  // createTeardown() takes no effects, so these defaults are the only place
  // left. A no-op kill or removePath would otherwise leave every test green.
  // Long-lived on purpose: a child that exits by itself would satisfy a weaker
  // assertion and let a no-op kill pass, which is exactly what happened when
  // this test used a thirty-second timer.
  const child = spawn(process.execPath, ["-e", "setTimeout(()=>{},300000)"], { stdio: "ignore" });
  await new Promise((resolve, reject) => { child.once("spawn", resolve); child.once("error", reject); });
  const base = mkdtempSync(joinPath(tmpdir(), "capture-teardown-"));
  const socket = joinPath(base, "X999");
  const lock = joinPath(base, ".X999-lock");
  writeFileSync(socket, "");
  writeFileSync(lock, `   ${process.pid}\n`);
  const unit = createTeardown({ wait: async () => {} });
  unit.pinRoot({ pid: child.pid, startTime: processStartTime(child.pid), exited: false });
  // The display record names this process, so the real readLock must confirm it.
  unit.adoptDisplay({ display: ":999", socket, lock, serverPid: process.pid, process: { kill() {} } });
  try {
    const outcome = await unit.run();
    assert.deepEqual(outcome.targets.map((target) => target.pid), [child.pid]);
    const exited = await Promise.race([
      new Promise((resolve) => child.once("exit", () => resolve(true))),
      new Promise((resolve) => setTimeout(() => resolve(false), 5000)),
    ]);
    assert.equal(exited, true, "the default kill must really signal, within a bounded wait");
    assert.equal(child.signalCode, "SIGTERM", "and it must be a signal, not a natural exit");
    assert.equal(outcome.displayRemoved, true, "the default readLock must confirm our own lock");
    assert.equal(existsSync(socket), false, "the default removePath must really remove");
    assert.equal(existsSync(lock), false);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    rmSync(base, { recursive: true, force: true });
  }
});

test("ENFORCEMENT: teardown's own readLock refuses a lock naming another server", async () => {
  const base = mkdtempSync(joinPath(tmpdir(), "capture-foreign-"));
  const socket = joinPath(base, "X998");
  const lock = joinPath(base, ".X998-lock");
  writeFileSync(socket, "");
  writeFileSync(lock, "   999999\n");
  try {
    const unit = createTeardown({ wait: async () => {} });
    unit.adoptDisplay({ display: ":998", socket, lock, serverPid: process.pid, process: { kill() {} } });
    const outcome = await unit.run();
    assert.equal(outcome.displayRemoved, false);
    assert.equal(existsSync(socket), true, "a live foreign server's files must survive");
    assert.equal(existsSync(lock), true);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test("ENFORCEMENT: the surface routine's own writeScreenshot really asks the page", async () => {
  const captured = [];
  const page = stubPage();
  page.screenshot = async ({ path }) => { captured.push(path); };
  await driveDirectorSurface({
    page, daemonHost: "127.0.0.1", daemonPort: 1,
    environment: {}, outputDirectory: "/tmp/evidence", label: "0.7.2",
    note: () => {}, errorCount: () => 0,
    // writeScreenshot deliberately absent: the real default must run.
  });
  assert.equal(captured.includes("/tmp/evidence/0.7.2-17-reduced-motion-icon.png"), true);
});

#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
//
// LOCAL-ONLY CAPTURE. The capture itself cannot run in CI; its dependency-free
// decision tests in ./capture.test.mjs do, and are part of `npm run ci`.
//
// Drives the real shipped Paseo desktop application (the packaged Electron
// binary, not a bundle executed against a module map) on a display this tool
// owns, opens the Director surface, and records what the real client bundle
// does with it. See ./README.md for why the capture cannot run on GitHub CI.
//
// Why teardown scans processes instead of signalling a process group: the
// application spawns its bundled daemon with setsid(), so that daemon leaves
// both our process group and our session. Measured on a shipped 0.7.2 build,
// the daemon's process group and session id both differ from the application's.
// Signalling a negative process group id would therefore silently stop cleaning
// up the one process the scan exists to catch. The scan is instead made safe by
// confirming identity: the spawned root is pinned by the start time read
// immediately after spawn, and nothing is signalled at all unless that pin
// still matches a live process.
//
// Every decision that protects the machine is a pure exported function tested
// in capture.test.mjs. The imperative shell below composes them and performs
// only effects.

import { spawn } from "node:child_process";
import {
  existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync,
} from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Applied inside displayCandidates itself. Not a parameter, not a default: a
// caller cannot thread a narrower list through and drop a physical console.
export const RESERVED_DISPLAY_NUMBERS = Object.freeze([0, 1, 2, 8]);

export const DEFAULT_DISPLAY_RANGE = Object.freeze({ minimum: 120, maximum: 199 });

export const LABEL_PATTERN = /^[A-Za-z0-9._-]+$/u;

/** The run's private directories are owner-only: they hold the Electron profile
 *  and the daemon home for a run that may carry a credential. */
export const PRIVATE_DIRECTORY_MODE = 0o700;

export function runDirectoryLayout(outputDirectory, label) {
  const runRoot = resolve(outputDirectory, `.run-${validateLabel(label)}`);
  return { runRoot, userDataDir: join(runRoot, "electron-user-data"), paseoHome: join(runRoot, "paseo-home") };
}

/** Every listener and every endpoint this tool creates is loopback-only: the
 *  bundled daemon must not be reachable off the machine, and the debugging
 *  endpoint exposes the application's whole renderer. */
export function loopbackListen(port) {
  return `127.0.0.1:${assertUsablePort(port)}`;
}

export function loopbackDebuggingEndpoint(port) {
  return `http://127.0.0.1:${assertUsablePort(port)}`;
}

function assertUsablePort(port) {
  if (!Number.isInteger(port) || port <= 0 || port > 65_535) {
    throw new Error(`port must be a valid TCP port, got ${port}`);
  }
  return port;
}

/** Source with comments removed, so a wiring contract cannot be satisfied by a
 *  call that has been commented out. */
export function stripComments(source) {
  return String(source)
    .replace(/\/\*[\s\S]*?\*\//gu, " ")
    .split("\n")
    .map((line) => {
      let quote = null;
      for (let index = 0; index < line.length; index += 1) {
        const character = line[index];
        if (quote !== null) {
          if (character === "\\") index += 1;
          else if (character === quote) quote = null;
          continue;
        }
        if (character === "\"" || character === "'" || character === "`") { quote = character; continue; }
        if (character === "/" && line[index + 1] === "/") return line.slice(0, index);
      }
      return line;
    })
    .join("\n");
}

// The plugin-side diagnostic emitted when an optional host primitive is absent
// and its defined fallback renders instead.
export const FALLBACK_WARNING = /optional host primitive .* is unavailable/iu;

export function scrubSecret(text, secret) {
  if (!secret) return String(text);
  return String(text).split(secret).join("<redacted>");
}

export function validateLabel(label) {
  if (typeof label !== "string" || !LABEL_PATTERN.test(label) || label === "." || label === "..") {
    throw new Error(`--label must match ${LABEL_PATTERN.source} and may not be "." or ".."`);
  }
  return label;
}

// The daemon password is typed into the connection form, so the form must be
// pointed at this machine. There is no opt-out: a remote target would send both
// the credential and this host's identity off the machine.
export function assertLoopbackHost(host) {
  const normalized = String(host ?? "").trim().toLowerCase();
  const named = new Set(["localhost", "::1", "[::1]"]);
  const dotted = /^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/u.exec(normalized);
  const octetsValid = dotted !== null && dotted.slice(1).every((part) => Number(part) <= 255);
  if (!named.has(normalized) && !octetsValid) {
    throw new Error(
      `--daemon-host must be a loopback address, got "${host}". This tool types a daemon ` +
      "password into the connection form and must not send it, or this host's identity, off the machine.",
    );
  }
  return normalized;
}

/** The server pid recorded in an X lock file. The X server writes it
 *  right-aligned in a fixed-width field with a trailing newline, for example
 *  "   2470092\n". Returns null for anything that is not a plain pid. */
export function parseDisplayLockPid(content) {
  if (typeof content !== "string") return null;
  const trimmed = content.trim();
  if (!/^\d+$/u.test(trimmed)) return null;
  const pid = Number(trimmed);
  return Number.isSafeInteger(pid) && pid > 0 ? pid : null;
}

/** True only when the X lock for this display names our own server process.
 *  Checked before adopting a display and again before removing its files, so a
 *  foreign server that took the number is never adopted and never deleted. */
export function displayLockOwnedBy(lockPath, pid, readLock = defaultReadLock) {
  return parseDisplayLockPid(readLock(lockPath)) === pid;
}

function defaultReadLock(lockPath) {
  try {
    return readFileSync(lockPath, "utf8");
  } catch {
    return null;
  }
}

export function occupiedDisplayNumbers(socketDirectory = "/tmp/.X11-unix", lockDirectory = "/tmp") {
  const numbers = new Set();
  const collect = (directory, pattern) => {
    let entries;
    try {
      entries = readdirSync(directory);
    } catch {
      return;
    }
    for (const entry of entries) {
      const match = pattern.exec(entry);
      if (match) numbers.add(Number(match[1]));
    }
  };
  collect(socketDirectory, /^X(\d+)$/u);
  collect(lockDirectory, /^\.X(\d+)-lock$/u);
  return numbers;
}

export function displayCandidates({ minimum, maximum, occupied = new Set() }) {
  if (!Number.isInteger(minimum) || !Number.isInteger(maximum) || minimum > maximum) {
    throw new Error("display range must be a valid integer interval");
  }
  const refused = new Set(RESERVED_DISPLAY_NUMBERS);
  const candidates = [];
  for (let number = minimum; number <= maximum; number += 1) {
    if (refused.has(number) || occupied.has(number)) continue;
    candidates.push(number);
  }
  return candidates;
}

// The application must never inherit the ambient Paseo or Director environment:
// that would point its bundled daemon at the operator's PASEO_HOME and could
// hand it a credential. Everything in both namespaces is removed first, so a
// variable added in future is excluded without editing a list.
export function applicationEnvironment(base, { display, paseoHome, userDataDir, listen, cdpPort }) {
  const environment = {};
  for (const [name, value] of Object.entries(base)) {
    if (/^(?:PASEO|DIRECTOR)_/u.test(name)) continue;
    environment[name] = value;
  }
  environment.DISPLAY = display;
  environment.PASEO_HOME = paseoHome;
  environment.PASEO_LISTEN = listen;
  environment.PASEO_ELECTRON_USER_DATA_DIR = userDataDir;
  environment.PASEO_DISABLE_SINGLE_INSTANCE_LOCK = "1";
  environment.PASEO_ELECTRON_FLAGS = `--remote-debugging-port=${cdpPort}`;
  return environment;
}

/** Field 22 of /proc/<pid>/stat. The command name may contain spaces and
 *  parentheses, so parsing starts after its closing parenthesis. */
export function parseProcessStartTime(statContent) {
  if (typeof statContent !== "string") return null;
  const close = statContent.lastIndexOf(")");
  if (close === -1) return null;
  const fields = statContent.slice(close + 1).trim().split(/\s+/u);
  const startTime = fields[19];
  if (startTime === undefined || !/^\d+$/u.test(startTime)) return null;
  return Number(startTime);
}

export function descendantPids(rootPid, parentByPid) {
  const childrenByParent = new Map();
  for (const [pid, parent] of parentByPid) {
    if (!childrenByParent.has(parent)) childrenByParent.set(parent, []);
    childrenByParent.get(parent).push(pid);
  }
  const found = new Set();
  const queue = [rootPid];
  while (queue.length > 0) {
    for (const child of childrenByParent.get(queue.shift()) ?? []) {
      if (child === rootPid || found.has(child)) continue;
      found.add(child);
      queue.push(child);
    }
  }
  return found;
}

/** True only when the pinned root is still the process this run spawned.
 *  `root.startTime` must have been read at spawn, never from the same snapshot
 *  the comparison uses, or the check compares a value against itself. */
export function rootIsConfirmed(root, processTable) {
  if (root === null || root === undefined) return false;
  if (root.exited === true) return false;
  if (!Number.isInteger(root.pid) || root.pid <= 1) return false;
  if (!Number.isInteger(root.startTime)) return false;
  const live = processTable?.startTimeByPid?.get(root.pid);
  return live !== undefined && live === root.startTime;
}

/** The processes teardown may signal, each carried WITH the identity that
 *  authorised it as `{ pid, startTime }`.
 *
 *  Pure and injectable so the property can be tested without spawning: it takes
 *  the identities recorded during the run and a process-table snapshot, and
 *  decides alone.
 *
 *  Two independent proofs, deliberately not chained. The root and anything
 *  derived from the live parent map are authorised only while the pin taken at
 *  spawn still matches, because a dead or recycled root proves nothing about
 *  its apparent children. Descendants recorded earlier were recorded only while
 *  the root was confirmed and carry their own recorded start time, so they
 *  prove themselves and remain in scope even after the root has exited. That
 *  matters: the bundled daemon is spawned with setsid() and outlives the
 *  application, so gating it on root liveness would abandon the one process
 *  this scan exists to catch. */
export function confirmedTeardownTargets({ root, tracked = new Map(), processTable, selfPid = process.pid }) {
  const { parentByPid = new Map(), startTimeByPid = new Map() } = processTable ?? {};
  const targets = new Map();
  const admit = (pid, startTime) => {
    if (!Number.isInteger(pid) || pid <= 1 || pid === selfPid) return;
    if (startTime === undefined) return;
    targets.set(pid, startTime);
  };
  if (rootIsConfirmed(root, processTable)) {
    admit(root.pid, root.startTime);
    for (const pid of descendantPids(root.pid, parentByPid)) admit(pid, startTimeByPid.get(pid));
  }
  for (const [pid, startTime] of tracked) {
    if (startTimeByPid.get(pid) === startTime) admit(pid, startTime);
  }
  return [...targets.entries()]
    .map(([pid, startTime]) => ({ pid, startTime }))
    .sort((left, right) => left.pid - right.pid);
}

/** The subset of identity-carrying targets that are STILL the same processes.
 *
 *  The signal site must re-prove identity every round, not merely check that
 *  the number is present: signalling frees pids, so between rounds a target's
 *  number can be taken by an unrelated process. */
export function survivingTargets(targets, processTable) {
  const startTimeByPid = processTable?.startTimeByPid ?? new Map();
  return targets.filter((target) => startTimeByPid.get(target.pid) === target.startTime);
}

/** Escalate SIGTERM to SIGKILL, re-confirming identity before every signal.
 *
 *  Injectable rather than inlined so the escalation itself is covered: the
 *  round-by-round re-confirmation is the property, and it lives here. Returns
 *  the targets still alive and still ours after the last round. */
export async function terminateTargets({
  targets, readTable, kill, wait, signals = ["SIGTERM", "SIGTERM", "SIGKILL"],
}) {
  let remaining = targets;
  for (const signal of signals) {
    remaining = survivingTargets(remaining, readTable());
    if (remaining.length === 0) return [];
    for (const target of remaining) {
      try {
        kill(target.pid, signal);
      } catch {
        // Already gone between confirming and signalling.
      }
    }
    await wait();
  }
  return survivingTargets(remaining, readTable());
}

export function summarizeRendererEvents(rendererEvents) {
  const errors = rendererEvents.filter(
    (event) => event.kind === "pageerror" || (event.kind === "console" && event.type === "error"),
  );
  return {
    rendererErrorsTotal: errors.length,
    react130: errors.some((event) => event.text.includes("React error #130")),
    fallbackWarnings: rendererEvents.filter(
      (event) => event.kind === "console" && FALLBACK_WARNING.test(event.text),
    ).length,
  };
}

export function resolveApplicationExecutable(target) {
  const absolute = resolve(target);
  if (!existsSync(absolute)) throw new Error(`application path does not exist: ${absolute}`);
  if (statSync(absolute).isDirectory()) {
    const candidate = join(absolute, "Paseo");
    if (!existsSync(candidate)) throw new Error(`no Paseo executable inside ${absolute}`);
    return candidate;
  }
  return absolute;
}

export function parseArguments(argv) {
  const options = {
    app: null,
    out: null,
    label: null,
    daemonHost: "127.0.0.1",
    daemonPort: null,
    displayMinimum: DEFAULT_DISPLAY_RANGE.minimum,
    displayMaximum: DEFAULT_DISPLAY_RANGE.maximum,
    keepUserData: false,
  };
  const numeric = new Set(["daemonPort", "displayMinimum", "displayMaximum"]);
  const names = new Map([
    ["--app", "app"],
    ["--out", "out"],
    ["--label", "label"],
    ["--daemon-host", "daemonHost"],
    ["--daemon-port", "daemonPort"],
    ["--display-min", "displayMinimum"],
    ["--display-max", "displayMaximum"],
  ]);
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    if (token === "--keep-user-data") {
      options.keepUserData = true;
      continue;
    }
    const name = names.get(token);
    if (name === undefined) throw new Error(`unknown argument: ${token}`);
    const value = argv[index + 1];
    if (value === undefined) throw new Error(`${token} requires a value`);
    index += 1;
    if (numeric.has(name)) {
      const parsed = Number(value);
      if (!Number.isInteger(parsed)) throw new Error(`${token} requires an integer`);
      options[name] = parsed;
      continue;
    }
    options[name] = value;
  }
  for (const required of ["app", "out", "daemonPort"]) {
    if (options[required] === null) {
      throw new Error(`--${required.replace(/[A-Z]/gu, (letter) => `-${letter.toLowerCase()}`)} is required`);
    }
  }
  options.label = validateLabel(options.label ?? "capture");
  options.daemonHost = assertLoopbackHost(options.daemonHost);
  return options;
}

function sleep(milliseconds) {
  return new Promise((done) => setTimeout(done, milliseconds));
}

async function freePort() {
  const net = await import("node:net");
  return new Promise((done, fail) => {
    const server = net.createServer();
    server.on("error", fail);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => done(port));
    });
  });
}

export function processStartTime(pid) {
  try {
    return parseProcessStartTime(readFileSync(`/proc/${pid}/stat`, "utf8"));
  } catch {
    return null;
  }
}

function readProcessTable() {
  const parentByPid = new Map();
  const startTimeByPid = new Map();
  for (const entry of readdirSync("/proc")) {
    if (!/^\d+$/u.test(entry)) continue;
    let stat;
    try {
      stat = readFileSync(`/proc/${entry}/stat`, "utf8");
    } catch {
      continue;
    }
    const close = stat.lastIndexOf(")");
    if (close === -1) continue;
    const parent = Number(stat.slice(close + 1).trim().split(/\s+/u)[1]);
    if (!Number.isInteger(parent)) continue;
    parentByPid.set(Number(entry), parent);
    const startTime = parseProcessStartTime(stat);
    if (startTime !== null) startTimeByPid.set(Number(entry), startTime);
  }
  return { parentByPid, startTimeByPid };
}

// Claiming a display is a race: another process may take the number between the
// check and the launch. The X server creates its lock with O_EXCL, so the only
// reliable claim is to try to start and move on when it refuses — and the lock
// records the server's pid, so ownership is proved rather than assumed.
//
// The effects are injected so the adopt decision is exercised by tests at the
// point it is ENFORCED. Mutating the condition below must fail the suite; a
// test of the predicate alone would not notice.
export const defaultDisplayIo = {
  spawnServer: (number) => spawn(
    "Xvfb",
    [`:${number}`, "-screen", "0", "1600x1000x24", "-nolisten", "tcp", "-noreset"],
    { stdio: ["ignore", "ignore", "ignore"] },
  ),
  socketExists: (path) => existsSync(path),
  readLock: (path) => defaultReadLock(path),
  occupied: () => occupiedDisplayNumbers(),
  wait: (ms) => sleep(ms),
};

export async function claimDisplay({ minimum, maximum }, io = defaultDisplayIo, attempts = 50, teardown) {
  // The server is handed to teardown the instant it is spawned, not when this
  // function returns. It is live for up to several seconds while the socket and
  // lock appear, and an interruption in that window would otherwise orphan a
  // running X server together with its socket and lock.
  if (teardown === undefined) throw new Error("claimDisplay requires the teardown that will own the server");
  for (const number of displayCandidates({ minimum, maximum, occupied: io.occupied() })) {
    const socket = `/tmp/.X11-unix/X${number}`;
    const lock = `/tmp/.X${number}-lock`;
    const child = io.spawnServer(number);
    const record = { number, display: `:${number}`, process: child, socket, lock, serverPid: child.pid };
    teardown.adoptDisplay(record);
    let failed = false;
    child.on("exit", () => { failed = true; });
    child.on("error", () => { failed = true; });
    for (let attempt = 0; attempt < attempts && !failed; attempt += 1) {
      // The socket appearing is not proof the display is ours: another server
      // may have taken the number between the occupancy sample and this launch.
      if (io.socketExists(socket) && parseDisplayLockPid(io.readLock(lock)) === child.pid) {
        return record;
      }
      await io.wait(100);
    }
    await teardown.releaseDisplay();
  }
  throw new Error(`no free display between :${minimum} and :${maximum}`);
}

/** The teardown composition, owning the identities it will later act on.
 *
 *  This exists because the previous three defects on this tool all lived in the
 *  composition rather than in the predicates: the selector was correct and the
 *  caller undid it. So the state lives here, behind tested code, instead of in
 *  the caller as parameters that a later edit can stop passing:
 *
 *    - the root pin is stored once by `pinRoot` and only ever read afterwards,
 *      so it cannot be re-derived from the snapshot the comparison uses;
 *    - `tracked` is internal, so there is no call-site argument to drop;
 *    - the display's ownership is re-proved here before anything is removed.
 *
 *  Every effect is injected, so all of the above is exercised by tests at the
 *  point it is enforced. */
export function createTeardown({ readTable, kill, wait, readLock, removePath }) {
  const tracked = new Map();
  let root = null;
  let display = null;

  const trackDescendants = () => {
    if (root === null) return;
    const processTable = readTable();
    if (!rootIsConfirmed(root, processTable)) return;
    for (const pid of descendantPids(root.pid, processTable.parentByPid)) {
      const startTime = processTable.startTimeByPid.get(pid);
      if (!tracked.has(pid) && startTime !== undefined) tracked.set(pid, startTime);
    }
  };

  const stopDisplay = async (record) => {
    if (record === null) return false;
    record.process.kill("SIGTERM");
    await wait();
    record.process.kill("SIGKILL");
    // Re-proved here, not assumed: if our server died and another took the
    // number, these files belong to that server and must not be removed.
    const ours = parseDisplayLockPid(readLock(record.lock)) === record.serverPid;
    if (ours) {
      removePath(record.socket);
      removePath(record.lock);
    }
    return ours;
  };

  return {
    pinRoot(value) { root = value; },
    adoptDisplay(value) { display = value; },
    /** Stop and clear a display this run spawned but did not keep. Used by the
     *  claiming loop when it abandons a candidate, so an abandoned server is
     *  never left behind either. */
    async releaseDisplay() {
      const released = await stopDisplay(display);
      display = null;
      return released;
    },
    trackDescendants,
    trackedSnapshot: () => new Map(tracked),
    async run({ removeRunRoot = () => {} } = {}) {
      trackDescendants();
      const targets = confirmedTeardownTargets({ root, tracked, processTable: readTable() });
      const survivors = await terminateTargets({ targets, readTable, kill, wait });
      const displayRemoved = await stopDisplay(display);
      removeRunRoot();
      return { targets, survivors, displayRemoved };
    },
  };
}

export function spawnApplication(executable, baseEnvironment, isolation, registerRoot) {
  // The pin is handed over inside this function, before the promise resolves.
  // A caller that registered it afterwards would leave the application, and
  // later its setsid'd daemon, live but invisible to teardown in between.
  if (typeof registerRoot !== "function") {
    throw new Error("spawnApplication requires a function that records the spawned root");
  }
  // The filter is applied here rather than by the caller: a caller that forgot
  // it would hand the application the ambient Paseo environment, including a
  // credential. Applying it at the spawn itself removes that seam.
  const environment = applicationEnvironment(baseEnvironment, isolation);
  return new Promise((done, fail) => {
    const child = spawn(executable, [], { env: environment, stdio: ["ignore", "pipe", "pipe"] });
    const onError = (error) => fail(error);
    child.once("error", onError);
    child.once("spawn", () => {
      child.off("error", onError);
      const root = { pid: child.pid, startTime: processStartTime(child.pid), exited: false };
      child.once("exit", () => { root.exited = true; });
      child.on("error", () => { root.exited = true; });
      if (!Number.isInteger(root.startTime)) {
        child.kill("SIGKILL");
        fail(new Error(`could not read the start time of the spawned process ${child.pid}; refusing to continue`));
        return;
      }
      registerRoot(root);
      done({ child, root, environment });
    });
  });
}

export async function driveDirectorSurface({ page, daemonHost, daemonPort, password, screenshot, note, errorCount }) {
  const testIds = () => page.evaluate(() =>
    Array.from(document.querySelectorAll("[data-testid]")).map((element) => element.getAttribute("data-testid")));
  const bodyText = () => page.evaluate(() => document.body.innerText);

  await page.waitForSelector('[data-testid="sidebar-hosts-trigger"]', { timeout: 120_000 });
  await screenshot("10-booted");

  // Direct connection only. The external pairing flow is never used, so no
  // daemon or server identity leaves the machine.
  await page.click('[data-testid="sidebar-hosts-trigger"]');
  await page.waitForSelector('[data-testid="sidebar-host-add"]', { timeout: 30_000 });
  await page.click('[data-testid="sidebar-host-add"]');
  await page.waitForSelector('[data-testid="add-host-method-direct"]', { timeout: 30_000 });
  await page.click('[data-testid="add-host-method-direct"]');
  await page.waitForSelector('[data-testid="direct-host-input"]', { timeout: 30_000 });
  await page.fill('[data-testid="direct-host-input"]', daemonHost);
  await page.fill('[data-testid="direct-port-input"]', String(daemonPort));
  if (password) {
    await page.fill('[data-testid="direct-password-input"]', password);
    // Verified before the capture, never assumed: a build that renders this
    // field unmasked must not have its secret written to the evidence file.
    const inputType = await page.getAttribute('[data-testid="direct-password-input"]', "type");
    if (inputType !== "password") {
      throw new Error(`refusing to capture the connection form: the password field is type "${inputType}", not masked`);
    }
  }
  await screenshot("11-direct-form-filled");

  await page.click('[data-testid="direct-host-submit"]');
  await page.waitForTimeout(9000);
  await screenshot("12-after-submit");
  await page.click('[data-testid="settings-back-to-workspace"]');
  await page.waitForTimeout(5000);
  await screenshot("13-connected");

  let directorTestId = null;
  for (let attempt = 0; attempt < 40 && directorTestId === null; attempt += 1) {
    directorTestId = (await testIds()).find((value) => /director/iu.test(value ?? "")) ?? null;
    if (directorTestId === null) await page.waitForTimeout(2000);
  }
  if (directorTestId === null) throw new Error("the Director sidebar entry never registered");
  note(`director sidebar entry: ${directorTestId}`);

  await page.click(`[data-testid="${directorTestId}"]`);
  await page.waitForTimeout(8000);
  await screenshot("14-director-surface-initial");
  for (let attempt = 0; attempt < 40; attempt += 1) {
    if (!(await bodyText()).includes("Loading Director Home")) break;
    await page.waitForTimeout(3000);
  }
  await screenshot("15-director-surface");
  note(`surface text: ${(await bodyText()).slice(0, 1200)}`);

  // The snapshot-backed Icon call sites need an engine projection. The
  // reduced-motion branch of Director Home renders the optional host primitive
  // without one, so it is the reachable way to exercise the primitive itself.
  const errorsBeforeProbe = errorCount();
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.waitForTimeout(3000);
  await screenshot("16-reduced-motion-live");
  await page.reload({ waitUntil: "load" });
  await page.waitForSelector('[data-testid="sidebar-hosts-trigger"]', { timeout: 120_000 });
  await page.waitForTimeout(4000);
  const reopened = (await testIds()).find((value) => /director/iu.test(value ?? "")) ?? null;
  if (reopened !== null) {
    await page.click(`[data-testid="${reopened}"]`);
    await page.waitForTimeout(12_000);
    await screenshot("17-reduced-motion-icon");
  }
  // Asserted, not assumed: without this the probe silently degrades into a
  // second ordinary capture and the result would overstate what ran.
  const reducedMotionActive = await page.evaluate(() =>
    window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  if (!reducedMotionActive) {
    throw new Error("reduced motion never took effect, so the Icon probe did not run");
  }
  note(`probe surface text: ${(await bodyText()).slice(0, 800)}`);

  return { directorTestId, reducedMotionActive, errorsBeforeProbe };
}

async function loadPlaywright(environment) {
  const specifier = environment.PASEO_DESKTOP_PLAYWRIGHT;
  const target = specifier ? pathToFileURL(join(resolve(specifier), "index.mjs")).href : "playwright";
  try {
    return await import(target);
  } catch (cause) {
    throw new Error(
      "Playwright could not be loaded. This tool deliberately does not declare " +
      "Playwright as a repository dependency; point PASEO_DESKTOP_PLAYWRIGHT at " +
      `an existing Playwright package directory. Tried: ${target}`,
      { cause },
    );
  }
}

/** Both private directories the application must actually use. Declared here so
 *  a build that silently ignores either override cannot pass as isolated, and
 *  so dropping one from the check is a visible change to tested code. */
export function isolationTargets({ userDataDir, paseoHome }) {
  return [
    { directory: userDataDir, label: "Electron user-data directory" },
    { directory: paseoHome, label: "PASEO_HOME" },
  ];
}

export async function verifyIsolation(targets, { listDirectory, wait, attempts = 60 }) {
  for (const { directory, label } of targets) {
    let populated = false;
    for (let attempt = 0; attempt < attempts && !populated; attempt += 1) {
      if (listDirectory(directory).length > 0) populated = true;
      else await wait(500);
    }
    if (!populated) {
      throw new Error(
        `the application wrote nothing to its private ${label} ${directory}; this build appears to ` +
        "ignore the override and the run is not isolated",
      );
    }
  }
  return targets.length;
}

/** Registers the same teardown for interruption as for a normal exit. */
export function installSignalHandlers({ on, handler, signals = ["SIGINT", "SIGTERM"] }) {
  const installed = signals.map((signal) => [signal, () => handler(signal)]);
  for (const [signal, bound] of installed) on(signal, bound);
  return installed;
}

/** The run's durable artifacts. Built here rather than at the write site so the
 *  properties that matter — the application log is scrubbed, and metrics are
 *  computed only from renderer observations — are enforced in tested code. */
export function buildRunArtifacts({ label, userAgent, display, outcome, rendererEvents, notes, mainLog, password }) {
  const summary = summarizeRendererEvents(rendererEvents);
  const result = {
    label,
    userAgent,
    display,
    directorSidebarTestId: outcome.directorTestId,
    reducedMotionActive: outcome.reducedMotionActive,
    fallbackWarnings: summary.fallbackWarnings,
    rendererErrorsTotal: summary.rendererErrorsTotal,
    rendererErrorsBeforeProbe: outcome.errorsBeforeProbe,
    rendererErrorsDuringProbe: summary.rendererErrorsTotal - outcome.errorsBeforeProbe,
    react130: summary.react130,
  };
  const transcript = [
    ...rendererEvents.map((event) => `[${event.at}] ${event.kind}.${event.type}: ${event.text}`),
    ...notes.map((entry) => `[${entry.at}] note: ${entry.text}`),
  ].sort();
  // Every emitted file is scrubbed here, not only the application log. The
  // caller also scrubs upstream, but an evidence-file property must not depend
  // on a caller remembering to.
  return {
    result,
    files: [
      { name: `${label}-result.json`, contents: `${JSON.stringify(result, null, 2)}\n` },
      { name: `${label}-console.log`, contents: `${transcript.join("\n")}\n` },
      { name: `${label}-app-main.log`, contents: mainLog.join("") },
    ].map((file) => ({ name: file.name, contents: scrubSecret(file.contents, password) })),
  };
}

export async function main(argv = process.argv.slice(2), environment = process.env) {
  const options = parseArguments(argv);
  const executable = resolveApplicationExecutable(options.app);
  const outputDirectory = resolve(options.out);
  const { runRoot, userDataDir, paseoHome } = runDirectoryLayout(outputDirectory, options.label);

  const password = environment.PASEO_PASSWORD ?? "";
  const rendererEvents = [];
  const notes = [];
  const stamp = () => new Date().toISOString();
  const note = (text) => { notes.push({ at: stamp(), text: scrubSecret(text, password) }); };
  const errorCount = () => summarizeRendererEvents(rendererEvents).rendererErrorsTotal;

  let child = null;
  let browser = null;

  const teardown = createTeardown({
    readTable: readProcessTable,
    kill: (pid, signal) => process.kill(pid, signal),
    wait: () => sleep(2000),
    readLock: (path) => defaultReadLock(path),
    removePath: (path) => rmSync(path, { force: true }),
  });

  const performCleanup = async () => {
    if (browser !== null) await browser.close().catch(() => {});
    const outcome = await teardown.run({
      removeRunRoot: () => {
        if (!options.keepUserData) rmSync(runRoot, { recursive: true, force: true });
      },
    });
    if (child !== null && child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    if (outcome.survivors.length > 0) {
      console.error(`warning: processes from this run did not stop: ${outcome.survivors.map((t) => t.pid).join(", ")}`);
    }
  };

  let cleanupPromise = null;
  const cleanup = () => {
    if (cleanupPromise === null) cleanupPromise = performCleanup();
    return cleanupPromise;
  };
  const onSignal = (signal) => {
    cleanup().finally(() => process.exit(signal === "SIGINT" ? 130 : 143));
  };
  // Installed before anything exists, so no resource is ever created while an
  // interruption would bypass teardown entirely.
  const handlers = installSignalHandlers({
    on: (signal, bound) => process.on(signal, bound),
    handler: onSignal,
  });

  mkdirSync(outputDirectory, { recursive: true });
  rmSync(runRoot, { recursive: true, force: true });
  for (const directory of [runRoot, userDataDir, paseoHome]) {
    mkdirSync(directory, { recursive: true, mode: PRIVATE_DIRECTORY_MODE });
  }

  try {
    const display = await claimDisplay(
      { minimum: options.displayMinimum, maximum: options.displayMaximum },
      defaultDisplayIo,
      50,
      teardown,
    );
    note(`claimed display ${display.display}`);

    const cdpPort = await freePort();
    const daemonListen = loopbackListen(await freePort());
    const launched = await spawnApplication(
      executable,
      environment,
      { display: display.display, paseoHome, userDataDir, listen: daemonListen, cdpPort },
      (root) => teardown.pinRoot(root),
    );
    child = launched.child;
    note(`spawned application pid ${launched.root.pid}`);
    // Raw chunks, joined and scrubbed once at write time: a secret split across
    // a chunk boundary would survive per-chunk scrubbing.
    const mainLog = [];
    for (const stream of [child.stdout, child.stderr]) {
      stream.on("data", (chunk) => mainLog.push(chunk.toString()));
    }

    let version = null;
    for (let attempt = 0; attempt < 120 && version === null; attempt += 1) {
      await sleep(500);
      teardown.trackDescendants();
      try {
        const response = await fetch(`${loopbackDebuggingEndpoint(cdpPort)}/json/version`);
        if (response.ok) version = await response.json();
      } catch {
        // The debugging endpoint is not listening yet.
      }
    }
    if (version === null) throw new Error(`the application never exposed a debugging endpoint on port ${cdpPort}`);
    note(`application user agent: ${version["User-Agent"]}`);

    // Verified, not assumed: both overrides fail silently on a build that
    // ignores them, and an unisolated run must not pass as an isolated one.
    await verifyIsolation(isolationTargets({ userDataDir, paseoHome }), {
      listDirectory: (directory) => readdirSync(directory),
      wait: (ms) => sleep(ms),
    });

    const { chromium } = await loadPlaywright(environment);
    browser = await chromium.connectOverCDP(loopbackDebuggingEndpoint(cdpPort));
    const context = browser.contexts()[0];
    let page = null;
    for (let attempt = 0; attempt < 60 && page === null; attempt += 1) {
      page = context.pages().find((candidate) => candidate.url().startsWith("paseo://")) ?? null;
      if (page === null) await sleep(500);
    }
    if (page === null) throw new Error("the application window never became available");
    page.on("console", (message) => rendererEvents.push({
      at: stamp(), kind: "console", type: message.type(), text: scrubSecret(message.text(), password),
    }));
    page.on("pageerror", (error) => rendererEvents.push({
      at: stamp(), kind: "pageerror", type: "error", text: scrubSecret(error.message, password),
    }));

    const screenshot = async (name) => {
      await page.screenshot({ path: join(outputDirectory, `${options.label}-${name}.png`) });
    };
    const outcome = await driveDirectorSurface({
      page,
      daemonHost: options.daemonHost,
      daemonPort: options.daemonPort,
      password,
      screenshot,
      note,
      errorCount,
    });
    teardown.trackDescendants();

    const artifacts = buildRunArtifacts({
      label: options.label,
      userAgent: version["User-Agent"],
      display: display.display,
      outcome,
      rendererEvents,
      notes,
      mainLog,
      password,
    });
    for (const file of artifacts.files) writeFileSync(join(outputDirectory, file.name), file.contents);
    const result = artifacts.result;
    console.log(JSON.stringify(result, null, 2));
    return result;
  } finally {
    await cleanup();
    for (const [signal, handler] of handlers) process.off(signal, handler);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

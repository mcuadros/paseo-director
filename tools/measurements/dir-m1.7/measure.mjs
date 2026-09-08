#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { readFileSync, renameSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

const root = required("DIRECTOR_MEASURE_ROOT");
const controlPath = `${root}/mode.json`;
const logPath = `${root}/requests.jsonl`;
const resultPath = `${root}/measurement.json`;
const cdpOrigin = required("DIRECTOR_MEASURE_CDP_ORIGIN");
const paseoOrigin = required("DIRECTOR_MEASURE_PASEO_ORIGIN");
const paseoHost = required("DIRECTOR_MEASURE_PASEO_HOST");
const pluginId = required("DIRECTOR_MEASURE_PLUGIN_ID");

function setMode(kind) {
  const temporary = `${controlPath}.partial`;
  writeFileSync(temporary, `${JSON.stringify({ kind })}\n`, { mode: 0o600 });
  renameSync(temporary, controlPath);
}

function requests() {
  try {
    return readFileSync(logPath, "utf8")
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  } catch (error) {
    if (error?.code === "ENOENT") return [];
    throw error;
  }
}

const target = await fetch(
  `${cdpOrigin}/json/new?${encodeURIComponent(paseoOrigin)}`,
  { method: "PUT" },
).then((response) => response.json());
const socket = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  socket.addEventListener("open", resolve, { once: true });
  socket.addEventListener("error", reject, { once: true });
});
let nextId = 1;
const pending = new Map();
socket.addEventListener("message", (event) => {
  const message = JSON.parse(String(event.data));
  if (!message.id) return;
  const handler = pending.get(message.id);
  if (!handler) return;
  pending.delete(message.id);
  if (message.error) handler.reject(new Error(JSON.stringify(message.error)));
  else handler.resolve(message.result);
});

function send(method, params = {}) {
  const id = nextId++;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params }));
  });
}

async function evaluate(expression) {
  const result = await send("Runtime.evaluate", {
    expression,
    returnByValue: true,
    awaitPromise: true,
  });
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.text ?? "browser evaluation failed");
  }
  return result.result.value;
}

async function waitFor(label, predicate, timeoutMs = 15_000) {
  const startedAt = Date.now();
  let last;
  while (Date.now() - startedAt < timeoutMs) {
    last = await evaluate("document.body?.innerText ?? ''");
    if (predicate(last)) return { at: Date.now(), text: last };
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`timed out waiting for ${label}: ${JSON.stringify(last)}`);
}

await send("Runtime.enable");
await send("Page.enable");
await waitFor("sidebar contribution", (text) => text.includes("Board measurement"));
const clicked = await evaluate(`(() => {
  const candidates = [...document.querySelectorAll("*")].filter(
    (element) => element.textContent?.trim() === "Board measurement",
  );
  const target = candidates.at(-1);
  if (!target) return false;
  target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  return true;
})()`);
if (!clicked) throw new Error("Board measurement sidebar item was not clickable");

const loading = await waitFor(
  "loading state",
  (text) => text.includes("Loading project tasks"),
);
const empty = await waitFor("empty state", (text) => text.includes("No tasks yet"));
setMode("data");
const data = await waitFor("data state", (text) => text.includes("Measured Board task"));
const dataRequest = requests().findLast((entry) => entry.kind === "data");
if (!dataRequest) throw new Error("data request was not observed");

setMode("error");
let errorRequest;
const errorDeadline = Date.now() + 8_000;
while (Date.now() < errorDeadline) {
  errorRequest = requests().findLast((entry) => entry.kind === "error");
  if (errorRequest) break;
  await new Promise((resolve) => setTimeout(resolve, 50));
}
if (!errorRequest) throw new Error("scheduled error refresh was not observed");
await new Promise((resolve) => setTimeout(resolve, 150));
const cachedText = await evaluate("document.body?.innerText ?? ''");
if (!cachedText.includes("Measured Board task") || cachedText.includes("Board data is unavailable")) {
  throw new Error("cached data was not preserved after refresh failure");
}

const reload = spawnSync(
  "paseo",
  ["plugin", "reload", pluginId, "--host", paseoHost, "--json"],
  { encoding: "utf8", timeout: 30_000 },
);
if (reload.status !== 0) throw new Error("plugin reload failed");
const error = await waitFor(
  "initial error state after query-cache reset",
  (text) => text.includes("Board data is unavailable"),
);

const result = {
  schemaVersion: 1,
  tuple: {
    paseo: required("DIRECTOR_MEASURE_PASEO_VERSION"),
    browser: required("DIRECTOR_MEASURE_BROWSER_VERSION"),
    host: "disposable passwordless loopback daemon with bundled web UI",
    plugin: "generated exact-version fixture mounting repository ProjectBoard",
  },
  scenes: {
    loading: loading.at,
    empty: empty.at,
    data: data.at,
    cachedRefreshFailurePreservedData: true,
    initialErrorAfterReload: error.at,
  },
  refresh: {
    dataRequestAt: dataRequest.at,
    errorRequestAt: errorRequest.at,
    intervalMs: errorRequest.at - dataRequest.at,
    configuredMs: 2_000,
  },
  requests: requests(),
  providerFailureObserved: false,
  queryClientProviderPathExercised: true,
  result: "pass",
};
writeFileSync(resultPath, `${JSON.stringify(result, null, 2)}\n`, { mode: 0o600 });
console.log(JSON.stringify(result));
await send("Target.closeTarget", { targetId: target.id });
socket.close();

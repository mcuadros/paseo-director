// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  boundedTransportChildCode,
  classifyPastaCapability,
  selectReleaseRuntimeTransport,
} from "./release-runtime-lifecycle.mjs";

const scriptPath = "/test/release-runtime-lifecycle.mjs";
const nodeExecutable = "/test/node";
const denied = [
  "Couldn't write to /proc/self/uid_map: Operation not permitted",
  "Couldn't configure user mappings",
  "clone: Operation not permitted",
].join("\n");

test("admitted pasta remains the preferred isolated transport with fixed argv", async () => {
  let checkedDirectPorts = false;
  const plan = await selectReleaseRuntimeTransport({
    probe: { status: 0, stderr: "" },
    forwardedArguments: ["--evidence", "/test/evidence.json"],
    portsAbsent: async () => { checkedDirectPorts = true; return true; },
    reserve: async () => 41234,
    nodeExecutable,
    scriptPath,
  });
  assert.equal(checkedDirectPorts, false);
  assert.deepEqual(plan, {
    transport: "pasta",
    executable: "/usr/bin/pasta",
    arguments: [
      "--quiet", "--foreground", "--ipv4-only",
      "--tcp-ports", "127.0.0.1/41234", "--udp-ports", "none",
      "--tcp-ns", "none", "--udp-ns", "none",
      nodeExecutable, scriptPath, "--director-inner-release-runtime",
      "--evidence", "/test/evidence.json",
    ],
    shell: false,
  });
});

test("the exact GitHub user-namespace refusal selects direct transport after port proof", async () => {
  let portChecks = 0;
  const plan = await selectReleaseRuntimeTransport({
    probe: { status: 1, stderr: denied },
    portsAbsent: async () => { portChecks += 1; return true; },
    reserve: async () => { throw new Error("pasta port reservation must not run"); },
    nodeExecutable,
    scriptPath,
  });
  assert.equal(portChecks, 1);
  assert.deepEqual(plan, {
    transport: "direct",
    executable: nodeExecutable,
    arguments: [scriptPath, "--director-inner-release-runtime"],
    shell: false,
  });
});

test("missing pasta selects the same bounded direct path and occupied ports refuse it", async () => {
  assert.equal(classifyPastaCapability({ status: null, error: { code: "ENOENT" } }), "direct");
  await assert.rejects(
    selectReleaseRuntimeTransport({
      probe: { status: null, error: { code: "ENOENT" } },
      portsAbsent: async () => false,
      nodeExecutable,
      scriptPath,
    }),
    { message: "DIRECTOR_RELEASE_RUNTIME_PORT_OCCUPIED" },
  );
});

test("unknown pasta failure and incomplete permission text never fall back", () => {
  for (const probe of [
    { status: 1, stderr: "clone: Operation not permitted" },
    { status: 1, stderr: "network setup failed" },
    { status: null, error: { code: "EACCES" } },
  ]) {
    assert.throws(() => classifyPastaCapability(probe), {
      message: "DIRECTOR_RELEASE_RUNTIME_PASTA_PROBE",
    });
  }
});

test("transport diagnostics retain only an exact bounded child code", () => {
  assert.equal(
    boundedTransportChildCode("private /tmp/path password=secret\nDIRECTOR_RELEASE_RUNTIME_TIMEOUT\n", "DIRECTOR_RELEASE_RUNTIME_DIRECT_EXIT"),
    "DIRECTOR_RELEASE_RUNTIME_TIMEOUT",
  );
  assert.equal(
    boundedTransportChildCode("private /tmp/path password=secret\n", "DIRECTOR_RELEASE_RUNTIME_DIRECT_EXIT"),
    "DIRECTOR_RELEASE_RUNTIME_DIRECT_EXIT",
  );
});

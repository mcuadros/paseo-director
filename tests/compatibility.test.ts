// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  HostCompatibilityError,
  assertHostCompatibility,
} from "../connector/compatibility.server.ts";

const supported = {
  platform: "linux",
  architecture: "x64",
  nodeVersion: "22.0.0",
  paseoVersion: () => "0.7.2",
};

test("compatibility is exact Paseo 0.7.2 on Linux amd64 with Node 22 or newer", () => {
  assert.deepEqual(assertHostCompatibility(supported), {
    paseoVersion: "0.7.2",
    nodeVersion: "22.0.0",
    platform: "linux",
    architecture: "x64",
    target: "linux-amd64",
  });
  assert.equal(assertHostCompatibility({ ...supported, nodeVersion: "26.7.0" }).nodeVersion, "26.7.0");
});

test("unsupported platform, target, Node, and Paseo fail with bounded diagnostics", () => {
  for (const [probe, code] of [
    [{ ...supported, platform: "darwin" }, "HOST_PLATFORM_UNSUPPORTED"],
    [{ ...supported, architecture: "arm64" }, "HOST_TARGET_UNSUPPORTED"],
    [{ ...supported, nodeVersion: "21.9.0" }, "NODE_VERSION_UNSUPPORTED"],
    [{ ...supported, paseoVersion: () => "0.7.3" }, "PASEO_VERSION_UNSUPPORTED"],
    [{ ...supported, paseoVersion: () => "secret/path?token=never" }, "PASEO_VERSION_UNSUPPORTED"],
  ] as const) {
    assert.throws(() => assertHostCompatibility(probe), (error: unknown) =>
      error instanceof HostCompatibilityError &&
      error.code === code &&
      !error.message.includes("secret") &&
      !error.message.includes("path"));
  }
});

// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { verifyInstall } from "./verify-install.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const supported = {
  platform: "linux",
  architecture: "x64",
  nodeVersion: "22.0.0",
  repositoryRoot,
};

test("install verification binds exact compatibility to the installed locked closure", () => {
  assert.equal(verifyInstall({ ...supported, paseoVersion: "0.7.2" }).code, "DIRECTOR_INSTALL_READY");
});

test("install verification rejects incompatible hosts with bounded path-free diagnostics", () => {
  assert.deepEqual(verifyInstall({ ...supported, paseoVersion: "0.8.0" }), {
    code: "DIRECTOR_INSTALL_PASEO_UNSUPPORTED",
    message: "Director supports exact Paseo 0.7.2; observed 0.8.0",
  });
  const unsafe = verifyInstall({ ...supported, paseoVersion: "secret/path?credential=value" });
  assert.equal(unsafe.code, "DIRECTOR_INSTALL_PASEO_UNSUPPORTED");
  assert.doesNotMatch(unsafe.message, /secret|path|credential|value/u);
});

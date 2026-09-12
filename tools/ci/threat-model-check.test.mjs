// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import { validateThreatModel, verifyRepositoryBoundary } from "./threat-model-check.mjs";

const manifest = JSON.parse(readFileSync(new URL("../../docs/threat-model-hardening.json", import.meta.url), "utf8"));

test("the maintained threat matrix resolves every normative source and evidence anchor", () => {
  assert.deepEqual(verifyRepositoryBoundary(), []);
});

test("the threat matrix fails closed on missing coverage, stale anchors, and reopened severity", () => {
  for (const mutate of [
    (value) => { value.controls[0].threats = value.controls[0].threats.slice(1); },
    (value) => { value.controls[0].tests[0] += "-missing"; },
    (value) => { value.controls[0].status = "open"; },
    (value) => { value.controls[0].categories = []; },
  ]) {
    const changed = structuredClone(manifest);
    mutate(changed);
    assert.notEqual(validateThreatModel(changed).length, 0);
  }
});

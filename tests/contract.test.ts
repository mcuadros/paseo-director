// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  assertHostDescriptor,
  EXPECTED_HOST_DESCRIPTOR,
  HOST_CAPABILITIES,
  HostHandshakeError,
} from "../generated/host-contract.shared.ts";
import { connectorStartupStatus } from "../rpc/startup.shared.ts";

test("the generated host descriptor accepts only the exact engine contract", () => {
  assert.equal(assertHostDescriptor(EXPECTED_HOST_DESCRIPTOR), EXPECTED_HOST_DESCRIPTOR);

  for (const drift of [
    { ...EXPECTED_HOST_DESCRIPTOR, contractVersion: "director-host/v2" },
    { ...EXPECTED_HOST_DESCRIPTOR, contractHash: "0".repeat(64) },
    {
      ...EXPECTED_HOST_DESCRIPTOR,
      capabilities: HOST_CAPABILITIES.slice(0, -1),
    },
    { ...EXPECTED_HOST_DESCRIPTOR, extra: true },
  ]) {
    assert.throws(
      () => assertHostDescriptor(drift),
      (error: unknown) => error instanceof HostHandshakeError,
    );
  }
});

test("the Paseo startup RPC is strict and exposes no credential field", () => {
  const result = connectorStartupStatus.output.safeParse({
    state: "scaffold-ready",
    engineMode: "development",
    productBehavior: false,
    descriptor: EXPECTED_HOST_DESCRIPTOR,
  });
  assert.equal(result.success, true);

  const credential = connectorStartupStatus.output.safeParse({
    state: "scaffold-ready",
    engineMode: "development",
    productBehavior: false,
    descriptor: EXPECTED_HOST_DESCRIPTOR,
    credential: "must-not-cross",
  });
  assert.equal(credential.success, false);
});

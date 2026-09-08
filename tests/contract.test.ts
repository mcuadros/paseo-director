// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  assertBoardSnapshot,
  BOARD_MAXIMUM_TASKS,
  assertHostDescriptor,
  EXPECTED_HOST_DESCRIPTOR,
  HOST_CAPABILITIES,
  HostHandshakeError,
} from "../generated/host-contract.shared.ts";
import { boardSnapshotRpc } from "../rpc/board.shared.ts";
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
    state: "board-ready",
    engineMode: "development",
    productBehavior: true,
    descriptor: EXPECTED_HOST_DESCRIPTOR,
  });
  assert.equal(result.success, true);

  const credential = connectorStartupStatus.output.safeParse({
    state: "board-ready",
    engineMode: "development",
    productBehavior: true,
    descriptor: EXPECTED_HOST_DESCRIPTOR,
    credential: "must-not-cross",
  });
  assert.equal(credential.success, false);
});

test("the Board RPC and generated parser accept only the engine-owned shape", () => {
  const snapshot = {
    schemaVersion: 1,
    cursor: "12",
    tasks: [{
      id: "task-1",
      projectId: "project-1",
      projectName: "Director",
      title: "Render the Board",
      state: "building",
      runNumber: "1",
      candidateSha: null,
    }],
  } as const;
  assert.equal(boardSnapshotRpc.input.safeParse({}).success, true);
  assert.equal(boardSnapshotRpc.input.safeParse({ projectId: "client-owned" }).success, false);
  assert.equal(boardSnapshotRpc.output.safeParse(snapshot).success, true);
  assert.deepEqual(assertBoardSnapshot(snapshot), snapshot);

  for (const drift of [
    { ...snapshot, cursor: 12 },
    { ...snapshot, cursor: "18446744073709551616" },
    { ...snapshot, extra: true },
    { ...snapshot, tasks: [{ ...snapshot.tasks[0], state: "client-invented" }] },
    {
      ...snapshot,
      tasks: Array.from({ length: BOARD_MAXIMUM_TASKS + 1 }, () => snapshot.tasks[0]),
    },
  ]) {
    assert.equal(boardSnapshotRpc.output.safeParse(drift).success, false);
    assert.throws(() => assertBoardSnapshot(drift));
  }
});

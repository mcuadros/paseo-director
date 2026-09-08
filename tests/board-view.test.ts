// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import type { BoardSnapshot } from "../generated/host-contract.shared.ts";
import {
  boardScene,
  boardStatesForLayout,
  tasksInState,
} from "../ui/board-view.client.ts";

const emptySnapshot: BoardSnapshot = {
  schemaVersion: 1,
  cursor: "0",
  tasks: [],
};

test("board scenes cover loading, error, empty, cached, and updated data", () => {
  assert.deepEqual(
    boardScene({ data: undefined, isPending: true, isError: false }),
    { kind: "loading" },
  );
  assert.deepEqual(
    boardScene({ data: undefined, isPending: false, isError: true }),
    { kind: "error" },
  );
  assert.deepEqual(
    boardScene({ data: emptySnapshot, isPending: false, isError: false }),
    { kind: "empty", snapshot: emptySnapshot },
  );

  const first: BoardSnapshot = {
    schemaVersion: 1,
    cursor: "2",
    tasks: [{
      id: "task-1",
      projectId: "project-1",
      projectName: "Director",
      title: "Queued task",
      state: "queued",
      runNumber: null,
      candidateSha: null,
    }],
  };
  const updated: BoardSnapshot = {
    ...first,
    cursor: "4",
    tasks: [{
      ...first.tasks[0],
      title: "Building task",
      state: "building",
      runNumber: "1",
    }],
  };
  assert.deepEqual(
    boardScene({ data: first, isPending: false, isError: false }),
    { kind: "data", snapshot: first },
  );
  assert.deepEqual(
    boardScene({ data: updated, isPending: false, isError: true }),
    { kind: "data", snapshot: updated },
  );
});

test("wide Board shows every applicable lane and compact Board shows one lane", () => {
  const tasks: BoardSnapshot["tasks"] = [{
    id: "task-attention",
    projectId: "project-1",
    projectName: "Director",
    title: "Needs a decision",
    state: "needs_you",
    runNumber: null,
    candidateSha: null,
  }];
  assert.deepEqual(boardStatesForLayout(false, "queued", []), [
    "queued",
    "building",
    "validating",
    "in_review",
    "ready",
  ]);
  assert.deepEqual(boardStatesForLayout(false, "queued", tasks), [
    "needs_you",
    "queued",
    "building",
    "validating",
    "in_review",
    "ready",
  ]);
  assert.deepEqual(boardStatesForLayout(true, "ready", tasks), ["ready"]);
  assert.deepEqual(boardStatesForLayout(true, "needs_you", []), ["queued"]);
});

test("the UI groups only by the engine-provided state", () => {
  const tasks: BoardSnapshot["tasks"] = [
    {
      id: "task-a",
      projectId: "project-1",
      projectName: "Director",
      title: "Has no run but is engine-ready",
      state: "ready",
      runNumber: null,
      candidateSha: null,
    },
    {
      id: "task-b",
      projectId: "project-1",
      projectName: "Director",
      title: "Has a candidate but is engine-building",
      state: "building",
      runNumber: "2",
      candidateSha: "abcdef0123456789abcdef0123456789abcdef01",
    },
  ];
  assert.deepEqual(tasksInState(tasks, "ready").map(({ id }) => id), ["task-a"]);
  assert.deepEqual(tasksInState(tasks, "building").map(({ id }) => id), ["task-b"]);
});

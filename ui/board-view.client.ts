// SPDX-License-Identifier: Apache-2.0
// Pure client presentation helpers consume, but never derive, engine state.

import {
  boardStates,
  type BoardSnapshot,
  type BoardState,
  type BoardTask,
} from "../rpc/board.shared.ts";

export type BoardRequestState = {
  data: BoardSnapshot | undefined;
  isPending: boolean;
  isError: boolean;
};

export type BoardScene =
  | { kind: "loading" }
  | { kind: "error" }
  | { kind: "empty"; snapshot: BoardSnapshot }
  | { kind: "data"; snapshot: BoardSnapshot };

export const boardStateLabels: Readonly<Record<BoardState, string>> = {
  needs_you: "Needs you",
  queued: "Queued",
  building: "Building",
  validating: "Validating",
  in_review: "In review",
  ready: "Ready",
};

export function boardScene(request: BoardRequestState): BoardScene {
  if (request.data) {
    return request.data.tasks.length === 0
      ? { kind: "empty", snapshot: request.data }
      : { kind: "data", snapshot: request.data };
  }
  return request.isPending ? { kind: "loading" } : { kind: "error" };
}

export function boardStatesForLayout(
  compact: boolean,
  selected: BoardState,
  tasks: readonly BoardTask[],
): readonly BoardState[] {
  const hasNeedsYou = tasks.some(({ state }) => state === "needs_you");
  const available = boardStates.filter(
    (state) => state !== "needs_you" || hasNeedsYou,
  );
  if (!compact) return available;
  return [available.includes(selected) ? selected : "queued"];
}

export function tasksInState(
  tasks: readonly BoardTask[],
  state: BoardState,
): readonly BoardTask[] {
  return tasks.filter((task) => task.state === state);
}

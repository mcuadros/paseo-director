// SPDX-License-Identifier: Apache-2.0

import { appendFileSync, readFileSync } from "node:fs";

import type { BoardSnapshot } from "./rpc/board.shared";

type MeasurementMode = {
  kind: "delayed-empty" | "empty" | "data" | "error";
  delayMs?: number;
};

function paths() {
  const control = process.env.DIRECTOR_BOARD_MEASURE_CONTROL;
  const log = process.env.DIRECTOR_BOARD_MEASURE_LOG;
  if (!control || !log) throw new Error("measurement paths are required");
  return { control, log };
}

export async function loadMeasuredBoard(): Promise<BoardSnapshot> {
  const { control, log } = paths();
  const mode = JSON.parse(readFileSync(control, "utf8")) as MeasurementMode;
  appendFileSync(log, `${JSON.stringify({ at: Date.now(), kind: mode.kind })}\n`);
  if (mode.delayMs) await new Promise((resolve) => setTimeout(resolve, mode.delayMs));
  if (mode.kind === "error") throw new Error("measured Board error");
  const tasks: BoardSnapshot["tasks"] = mode.kind === "data"
    ? [{
        id: "measured-task", projectId: "measured-project",
        projectName: "Measured project", title: "Measured Board task",
        state: "building", runNumber: "1", candidateSha: null,
      }]
    : [];
  return { schemaVersion: 1, cursor: mode.kind === "data" ? "2" : "1", tasks };
}

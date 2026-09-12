// SPDX-License-Identifier: Apache-2.0

import { workerData, parentPort } from "node:worker_threads";

import {
  persistPrivateJson,
  withProcessIdentityLock,
} from "./internal.mjs";

const barrier = new Int32Array(workerData.barrier);
parentPort.postMessage({ status: "ready", effect: workerData.effect });
Atomics.wait(barrier, 0, 0);

try {
  const result = await withProcessIdentityLock(
    workerData.configuration,
    async () => {
      parentPort.postMessage({
        status: "entered",
        effect: workerData.effect,
      });
      Atomics.wait(barrier, 1, 0);
      persistPrivateJson(workerData.statePath, {
        schemaVersion: 1,
        effects: { [workerData.effect]: "complete" },
      });
      return workerData.effect;
    },
  );
  parentPort.postMessage({ status: "fulfilled", effect: result });
} catch (error) {
  parentPort.postMessage({
    status: "rejected",
    effect: workerData.effect,
    code: error?.code ?? "UNEXPECTED",
  });
}

import { spawn } from "node:child_process";
import { appendFileSync, existsSync, readFileSync } from "node:fs";
import { createConnection, type Socket } from "node:net";
import { join } from "node:path";

import type { PaseoApi } from "@getpaseo/client";

const CONTRACT_HASH = "9ee14489ce72ddace0df4550b11ad03ce3ac946bd9392e67e5cd047c210715e4";
const ENGINE_SHA = "fb29fe79c1632f8921dff2d681e9e8879e0b782e8bb13d3394ec15aeea032ca1";

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

export interface LiveConnector {
  attachPaseo(paseo: PaseoApi): void;
  cleanup(): void;
  describe(): { hasPaseoApi: boolean; contractHash: string };
}

export function startLiveConnector(
  pluginContext: Record<string, unknown>,
): LiveConnector {
  const runtimeRoot = process.env.DIRECTOR_M112_RUNTIME_ROOT;
  if (!runtimeRoot) throw new Error("DIRECTOR_M112_RUNTIME_ROOT is required");
  const eventsPath = join(runtimeRoot, "events.jsonl");
  const pidPath = join(runtimeRoot, "engine.pid");
  const socketPath = join(runtimeRoot, "engine.sock");
  const enginePath = join(runtimeRoot, "artifacts", ENGINE_SHA, "director-engine.mjs");
  appendFileSync(
    eventsPath,
    `${JSON.stringify({
      type: "plugin-start",
      pid: process.pid,
      cwd: process.cwd(),
      hasTopLevelPaseoApi: typeof pluginContext.paseo === "object",
      engineArtifactPresent: existsSync(enginePath),
    })}\n`,
  );

  let socket: Socket | undefined;
  let paseo: PaseoApi | undefined;
  let stopped = false;
  async function connect(): Promise<void> {
    for (let attempt = 0; attempt < 100; attempt += 1) {
      try {
        socket = await new Promise<Socket>((resolve, reject) => {
          const candidate = createConnection(socketPath, () => resolve(candidate));
          candidate.once("error", reject);
        });
        if (stopped) {
          socket.destroy();
          return;
        }
        socket.write(
          `${JSON.stringify({
            type: "connector-describe",
            hasPaseoApi: paseo !== undefined,
            contractHash: CONTRACT_HASH,
          })}\n`,
        );
        return;
      } catch (error) {
        const code = (error as NodeJS.ErrnoException).code;
        if (attempt === 0 && (code === "ENOENT" || code === "ECONNREFUSED")) {
          let priorPid = 0;
          if (existsSync(pidPath)) priorPid = Number.parseInt(readFileSync(pidPath, "utf8"), 10);
          if (!Number.isSafeInteger(priorPid) || priorPid <= 1 || !isAlive(priorPid)) {
            const child = spawn(process.execPath, [enginePath], {
              detached: true,
              env: process.env,
              stdio: "ignore",
            });
            child.unref();
          }
        }
        if (attempt === 99) throw error;
        await delay(50);
      }
    }
  }
  void connect().catch((error: unknown) => {
    appendFileSync(
      eventsPath,
      `${JSON.stringify({
        type: "connector-error",
        pid: process.pid,
        code: (error as NodeJS.ErrnoException).code ?? "unknown",
      })}\n`,
    );
  });

  return {
    attachPaseo(value) {
      paseo = value;
      appendFileSync(
        eventsPath,
        `${JSON.stringify({ type: "handler-authority-acquired", pid: process.pid })}\n`,
      );
    },
    cleanup() {
      stopped = true;
      appendFileSync(
        eventsPath,
        `${JSON.stringify({ type: "plugin-cleanup", pid: process.pid })}\n`,
      );
      socket?.destroy();
    },
    describe() {
      return { hasPaseoApi: paseo !== undefined, contractHash: CONTRACT_HASH };
    },
  };
}

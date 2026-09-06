import { randomUUID } from "node:crypto";
import { appendFileSync, existsSync, rmSync } from "node:fs";
import path from "node:path";

interface ProbeLifecycle {
  instanceId: string;
  cleanup: () => void;
}

function appendEvent(eventsPath: string, event: Record<string, unknown>): void {
  appendFileSync(eventsPath, `${JSON.stringify(event)}\n`, { encoding: "utf8" });
}

export function startProbe(pluginId: string): ProbeLifecycle {
  const root = process.env.DIRECTOR_LIFECYCLE_ROOT;
  if (!root) throw new Error("DIRECTOR_LIFECYCLE_ROOT is required");

  const instanceId = randomUUID();
  const eventsPath = path.join(root, `${pluginId}.events.jsonl`);
  const crashPath = path.join(root, `${pluginId}.crash`);
  appendEvent(eventsPath, {
    type: "started",
    instanceId,
    pid: process.pid,
    timestamp: new Date().toISOString(),
  });

  const timer = setInterval(() => {
    if (!existsSync(crashPath)) return;
    rmSync(crashPath, { force: true });
    appendEvent(eventsPath, {
      type: "crash-requested",
      instanceId,
      pid: process.pid,
      timestamp: new Date().toISOString(),
    });
    process.exit(23);
  }, 50);
  timer.unref();

  return {
    instanceId,
    cleanup: () => {
      clearInterval(timer);
      appendEvent(eventsPath, {
        type: "cleanup",
        instanceId,
        pid: process.pid,
        timestamp: new Date().toISOString(),
      });
    },
  };
}

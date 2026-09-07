import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  appendFileSync,
  existsSync,
  readFileSync,
} from "node:fs";
import { createConnection, type Socket } from "node:net";
import { join } from "node:path";

import {
  createPaseoClient,
  type PaseoClient,
} from "@getpaseo/client";

const CONTRACT_VERSION = 1;
const AUTHORITY_OWNER = "director-for-paseo";
const CREDENTIAL_KIND = "daemon-password";
const CREDENTIAL_SCOPE = "full-daemon-operator";
const CAPABILITIES = [
  "executionWorkspace.createManaged",
  "executionWorkspace.observe",
  "executionWorkspace.archive",
  "taskAgent.createWithInitialPrompt",
  "reviewerAgent.createWithInitialPrompt",
  "helperAgent.observe",
  "agent.observe",
  "agent.archive",
] as const;
const CONTRACT_DOCUMENT = JSON.stringify({
  descriptorType: "connector-describe",
  contractVersion: CONTRACT_VERSION,
  capabilities: CAPABILITIES,
  authorityOwner: AUTHORITY_OWNER,
  credentialKind: CREDENTIAL_KIND,
  credentialScope: CREDENTIAL_SCOPE,
  observationFields: ["workspaceCount"],
});
const CONTRACT_HASH = createHash("sha256")
  .update(CONTRACT_DOCUMENT)
  .digest("hex");

function event(path: string, type: string, detail: Record<string, unknown> = {}) {
  appendFileSync(
    path,
    `${JSON.stringify({ type, pid: process.pid, ...detail })}\n`,
    { encoding: "utf8" },
  );
}

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

function requiredEnvironment(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

export function startConnectorAuthorityProbe(): {
  cleanup(): Promise<void>;
} {
  const runtimeRoot = requiredEnvironment("DIRECTOR_M112_AUTH_RUNTIME_ROOT");
  const paseoUrl = requiredEnvironment("DIRECTOR_M112_AUTH_PASEO_URL");
  const credentialPath = requiredEnvironment(
    "DIRECTOR_M112_AUTH_PASEO_PASSWORD_FILE",
  );
  if (!existsSync(credentialPath)) {
    throw new Error("connector credential file is absent");
  }
  const password = readFileSync(credentialPath, "utf8").trim();
  if (!password) throw new Error("connector credential is empty");

  const eventsPath = join(runtimeRoot, "events.jsonl");
  const enginePath = requiredEnvironment("DIRECTOR_M112_AUTH_ENGINE_PATH");
  const pidPath = join(runtimeRoot, "engine.pid");
  const socketPath = join(runtimeRoot, "engine.sock");
  let paseo: PaseoClient | undefined;
  let socket: Socket | undefined;
  let stopped = false;

  async function connectEngine(): Promise<void> {
    for (let attempt = 0; attempt < 100; attempt += 1) {
      try {
        socket = await new Promise<Socket>((resolve, reject) => {
          const candidate = createConnection(socketPath, () => resolve(candidate));
          candidate.once("error", reject);
        });
        return;
      } catch (error) {
        const code = (error as NodeJS.ErrnoException).code;
        if (attempt === 0 && (code === "ENOENT" || code === "ECONNREFUSED")) {
          let priorPid = 0;
          if (existsSync(pidPath)) {
            priorPid = Number.parseInt(readFileSync(pidPath, "utf8"), 10);
          }
          if (!Number.isSafeInteger(priorPid) || priorPid <= 1 || !isAlive(priorPid)) {
            const child = spawn(process.execPath, [enginePath], {
              detached: true,
              env: {
                DIRECTOR_M112_AUTH_RUNTIME_ROOT: runtimeRoot,
                PATH: process.env.PATH ?? "",
              },
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

  async function initialize(): Promise<void> {
    paseo = createPaseoClient({
      url: paseoUrl,
      password,
      clientId: `director-connector-authority-${process.pid}`,
      reconnect: { enabled: false },
    });
    await paseo.connect();
    const observed = await paseo.workspaces.list({ page: { limit: 1 } });
    if (stopped) return;
    event(eventsPath, "connector-sdk-ready", {
      authorityOwner: AUTHORITY_OWNER,
      credentialKind: CREDENTIAL_KIND,
      credentialScope: CREDENTIAL_SCOPE,
      observedWorkspaceCount: observed.entries.length,
    });
    await connectEngine();
    if (stopped || !socket) return;
    socket.write(
      `${JSON.stringify({
        type: "connector-describe",
        contractVersion: CONTRACT_VERSION,
        contractHash: CONTRACT_HASH,
        capabilities: CAPABILITIES,
        authorityOwner: AUTHORITY_OWNER,
        credentialKind: CREDENTIAL_KIND,
        credentialScope: CREDENTIAL_SCOPE,
        observation: { workspaceCount: observed.entries.length },
      })}\n`,
    );
  }

  void initialize().catch((error: unknown) => {
    event(eventsPath, "connector-start-failed", {
      message: error instanceof Error ? error.message : "unknown failure",
    });
  });

  return {
    async cleanup() {
      stopped = true;
      socket?.destroy();
      if (paseo) await paseo.close();
      event(eventsPath, "connector-cleanup");
    },
  };
}

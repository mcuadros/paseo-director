#!/usr/bin/env node

import { existsSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const contractPath = fileURLToPath(new URL("./taskstore-contract.mjs", import.meta.url));
const readyPrefix = "DIRECTOR_M04_READY ";

function assertObservation(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

function pidIsAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error?.code === "ESRCH") {
      return false;
    }
    throw error;
  }
}

function isOwnedServerProcess(identity) {
  if (!Number.isSafeInteger(identity?.server_pid) || identity.server_pid <= 1) {
    return false;
  }
  try {
    const commandLine = readFileSync(`/proc/${identity.server_pid}/cmdline`, "utf8");
    return (
      commandLine.includes("dolt") &&
      commandLine.includes("sql-server") &&
      commandLine.includes(identity.temp_dir_basename)
    );
  } catch (error) {
    if (error?.code === "ENOENT") {
      return false;
    }
    throw error;
  }
}

function waitForExit(child, timeoutMs) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve({ code: child.exitCode ?? 1, signal: child.signalCode });
  }
  return Promise.race([
    new Promise((resolve) => {
      child.once("close", (code, signal) => resolve({ code, signal }));
    }),
    new Promise((_, reject) => {
      const timer = setTimeout(
        () => reject(new Error("contract did not exit after SIGINT")),
        timeoutMs,
      );
      timer.unref();
    }),
  ]);
}

async function waitForPidExit(pid, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (pidIsAlive(pid) && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  return !pidIsAlive(pid);
}

let child;
let identity;
let tempPath;

async function cleanupOnFailure() {
  if (child && child.exitCode === null && child.signalCode === null) {
    child.kill("SIGKILL");
    await waitForExit(child, 5_000).catch(() => undefined);
  }
  if (identity?.server_pid && pidIsAlive(identity.server_pid)) {
    if (!isOwnedServerProcess(identity)) {
      return;
    }
    process.kill(identity.server_pid, "SIGKILL");
    if (!(await waitForPidExit(identity.server_pid, 5_000))) {
      return;
    }
  }
  if (tempPath && existsSync(tempPath)) {
    rmSync(tempPath, { recursive: true, force: true });
  }
}

try {
  child = spawn(process.execPath, [contractPath], {
    env: { ...process.env, DIRECTOR_M04_INTERRUPT_READY: "1" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  let stdout = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    stdout += chunk;
  });

  identity = await Promise.race([
    new Promise((resolve, reject) => {
      child.stderr.on("data", (chunk) => {
        stderr += chunk;
        const marker = stderr
          .split("\n")
          .find((line) => line.startsWith(readyPrefix));
        if (marker) {
          try {
            resolve(JSON.parse(marker.slice(readyPrefix.length)));
          } catch (error) {
            reject(error);
          }
        }
      });
      child.once("close", (code) => {
        reject(new Error(`contract exited ${code} before readiness: ${stderr || stdout}`));
      });
      child.once("error", reject);
    }),
    new Promise((_, reject) => {
      const timer = setTimeout(
        () => reject(new Error("contract did not reach authenticated readiness")),
        30_000,
      );
      timer.unref();
    }),
  ]);

  assertObservation(/^[0-9a-f]{16}$/.test(identity.run_id), "invalid run identity");
  assertObservation(
    Number.isSafeInteger(identity.server_pid) && identity.server_pid > 1,
    "invalid owned server PID",
  );
  assertObservation(
    Number.isInteger(identity.server_port) && identity.server_port >= 1024,
    "invalid owned server port",
  );
  assertObservation(
    new RegExp(`^director-m0\\.4-${identity.run_id}-[A-Za-z0-9]+$`).test(
      identity.temp_dir_basename,
    ),
    "invalid owned temporary directory identity",
  );
  tempPath = join(tmpdir(), identity.temp_dir_basename);
  assertObservation(existsSync(tempPath), "owned temporary directory was absent before interruption");
  assertObservation(
    isOwnedServerProcess(identity),
    "recorded PID was not the owned SQL server before interruption",
  );

  child.kill("SIGINT");
  const exit = await waitForExit(child, 20_000);
  assertObservation(exit.code === 130, `contract exited ${exit.code} instead of 130`);
  assertObservation(!existsSync(tempPath), "owned temporary directory survived SIGINT cleanup");
  assertObservation(!pidIsAlive(identity.server_pid), "owned SQL server survived SIGINT cleanup");

  process.stdout.write(
    `${JSON.stringify(
      {
        schema_version: 1,
        command: "node docs/evidence/m0.4/verify-interruption.mjs",
        injected_signal: "SIGINT",
        contract_exit_code: exit.code,
        run_id: identity.run_id,
        spawned_server_pid: identity.server_pid,
        authenticated_ready_port: identity.server_port,
        server_terminated_before_storage_removal: true,
        owned_temp_directory_removed: true,
      },
      null,
      2,
    )}\n`,
  );
} catch (error) {
  await cleanupOnFailure();
  process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`);
  process.exitCode = 1;
}

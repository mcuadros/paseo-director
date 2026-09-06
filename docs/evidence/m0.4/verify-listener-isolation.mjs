#!/usr/bin/env node

import { existsSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const contractPath = fileURLToPath(new URL("./taskstore-contract.mjs", import.meta.url));
const readyPrefix = "DIRECTOR_M04_READY ";
const failurePrefix = "DIRECTOR_M04_FAILURE ";
const reverifiedPrefix = "DIRECTOR_M04_REVERIFIED ";

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

function ownedTempPath(identity) {
  assertObservation(/^[0-9a-f]{16}$/.test(identity.run_id), "invalid run identity");
  assertObservation(
    new RegExp(`^director-m0\\.4-${identity.run_id}-[A-Za-z0-9]+$`).test(
      identity.temp_dir_basename,
    ),
    "invalid owned temporary directory identity",
  );
  return join(tmpdir(), identity.temp_dir_basename);
}

function startContract(extraEnv) {
  const child = spawn(process.execPath, [contractPath], {
    env: { ...process.env, ...extraEnv },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  const state = { child, stdout: "", stderr: "" };
  child.stdout.on("data", (chunk) => {
    state.stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    state.stderr += chunk;
  });
  return state;
}

function waitForMarker(state, prefix, timeoutMs) {
  return Promise.race([
    new Promise((resolve, reject) => {
      const inspect = () => {
        const marker = state.stderr.split("\n").find((line) => line.startsWith(prefix));
        if (marker) {
          try {
            resolve(JSON.parse(marker.slice(prefix.length)));
          } catch (error) {
            reject(error);
          }
        }
      };
      state.child.stderr.on("data", inspect);
      inspect();
      state.child.once("error", reject);
      state.child.once("close", (code) => {
        inspect();
        reject(new Error(`contract exited ${code} before marker ${prefix}: ${state.stderr}`));
      });
    }),
    new Promise((_, reject) => {
      const timer = setTimeout(() => reject(new Error(`missing marker ${prefix}`)), timeoutMs);
      timer.unref();
    }),
  ]);
}

function waitForExit(state, timeoutMs) {
  if (state.child.exitCode !== null || state.child.signalCode !== null) {
    return Promise.resolve({
      code: state.child.exitCode ?? 1,
      signal: state.child.signalCode,
      stdout: state.stdout,
      stderr: state.stderr,
    });
  }
  return Promise.race([
    new Promise((resolve) => {
      state.child.once("close", (code, signal) =>
        resolve({ code: code ?? 1, signal, stdout: state.stdout, stderr: state.stderr }),
      );
    }),
    new Promise((_, reject) => {
      const timer = setTimeout(() => reject(new Error("contract exit timed out")), timeoutMs);
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

let owner;
let contender;
let ownerIdentity;
let contenderIdentity;

async function cleanupOnFailure() {
  for (const state of [contender, owner]) {
    if (state?.child && state.child.exitCode === null && state.child.signalCode === null) {
      state.child.kill("SIGKILL");
      await waitForExit(state, 5_000).catch(() => undefined);
    }
  }
  for (const identity of [contenderIdentity, ownerIdentity]) {
    if (identity?.server_pid && pidIsAlive(identity.server_pid)) {
      if (!isOwnedServerProcess(identity)) {
        continue;
      }
      process.kill(identity.server_pid, "SIGKILL");
      if (!(await waitForPidExit(identity.server_pid, 5_000))) {
        continue;
      }
    }
    if (identity?.temp_dir_basename) {
      const path = ownedTempPath(identity);
      if (existsSync(path)) {
        rmSync(path, { recursive: true, force: true });
      }
    }
  }
}

try {
  owner = startContract({ DIRECTOR_M04_INTERRUPT_READY: "1" });
  ownerIdentity = await waitForMarker(owner, readyPrefix, 30_000);
  const ownerTemp = ownedTempPath(ownerIdentity);
  assertObservation(isOwnedServerProcess(ownerIdentity), "owner server PID was not owned/live");
  assertObservation(existsSync(ownerTemp), "owner storage was not live");

  contender = startContract({
    DIRECTOR_M04_FORCED_PORT: String(ownerIdentity.server_port),
  });
  contenderIdentity = await waitForMarker(contender, failurePrefix, 30_000);
  const contenderExit = await waitForExit(contender, 10_000);
  const contenderTemp = ownedTempPath(contenderIdentity);
  assertObservation(contenderExit.code !== 0, "contender unexpectedly succeeded");
  assertObservation(
    /owned Dolt server exited before readiness/.test(contenderExit.stderr) &&
      /address already in use|port.+in use/i.test(contenderExit.stderr),
    `contender did not fail closed on listener ownership: ${contenderExit.stderr}`,
  );
  assertObservation(!existsSync(contenderTemp), "contender storage survived failed ownership proof");
  assertObservation(
    !pidIsAlive(contenderIdentity.server_pid),
    "contender server child survived failed ownership proof",
  );

  owner.child.kill("SIGUSR1");
  const reverified = await waitForMarker(owner, reverifiedPrefix, 10_000);
  assertObservation(
    reverified.run_id === ownerIdentity.run_id &&
      reverified.server_pid === ownerIdentity.server_pid &&
      reverified.server_port === ownerIdentity.server_port,
    "contender attached to or changed the owner listener identity",
  );
  assertObservation(
    isOwnedServerProcess(ownerIdentity),
    "owner server identity changed during collision probe",
  );

  owner.child.kill("SIGINT");
  const ownerExit = await waitForExit(owner, 20_000);
  assertObservation(ownerExit.code === 130, "owner contract did not exit cleanly on SIGINT");
  assertObservation(!existsSync(ownerTemp), "owner storage survived final cleanup");
  assertObservation(!pidIsAlive(ownerIdentity.server_pid), "owner server survived final cleanup");

  process.stdout.write(
    `${JSON.stringify(
      {
        schema_version: 1,
        command: "node docs/evidence/m0.4/verify-listener-isolation.mjs",
        owner: {
          run_id: ownerIdentity.run_id,
          server_pid: ownerIdentity.server_pid,
          server_port: ownerIdentity.server_port,
          identity_reverified_after_collision: true,
          cleanup_exit_code: ownerExit.code,
        },
        contender: {
          run_id: contenderIdentity.run_id,
          server_pid: contenderIdentity.server_pid,
          forced_port: ownerIdentity.server_port,
          failed_closed: true,
          exit_code: contenderExit.code,
          storage_removed: true,
        },
        cross_run_attachment: false,
        remaining_owned_servers: 0,
        remaining_owned_directories: 0,
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

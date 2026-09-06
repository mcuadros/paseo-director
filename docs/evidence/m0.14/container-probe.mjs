#!/usr/bin/env node

import { spawn } from "node:child_process";
import { access, readFile, statfs, writeFile } from "node:fs/promises";
import net from "node:net";
import path from "node:path";

const request = JSON.parse(await readStdin());
if (request.nonce !== process.env.DIRECTOR_M014_NONCE) {
  throw new Error("The stdio request did not match the fixed container scope");
}

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks).toString("utf8");
}

async function outcome(operation) {
  try {
    await operation();
    return "allowed";
  } catch (cause) {
    return cause && typeof cause === "object" && typeof cause.code === "string"
      ? cause.code
      : "denied";
  }
}

async function connectOutcome(host, port) {
  return new Promise((resolve) => {
    const socket = net.createConnection({ host, port });
    const timer = setTimeout(() => {
      socket.destroy();
      resolve("timeout");
    }, 1_000);
    socket.once("connect", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve("allowed");
    });
    socket.once("error", (cause) => {
      clearTimeout(timer);
      socket.destroy();
      resolve(cause && typeof cause.code === "string" ? cause.code : "denied");
    });
  });
}

async function executableOutcome(name) {
  for (const directory of (process.env.PATH ?? "").split(path.delimiter).filter(Boolean)) {
    try {
      await access(path.join(directory, name), 1);
      return "present";
    } catch (cause) {
      if (cause && typeof cause === "object" && ["EACCES", "ENOENT"].includes(cause.code)) {
        continue;
      }
      throw cause;
    }
  }
  return "absent";
}

async function processLimitObserved() {
  const children = [];
  let refused = false;
  for (let index = 0; index < 96; index += 1) {
    const child = spawn("/bin/sleep", ["30"], {
      shell: false,
      stdio: "ignore",
    });
    const started = await new Promise((resolve, reject) => {
      child.once("spawn", () => resolve(true));
      child.once("error", (cause) => {
        if (cause && typeof cause === "object" && ["EAGAIN", "ENOMEM"].includes(cause.code)) {
          resolve(false);
        } else {
          reject(cause);
        }
      });
    });
    if (!started) {
      refused = true;
      break;
    }
    children.push(child);
  }
  await Promise.all(
    children.map(
      (child) =>
        new Promise((resolve) => {
          if (child.exitCode !== null || child.signalCode !== null) {
            resolve();
            return;
          }
          child.once("close", resolve);
          child.kill("SIGKILL");
        }),
    ),
  );
  return { refused, started: children.length };
}

const allowedPath = path.join(request.workspace, "container-write.txt");
await writeFile(allowedPath, `${request.nonce}\n`, { encoding: "utf8", mode: 0o600 });
const allowedText = await readFile(allowedPath, "utf8");
const statusText = await readFile("/proc/self/status", "utf8");
const limitsText = await readFile("/proc/self/limits", "utf8");
const memoryMax = (await readFile("/sys/fs/cgroup/memory.max", "utf8")).trim();
const pidsMax = (await readFile("/sys/fs/cgroup/pids.max", "utf8")).trim();
const cpuMax = (await readFile("/sys/fs/cgroup/cpu.max", "utf8")).trim();
const tmpStats = await statfs("/tmp");
const processLimit = await processLimitObserved();

const result = {
  stdioNonceMatched: allowedText.trim() === request.nonce,
  identity: {
    uid: process.getuid?.() ?? null,
    gid: process.getgid?.() ?? null,
    noNewPrivileges: /^NoNewPrivs:\s+1$/m.test(statusText),
    effectiveCapabilitiesZero: /^CapEff:\s+0+$/m.test(statusText),
  },
  filesystem: {
    workspaceWrite: "allowed",
    sourceRead: await outcome(() => access(request.source, 4)),
    sourceWrite: await outcome(() => writeFile(path.join(request.source, "escaped"), "x")),
    siblingRead: await outcome(() => access(request.sibling, 4)),
    siblingWrite: await outcome(() => writeFile(path.join(request.sibling, "escaped"), "x")),
    engineRead: await outcome(() => access(request.engineCredential, 4)),
    engineWrite: await outcome(() => writeFile(path.join(request.engine, "escaped"), "x")),
    hostHomeRead: await outcome(() => access(request.hostHome, 4)),
    providerCredentialRead: await outcome(() => access(request.providerCredential, 4)),
  },
  control: {
    paseoExecutable: await executableOutcome("paseo"),
    ghExecutable: await executableOutcome("gh"),
    dockerExecutable: await executableOutcome("docker"),
    podmanExecutable: await executableOutcome("podman"),
    dockerSocket: await outcome(() => access("/var/run/docker.sock", 6)),
    podmanSocket: await outcome(() => access("/run/podman/podman.sock", 6)),
    hostDaemonLoopback: await connectOutcome("127.0.0.1", request.daemonPort),
  },
  network: {
    publicIpv4: await connectOutcome("1.1.1.1", 443),
    dnsConfiguration: await outcome(() => access("/etc/resolv.conf", 4)),
  },
  limits: {
    memoryMax,
    pidsMax,
    cpuMax,
    fileSizeLimitPresent: !/^Max file size\s+unlimited\s+unlimited/m.test(limitsText),
    tmpBytes: Number(tmpStats.blocks) * Number(tmpStats.bsize),
    processLimit,
  },
};

process.stdout.write(`${JSON.stringify(result)}\n`);

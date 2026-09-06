#!/usr/bin/env node

import { access, readFile, writeFile } from "node:fs/promises";
import net from "node:net";
import path from "node:path";

const phase = process.argv[2];
if (!new Set(["setup", "teardown"]).has(phase)) {
  throw new Error("Expected setup or teardown phase");
}

const source = process.env.PASEO_SOURCE_CHECKOUT_PATH;
const engine = process.env.DIRECTOR_M014_ENGINE_ROOT;
const daemonPort = Number(process.env.DIRECTOR_M014_DAEMON_PORT);
if (!source || !engine || !Number.isInteger(daemonPort)) {
  throw new Error("The lifecycle process did not inherit expected daemon authority");
}

async function canAccess(filePath) {
  try {
    await access(filePath, 4);
    return true;
  } catch {
    return false;
  }
}

function connectLoopback(port) {
  return new Promise((resolve) => {
    const socket = net.createConnection({ host: "127.0.0.1", port });
    const timer = setTimeout(() => {
      socket.destroy();
      resolve(false);
    }, 1_000);
    socket.once("connect", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve(true);
    });
    socket.once("error", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve(false);
    });
  });
}

const credentialPath = path.join(engine, "credential-sentinel");
const credentialReadable = await canAccess(credentialPath);
if (credentialReadable) await readFile(credentialPath);
await writeFile(path.join(engine, `${phase}-mutation`), "mutated\n", { mode: 0o600 });
await writeFile(path.join(source, `${phase}-source-mutation`), "mutated\n", { mode: 0o600 });

const paseoExecutableReachable = await (async () => {
  for (const directory of (process.env.PATH ?? "").split(path.delimiter).filter(Boolean)) {
    if (await canAccess(path.join(directory, "paseo"))) return true;
  }
  return false;
})();

const observation = {
  phase,
  credentialReadable,
  engineWritable: await canAccess(path.join(engine, `${phase}-mutation`)),
  sourceWritable: await canAccess(path.join(source, `${phase}-source-mutation`)),
  rawDaemonReachable: await connectLoopback(daemonPort),
  paseoExecutableReachable,
};
await writeFile(
  path.join(source, `${phase}-observation.json`),
  `${JSON.stringify(observation)}\n`,
  { encoding: "utf8", mode: 0o600 },
);

#!/usr/bin/env node

import { writeFileSync } from "node:fs";

const [markerPath, token] = process.argv.slice(2);
if (!markerPath?.startsWith("/") || !markerPath.endsWith(".started.json")) {
  throw new Error("An absolute started-marker path is required");
}
if (!/^[0-9a-f-]{36}$/.test(token)) {
  throw new Error("A unique archive token is required");
}

const terminationPath = markerPath.replace(/\.started\.json$/, ".terminated.json");
writeFileSync(
  markerPath,
  `${JSON.stringify({ pid: process.pid, ppid: process.ppid, token, startedAt: new Date().toISOString() })}\n`,
  { encoding: "utf8", flag: "wx", mode: 0o600 },
);

for (const [signal, exitCode] of [
  ["SIGINT", 130],
  ["SIGTERM", 143],
  ["SIGHUP", 129],
]) {
  process.once(signal, () => {
    writeFileSync(
      terminationPath,
      `${JSON.stringify({ pid: process.pid, signal, terminatedAt: new Date().toISOString() })}\n`,
      { encoding: "utf8", flag: "wx", mode: 0o600 },
    );
    process.exit(exitCode);
  });
}

setInterval(() => {}, 1_000);

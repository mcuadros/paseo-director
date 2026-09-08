#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import {
  CoordinatorError,
  CoordinatorInterruption,
  canonicalJson,
  errorOutput,
  execute,
  parseCli,
} from "./coordinator.mjs";

let command = process.argv[2] ?? null;
try {
  const parsed = parseCli(process.argv.slice(2));
  command = parsed.command;
  const output = await execute(parsed.command, parsed.options);
  process.stdout.write(`${canonicalJson(output)}\n`);
} catch (error) {
  process.stdout.write(`${canonicalJson(errorOutput(command, error))}\n`);
  process.exitCode =
    error instanceof CoordinatorError || error instanceof CoordinatorInterruption ? 2 : 1;
}

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

let command = null;
let ownership;
let ownershipFile;
try {
  const parsed = parseCli(process.argv.slice(2));
  command = parsed.command;
  ownership = parsed.options.ownership;
  ownershipFile = parsed.options.ownershipFile;
  const output = await execute(parsed.command, parsed.options);
  process.stdout.write(`${canonicalJson(output)}\n`);
} catch (error) {
  process.stdout.write(`${canonicalJson(errorOutput(command, error, ownership, ownershipFile))}\n`);
  process.exitCode =
    error instanceof CoordinatorError || error instanceof CoordinatorInterruption ? 2 : 1;
}

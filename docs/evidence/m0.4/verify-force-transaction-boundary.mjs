#!/usr/bin/env node

import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:net";
import { randomBytes } from "node:crypto";

const doltBin = process.env.DOLT_BIN ?? "dolt";
const runId = randomBytes(8).toString("hex");
const database = `force_boundary_${runId}`;
const ownerUser = `owner_${runId}`;
const appUser = `app_${runId}`;
const ownerPassword = randomBytes(32).toString("base64url");
const appPassword = randomBytes(32).toString("base64url");
const secrets = [ownerPassword, appPassword];
const root = mkdtempSync(join(tmpdir(), `director-m0.4-force-boundary-${runId}-`));
const rootBasename = basename(root);
const databaseDir = join(root, database);
const configDir = join(root, ".doltcfg");
const clientRoot = join(root, "client-root");
const serverConfig = join(root, "config.yaml");
const clientBaseEnv = { DOLT_ROOT_PATH: clientRoot };
const ownerEnv = {
  ...clientBaseEnv,
  DOLT_CLI_USER: ownerUser,
  DOLT_CLI_PASSWORD: ownerPassword,
};
const appEnv = {
  ...clientBaseEnv,
  DOLT_CLI_USER: appUser,
  DOLT_CLI_PASSWORD: appPassword,
};

let server;
let serverPort;
let serverOutput = "";
let cleanupPromise;
let termination;

function assert(condition, message) {
  if (!condition) {
    throw new Error(`unexpected observation: ${message}`);
  }
}

function execute(args, { cwd, env = {}, input, timeout = 15_000 } = {}) {
  const renderedArgs = args.join("\0");
  assert(!secrets.some((secret) => renderedArgs.includes(secret)), "credential entered argv");
  const result = spawnSync(doltBin, args, {
    cwd,
    env: { ...process.env, ...env },
    input,
    encoding: "utf8",
    timeout,
    killSignal: "SIGKILL",
    maxBuffer: 4 * 1024 * 1024,
  });
  if (result.error) {
    throw result.error;
  }
  const output = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
  assert(!secrets.some((secret) => output.includes(secret)), "credential entered command output");
  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}

function successfully(args, options) {
  const result = execute(args, options);
  assert(result.code === 0, `${args.join(" ")} failed: ${result.stderr || result.stdout}`);
  return result;
}

function parseJson(result) {
  const output = result.stdout.trim();
  try {
    return JSON.parse(output);
  } catch (initialError) {
    for (let index = output.lastIndexOf("{"); index >= 0; index = output.lastIndexOf("{", index - 1)) {
      try {
        return JSON.parse(output.slice(index));
      } catch {
        // Dolt emits one JSON document per result-bearing statement.
      }
    }
    throw initialError;
  }
}

function firstRow(result) {
  return parseJson(result).rows[0];
}

function remoteArgs(sql) {
  return [
    "--host=127.0.0.1",
    `--port=${serverPort}`,
    "--no-tls",
    `--use-db=${database}`,
    "sql",
    "-q",
    sql,
    "-r",
    "json",
  ];
}

function asOwner(sql) {
  return execute(remoteArgs(sql), { env: ownerEnv });
}

function asApp(sql) {
  return execute(remoteArgs(sql), { env: appEnv });
}

function ownerSuccessfully(sql) {
  const result = asOwner(sql);
  assert(result.code === 0, `owner SQL failed: ${result.stderr || result.stdout}`);
  return result;
}

function appSuccessfully(sql) {
  const result = asApp(sql);
  assert(result.code === 0, `application SQL failed: ${result.stderr || result.stdout}`);
  return result;
}

function reservePort() {
  return new Promise((resolve, reject) => {
    const listener = createServer();
    listener.once("error", reject);
    listener.listen(0, "127.0.0.1", () => {
      const address = listener.address();
      assert(address && typeof address === "object", "ephemeral port was unavailable");
      const port = address.port;
      listener.close((error) => (error ? reject(error) : resolve(port)));
    });
  });
}

function running() {
  return server && server.exitCode === null && server.signalCode === null;
}

async function waitForServer() {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    assert(running(), `Dolt server exited before readiness: ${serverOutput}`);
    const result = asOwner("SELECT 1 AS ready");
    if (result.code === 0) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Dolt server did not become ready: ${serverOutput}`);
}

function cleanup() {
  if (!cleanupPromise) {
    cleanupPromise = (async () => {
      if (running()) {
        server.kill("SIGTERM");
        const deadline = Date.now() + 5_000;
        while (running() && Date.now() < deadline) {
          await new Promise((resolve) => setTimeout(resolve, 25));
        }
        if (running()) {
          server.kill("SIGKILL");
          const forcedDeadline = Date.now() + 5_000;
          while (running() && Date.now() < forcedDeadline) {
            await new Promise((resolve) => setTimeout(resolve, 25));
          }
        }
      }
      assert(!running(), "owned Dolt server did not terminate");
      if (server) {
        termination = {
          pid: server.pid,
          exit_code: server.exitCode,
          signal: server.signalCode,
        };
      }
      rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
      assert(!existsSync(root), "owned reproduction directory remained after cleanup");
    })();
  }
  return cleanupPromise;
}

async function main() {
  const doltVersion = successfully(["version"]).stdout.trim();
  const os = spawnSync("uname", ["-srvmo"], { encoding: "utf8" }).stdout.trim();
  mkdirSync(databaseDir, { mode: 0o700 });
  mkdirSync(clientRoot, { mode: 0o700 });
  successfully(
    ["init", "--name=Director TaskStore spike", "--email=spike@example.invalid"],
    { cwd: databaseDir, env: clientBaseEnv },
  );

  const bootstrap = `
    CREATE TABLE immutable_records (
      id VARCHAR(64) PRIMARY KEY,
      payload JSON NOT NULL
    );
    CREATE TABLE aggregates (
      id VARCHAR(64) PRIMARY KEY,
      version BIGINT UNSIGNED NOT NULL,
      data JSON NOT NULL
    );
    CREATE USER '${ownerUser}'@'%' IDENTIFIED WITH mysql_native_password BY '${ownerPassword}';
    GRANT ALL ON *.* TO '${ownerUser}'@'%' WITH GRANT OPTION;
    CREATE USER '${appUser}'@'%' IDENTIFIED WITH mysql_native_password BY '${appPassword}';
    GRANT SELECT, INSERT ON \`${database}\`.immutable_records TO '${appUser}'@'%';
    GRANT SELECT, INSERT, UPDATE ON \`${database}\`.aggregates TO '${appUser}'@'%';
    DROP USER IF EXISTS 'root'@'%';
    FLUSH PRIVILEGES;
    DROP USER IF EXISTS 'root'@'localhost';
  `;
  successfully(
    [
      `--data-dir=${root}`,
      `--doltcfg-dir=${configDir}`,
      `--use-db=${database}`,
      "sql",
      "-r",
      "json",
    ],
    { env: clientBaseEnv, input: bootstrap },
  );

  serverPort = await reservePort();
  writeFileSync(
    serverConfig,
    `log_level: warning
data_dir: ${JSON.stringify(root)}
cfg_dir: ${JSON.stringify(configDir)}
privilege_file: ${JSON.stringify(join(configDir, "privileges.db"))}
branch_control_file: ${JSON.stringify(join(configDir, "branch_control.db"))}
behavior:
  read_only: false
listener:
  host: 127.0.0.1
  port: ${serverPort}
system_variables:
  dolt_force_transaction_commit: 0
user_session_vars:
  - name: ${JSON.stringify(appUser)}
    vars:
      dolt_force_transaction_commit: 0
`,
    { mode: 0o600 },
  );

  server = spawn(doltBin, ["sql-server", `--config=${serverConfig}`], {
    cwd: root,
    env: { ...process.env, ...clientBaseEnv },
    stdio: ["ignore", "pipe", "pipe"],
  });
  server.stdout.setEncoding("utf8");
  server.stderr.setEncoding("utf8");
  server.stdout.on("data", (chunk) => {
    serverOutput += chunk;
  });
  server.stderr.on("data", (chunk) => {
    serverOutput += chunk;
  });
  await new Promise((resolve, reject) => {
    server.once("spawn", resolve);
    server.once("error", reject);
  });
  await waitForServer();

  const initialVariables = firstRow(
    appSuccessfully(
      "SELECT @@SESSION.dolt_force_transaction_commit AS session_value, @@GLOBAL.dolt_force_transaction_commit AS global_value",
    ),
  );
  assert(
    initialVariables.session_value === "0" && initialVariables.global_value === "0",
    `configured safe defaults were not applied: ${JSON.stringify(initialVariables)}`,
  );

  const grantRows = parseJson(
    ownerSuccessfully(`SHOW GRANTS FOR '${appUser}'@'%'`),
  ).rows;
  const grants = grantRows.flatMap((row) => Object.values(row).map(String));
  assert(
    grants.some((grant) => grant.includes(`immutable_records`)) &&
      grants.some((grant) => grant.includes(`aggregates`)) &&
      !grants.some(
        (grant) =>
          /SYSTEM_VARIABLES_ADMIN|SUPER/i.test(grant) ||
          (/ ON \*\.\* TO /i.test(grant) && !/GRANT USAGE ON \*\.\*/i.test(grant)),
      ),
    `application grants were not table-scoped: ${JSON.stringify(grants)}`,
  );

  appSuccessfully(`
    INSERT INTO immutable_records (id, payload)
      VALUES ('immutable-1', JSON_OBJECT('value', 'before'));
    INSERT INTO aggregates (id, version, data)
      VALUES ('aggregate-1', 0, JSON_OBJECT('value', 'before'));
    UPDATE aggregates SET version = 1, data = JSON_OBJECT('value', 'after')
      WHERE id = 'aggregate-1' AND version = 0;
  `);
  const requiredStateBefore = firstRow(
    appSuccessfully(`
      SELECT
        (SELECT COUNT(*) FROM immutable_records WHERE id = 'immutable-1') AS immutable_rows,
        (SELECT version FROM aggregates WHERE id = 'aggregate-1') AS aggregate_version
    `),
  );
  assert(
    requiredStateBefore.immutable_rows === "1" && requiredStateBefore.aggregate_version === "1",
    `required TaskStore operations failed before override probes: ${JSON.stringify(
      requiredStateBefore,
    )}`,
  );

  const sessionOverride = firstRow(
    appSuccessfully(`
      SET @@SESSION.dolt_force_transaction_commit = 1;
      SELECT @@SESSION.dolt_force_transaction_commit AS session_value
    `),
  );
  assert(sessionOverride.session_value === "1", "application session override was not observed");

  appSuccessfully("SET @@GLOBAL.dolt_force_transaction_commit = 1");
  const globalAfterAppSet = firstRow(
    ownerSuccessfully(
      "SELECT @@GLOBAL.dolt_force_transaction_commit AS global_value",
    ),
  );
  assert(
    globalAfterAppSet.global_value === "1",
    "application global override was not visible to the owner",
  );

  const cleanAppAfterGlobalOverride = asApp(
    "SELECT @@SESSION.dolt_force_transaction_commit AS session_value",
  );
  assert(
    cleanAppAfterGlobalOverride.code !== 0 &&
      `${cleanAppAfterGlobalOverride.stdout}${cleanAppAfterGlobalOverride.stderr}`.includes(
        "initialized more than 1x",
      ),
    `per-user safe default unexpectedly contained the global override: ${
      cleanAppAfterGlobalOverride.stderr || cleanAppAfterGlobalOverride.stdout
    }`,
  );

  ownerSuccessfully("SET @@GLOBAL.dolt_force_transaction_commit = 0");
  const globalAfterReset = firstRow(
    ownerSuccessfully(
      "SELECT @@GLOBAL.dolt_force_transaction_commit AS global_value",
    ),
  );
  assert(globalAfterReset.global_value === "0", "owner did not restore the global safe default");

  appSuccessfully(`
    INSERT INTO immutable_records (id, payload)
      VALUES ('immutable-2', JSON_OBJECT('value', 'after reset'));
    UPDATE aggregates SET version = 2, data = JSON_OBJECT('value', 'after reset')
      WHERE id = 'aggregate-1' AND version = 1;
  `);
  const requiredStateAfterReset = firstRow(
    appSuccessfully(`
      SELECT
        (SELECT COUNT(*) FROM immutable_records) AS immutable_rows,
        (SELECT version FROM aggregates WHERE id = 'aggregate-1') AS aggregate_version
    `),
  );
  assert(
    requiredStateAfterReset.immutable_rows === "2" &&
      requiredStateAfterReset.aggregate_version === "2",
    `required TaskStore operations did not resume after owner reset: ${JSON.stringify(
      requiredStateAfterReset,
    )}`,
  );

  assert(
    !secrets.some((secret) => readFileSync(serverConfig, "utf8").includes(secret)) &&
      !secrets.some((secret) => serverOutput.includes(secret)),
    "minimal reproduction persisted or logged a generated credential",
  );

  await cleanup();
  const report = {
    schema_version: 1,
    question:
      "Can supported Dolt 2.3.2 privilege or connection configuration prevent the normal application identity from changing session or global dolt_force_transaction_commit while preserving required TaskStore writes?",
    environment: {
      run_id: runId,
      dolt: doltVersion,
      operating_system: os,
      topology:
        "one disposable loopback SQL server; system_variables and per-user user_session_vars initialize dolt_force_transaction_commit to 0; one owner and one table-scoped application identity",
      server: termination,
      owned_temp_directory: {
        basename: rootBasename,
        removed: !existsSync(root),
      },
    },
    supported_controls: {
      system_variables_initial_global_value: Number(initialVariables.global_value),
      user_session_vars_initial_session_value: Number(initialVariables.session_value),
      read_only_rejected_as_boundary:
        "The public behavior.read_only control disables database modification, including required TaskStore INSERT and UPDATE operations.",
      per_user_statement_allowlist_available: false,
    },
    application_identity: {
      grants,
      global_admin_or_super_grant_present: false,
      required_operations_before_probes: {
        immutable_rows: Number(requiredStateBefore.immutable_rows),
        aggregate_version: Number(requiredStateBefore.aggregate_version),
      },
      required_operations_after_owner_reset: {
        immutable_rows: Number(requiredStateAfterReset.immutable_rows),
        aggregate_version: Number(requiredStateAfterReset.aggregate_version),
      },
    },
    override_results: {
      session_set_accepted: true,
      session_value_after_set: Number(sessionOverride.session_value),
      global_set_accepted: true,
      owner_observed_global_value_after_app_set: Number(globalAfterAppSet.global_value),
      new_app_connection_after_global_override_rejected: true,
      new_app_connection_diagnostic:
        "Variable 'dolt_force_transaction_commit' was initialized more than 1x",
      owner_restored_global_value: Number(globalAfterReset.global_value),
    },
    decision: {
      enforceable_supported_boundary: false,
      direct_dolt_2_3_2: "NO-GO",
      reason:
        "The table-scoped application identity can override both configured safe defaults. Dolt 2.3.2 exposes no supported per-user SET restriction or statement allowlist, and read-only mode removes required writes.",
      activated_contingency: "dir-m0.16",
    },
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
}

try {
  await main();
} catch (error) {
  let cleanupError;
  try {
    await cleanup();
  } catch (caught) {
    cleanupError = caught;
  }
  process.stderr.write(
    `DIRECTOR_M04_FORCE_BOUNDARY_FAILURE ${JSON.stringify({
      run_id: runId,
      server_pid: server?.pid ?? null,
      server_port: serverPort ?? null,
      temp_dir_basename: rootBasename,
    })}\n${error instanceof Error ? error.stack : String(error)}\n${
      cleanupError ? `cleanup failure: ${String(cleanupError)}\n` : ""
    }`,
  );
  process.exitCode = 1;
}

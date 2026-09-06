#!/usr/bin/env node

import {
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:net";
import { createHash, createHmac, randomBytes } from "node:crypto";

const bdBin = process.env.BD_BIN ?? "bd";
const doltBin = process.env.DOLT_BIN ?? "dolt";
const runId = randomBytes(8).toString("hex");
const directDatabase = `direct_${runId}`;
const beadsDatabase = `beads_${runId}`;
const beadsPrefix = `bc${runId.slice(0, 8)}`;
const ownerUser = `owner_${runId}`;
const appUser = `app_${runId}`;
const beadsUser = `beads_${runId}`;
const ownerPassword = randomBytes(32).toString("base64url");
const appPassword = randomBytes(32).toString("base64url");
const beadsPassword = randomBytes(32).toString("base64url");
const identityProof = createHmac("sha256", ownerPassword).update(runId).digest("hex");
const tempRoot = mkdtempSync(join(tmpdir(), `director-m0.4-${runId}-`));
const tempRootBasename = basename(tempRoot);
const doltClientRoot = join(tempRoot, "client-root");
const doltConfig = join(tempRoot, ".doltcfg");
const directStore = join(tempRoot, directDatabase);
const beadsStore = join(tempRoot, beadsDatabase);
const beadsProject = join(tempRoot, "beads-project");

const beadIds = Object.fromEntries(
  ["epic", "task", "blocker", "writer-a", "writer-b", "event", "command"].map((name) => [
    name,
    `${beadsPrefix}-${name}`,
  ]),
);

const immutableTableModel = [
  { table: "command_requests", identities: [{ name: "pk", columns: ["idempotency_key"] }] },
  { table: "command_outcomes", identities: [{ name: "pk", columns: ["idempotency_key"] }] },
  {
    table: "candidates",
    identities: [
      { name: "pk", columns: ["id"] },
      { name: "run_sequence", columns: ["run_id", "sequence"] },
    ],
  },
  {
    table: "events",
    identities: [
      { name: "pk", columns: ["global_sequence"] },
      { name: "event_id", columns: ["event_id"] },
      { name: "run_sequence", columns: ["run_id", "sequence"] },
    ],
  },
  {
    table: "audit_entries",
    identities: [
      { name: "pk", columns: ["sequence"] },
      { name: "audit_id", columns: ["audit_id"] },
    ],
  },
];

const mutableAggregateIdentityModel = [
  { table: "aggregates", identities: [{ name: "pk", columns: ["id"] }] },
];

const identityLedgerModel = [...mutableAggregateIdentityModel, ...immutableTableModel];
const focus = process.env.DIRECTOR_M04_FOCUS ?? "full";
assertObservation(
  focus === "full" || focus === "referenced-parent-identities",
  `unsupported DIRECTOR_M04_FOCUS value: ${focus}`,
);

const parentMissingSentinel = "guard.parent_missing";

const secretValues = [ownerPassword, appPassword, beadsPassword];
const doltClientEnv = { DOLT_ROOT_PATH: doltClientRoot };
const ownerDoltEnv = {
  ...doltClientEnv,
  DOLT_CLI_USER: ownerUser,
  DOLT_CLI_PASSWORD: ownerPassword,
};
const appDoltEnv = {
  ...doltClientEnv,
  DOLT_CLI_USER: appUser,
  DOLT_CLI_PASSWORD: appPassword,
};
const rootProbeEnv = {
  ...doltClientEnv,
  DOLT_CLI_USER: "root",
  DOLT_CLI_PASSWORD: "",
};
const beadsClientEnv = {
  BEADS_DOLT_PASSWORD: beadsPassword,
  DOLT_ROOT_PATH: doltClientRoot,
};

let serverProcess;
let serverStdout = "";
let serverStderr = "";
let cleanupPromise;
let interruptedSignal;
let serverIdentityVerified = false;
let serverTermination;
let serverPort;
let contentionEvidence;
let immutabilityEvidence;
let boundaryEvidence;
const credentialTransportChecks = [];

const checks = [];
const liveChildren = new Set();
const statementTimeoutMs = Number(process.env.DIRECTOR_M04_STATEMENT_TIMEOUT_MS ?? 60_000);
const adversarialTimeoutMs = Number(process.env.DIRECTOR_M04_ADVERSARIAL_TIMEOUT_MS ?? 4_000);

function execute(command, args, options = {}) {
  const renderedArgs = args.join("\u0000");
  if (secretValues.some((secret) => renderedArgs.includes(secret))) {
    throw new Error("ephemeral credential was placed in subprocess argv");
  }
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: { ...process.env, ...options.env },
    input: options.input,
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
    timeout: options.timeout ?? statementTimeoutMs,
    killSignal: "SIGKILL",
  });

  const timedOut =
    result.error?.code === "ETIMEDOUT" || (result.signal === "SIGKILL" && options.timeout);
  if (timedOut && !options.tolerateTimeout) {
    throw new Error(
      `subprocess exceeded its ${options.timeout ?? statementTimeoutMs}ms bound and was killed: ${command} ${args
        .filter((argument) => !secretValues.some((secret) => argument.includes(secret)))
        .join(" ")
        .slice(0, 400)}`,
    );
  }
  if (timedOut) {
    return {
      code: 1,
      timedOut: true,
      signal: result.signal,
      stdout: result.stdout ?? "",
      stderr: `${result.stderr ?? ""}\nstatement did not return within ${
        options.timeout ?? statementTimeoutMs
      }ms and was killed`,
    };
  }
  if (result.error) {
    throw result.error;
  }

  const renderedOutput = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
  if (secretValues.some((secret) => renderedOutput.includes(secret))) {
    throw new Error("subprocess output exposed an ephemeral credential");
  }

  return {
    code: result.status ?? 1,
    signal: result.signal,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}

function executeSuccessfully(command, args, options = {}) {
  const result = execute(command, args, options);
  if (result.code !== 0) {
    throw new Error(
      `${command} ${args.join(" ")} failed (${result.code}): ${result.stderr || result.stdout}`,
    );
  }
  return result;
}

function executeConcurrently(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const renderedArgs = args.join("\u0000");
    if (secretValues.some((secret) => renderedArgs.includes(secret))) {
      reject(new Error("ephemeral credential was placed in concurrent subprocess argv"));
      return;
    }
    const child = trackChild(
      spawn(command, args, {
        cwd: options.cwd,
        env: { ...process.env, ...options.env },
        stdio: ["ignore", "pipe", "pipe"],
      }),
    );
    let stdout = "";
    let stderr = "";

    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("error", reject);
    child.on("close", (code, signal) => {
      if (secretValues.some((secret) => `${stdout}\n${stderr}`.includes(secret))) {
        reject(new Error("concurrent subprocess output exposed an ephemeral credential"));
        return;
      }
      resolve({ code: code ?? 1, signal, stdout, stderr });
    });
  });
}

function assertObservation(condition, message) {
  if (!condition) {
    throw new Error(`unexpected observation: ${message}`);
  }
}

function isRunning(child) {
  return child && child.exitCode === null && child.signalCode === null;
}

function trackChild(child) {
  liveChildren.add(child);
  child.once("close", () => liveChildren.delete(child));
  return child;
}

async function settleTrackedChildren() {
  const deadline = Date.now() + 5_000;
  for (const child of [...liveChildren]) {
    if (isRunning(child)) {
      child.kill("SIGTERM");
    }
  }
  while ([...liveChildren].some(isRunning) && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  for (const child of [...liveChildren]) {
    if (isRunning(child)) {
      child.kill("SIGKILL");
    }
  }
  const forced = Date.now() + 5_000;
  while ([...liveChildren].some(isRunning) && Date.now() < forced) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  return [...liveChildren].filter(isRunning).length;
}

function record(id, system, contractResult, observed) {
  checks.push({ id, system, contract_result: contractResult, observed });
}

function parseJson(text) {
  return JSON.parse(text.trim());
}

function bd(args, options = {}) {
  return execute(bdBin, args, {
    ...options,
    env: { ...beadsClientEnv, ...options.env },
  });
}

function bdSuccessfully(args, options = {}) {
  return executeSuccessfully(bdBin, args, {
    ...options,
    env: { ...beadsClientEnv, ...options.env },
  });
}

function remoteDoltArgs(sql) {
  assertObservation(Number.isInteger(serverPort), "server port is unavailable");
  return [
    "--host=127.0.0.1",
    `--port=${serverPort}`,
    "--no-tls",
    `--use-db=${directDatabase}`,
    "sql",
    "-q",
    sql,
    "-r",
    "json",
  ];
}

function ownerDoltArgs(sql) {
  return { argv: remoteDoltArgs(sql), env: ownerDoltEnv };
}

function appDoltArgs(sql) {
  return { argv: remoteDoltArgs(sql), env: appDoltEnv };
}

function rootProbeDoltArgs(sql) {
  return { argv: remoteDoltArgs(sql), env: rootProbeEnv };
}

function unpackDoltInvocation(invocation) {
  if (Array.isArray(invocation)) {
    return { argv: invocation, env: doltClientEnv };
  }
  return invocation;
}

function doltSuccessfully(invocation, options = {}) {
  const { argv, env } = unpackDoltInvocation(invocation);
  return executeSuccessfully(doltBin, argv, {
    ...options,
    env: { ...env, ...options.env },
  });
}

function dolt(invocation, options = {}) {
  const { argv, env } = unpackDoltInvocation(invocation);
  return execute(doltBin, argv, {
    ...options,
    env: { ...env, ...options.env },
  });
}

function executeSecretBootstrap(sql) {
  const result = execute(
    doltBin,
    [
      `--data-dir=${tempRoot}`,
      `--doltcfg-dir=${doltConfig}`,
      `--use-db=${directDatabase}`,
      "sql",
      "-r",
      "json",
    ],
    { env: doltClientEnv, input: sql },
  );
  if (result.code !== 0) {
    const diagnostic = `${result.stdout}\n${result.stderr}`;
    const redacted = [ownerPassword, appPassword, beadsPassword].reduce(
      (text, secret) => text.replaceAll(secret, "<redacted>"),
      diagnostic,
    );
    throw new Error(`authenticated Dolt bootstrap failed: ${redacted.trim()}`);
  }
}

function appendOnlyLedgerDdl(spec, identityWidth) {
  const values = spec.identities
    .map(
      (identity) =>
        `(CONCAT('${spec.table}.${identity.name}', ${identity.columns
          .map((column) => `CHAR(31), NEW.${column}`)
          .join(", ")}))`,
    )
    .join(", ");
  return `
    CREATE TABLE ${spec.table}_identity (
      identity VARBINARY(${identityWidth}) NOT NULL PRIMARY KEY
    );
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER ${spec.table}_append_only
      BEFORE INSERT ON ${spec.table} FOR EACH ROW
      INSERT INTO ${spec.table}_identity (identity) VALUES ${values};
  `;
}

function commandRequestHash(command) {
  const canonical = JSON.stringify({
    aggregate_id: command.aggregateId,
    expected_version: command.expectedVersion,
    payload: command.payload,
  });
  return createHash("sha256").update(canonical).digest("hex");
}

function sqlString(value) {
  assertObservation(/^[a-zA-Z0-9_.:@-]+$/.test(value), "unsafe SQL fixture identifier");
  return `'${value}'`;
}

function sqlLiteral(value) {
  assertObservation(
    !value.includes("'") && !value.includes("\\") && !secretValues.some((secret) => value.includes(secret)),
    "unsafe SQL fixture literal",
  );
  return `'${value}'`;
}

function firstRow(result) {
  const document = parseJson(result.stdout);
  assertObservation(Array.isArray(document.rows) && document.rows.length > 0, "query returned no rows");
  return document.rows[0];
}

function openAppSqlSession(sessionEnv = appDoltEnv) {
  const argv = [
    "--host=127.0.0.1",
    `--port=${serverPort}`,
    "--no-tls",
    `--use-db=${directDatabase}`,
    "sql",
    "-r",
    "json",
  ];
  assertObservation(
    !secretValues.some((secret) => argv.join("\u0000").includes(secret)),
    "ephemeral credential was placed in SQL session argv",
  );
  const child = trackChild(
    spawn(doltBin, argv, {
      env: { ...process.env, ...sessionEnv },
      stdio: ["pipe", "pipe", "pipe"],
    }),
  );
  const session = {
    child,
    stdout: "",
    stderr: "",
    spawnError: undefined,
    closed: undefined,
  };
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    session.stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    session.stderr += chunk;
  });
  child.on("error", (error) => {
    session.spawnError = error;
  });
  session.closed = new Promise((resolve) => {
    child.on("close", (code, signal) => {
      if (secretValues.some((secret) => `${session.stdout}\n${session.stderr}`.includes(secret))) {
        resolve({
          code: 1,
          signal,
          stdout: "",
          stderr: "SQL session output exposed an ephemeral credential",
        });
        return;
      }
      resolve({ code: code ?? 1, signal, stdout: session.stdout, stderr: session.stderr });
    });
  });
  return session;
}

async function runTransactionProbe(sql) {
  const session = openAppSqlSession();
  const tag = `TXNPROBE_${randomBytes(4).toString("hex")}`;
  session.child.stdin.write(`START TRANSACTION;\nSELECT '${tag}_OPEN' AS marker;\n`);
  await waitForSessionText(session, `${tag}_OPEN`);
  session.child.stdin.end(`${sql};\nCOMMIT;\n`);
  const result = await session.closed;
  const output = `${result.stdout}\n${result.stderr}`;
  return {
    code: result.code === 0 && !/Error \d+ \(/.test(output) ? 0 : 1,
    stdout: result.stdout,
    stderr: result.stderr,
    committed: output.includes(`${tag}_OPEN`) && result.code === 0,
  };
}

async function waitForSessionText(session, text) {
  const deadline = Date.now() + 15_000;
  while (!session.stdout.includes(text) && Date.now() < deadline) {
    if (session.spawnError) {
      throw session.spawnError;
    }
    if (!isRunning(session.child)) {
      throw new Error(`SQL session exited before barrier: ${session.stderr || session.stdout}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assertObservation(session.stdout.includes(text), `SQL session missed barrier ${text}`);
}

function commandFixture(suffix, title) {
  assertObservation(/^[a-z]$/.test(suffix), "invalid command suffix");
  assertObservation(/^[A-Za-z ]+$/.test(title), "invalid command title");
  const command = {
    key: `cmd-${runId}-${suffix}`,
    aggregateId: "task-contention",
    expectedVersion: 0,
    payload: { title },
  };
  return {
    ...command,
    requestHash: commandRequestHash(command),
    eventId: `event-${runId}-${suffix}`,
    auditId: `audit-${runId}-${suffix}`,
    runSequence: suffix.charCodeAt(0) - 87,
  };
}

function contendingTransaction(command) {
  const key = sqlString(command.key);
  const aggregateId = sqlString(command.aggregateId);
  const requestHash = sqlString(command.requestHash);
  const eventId = sqlString(command.eventId);
  const auditId = sqlString(command.auditId);
  const title = `'${command.payload.title}'`;
  return `
    INSERT INTO command_requests
      (idempotency_key, aggregate_id, expected_version, request_hash, payload)
      VALUES (${key}, ${aggregateId}, ${command.expectedVersion}, ${requestHash},
        JSON_OBJECT('title', ${title}));
    UPDATE aggregates
      SET version = version + 1, data = JSON_OBJECT('title', ${title})
      WHERE id = ${aggregateId} AND version = ${command.expectedVersion};
    SET @matched = ROW_COUNT();
    INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload)
      VALUES (${eventId}, 'run-1', ${command.runSequence}, ${aggregateId},
        ${command.expectedVersion + 1}, 'task.updated', JSON_OBJECT('title', ${title}));
    INSERT INTO audit_entries (audit_id, event_id, actor_type, payload)
      VALUES (${auditId}, ${eventId}, 'system', JSON_OBJECT('command', ${key}));
    INSERT INTO command_outcomes
      (idempotency_key, outcome_type, observed_version, event_id, payload)
      VALUES (${key}, 'applied', ${command.expectedVersion + 1}, ${eventId},
        JSON_OBJECT('title', ${title}));
    COMMIT;
    SELECT @matched AS matched;
  `;
}

function readCommand(command) {
  return firstRow(
    doltSuccessfully(
      appDoltArgs(`
        SELECT r.aggregate_id, r.expected_version, r.request_hash,
          JSON_UNQUOTE(JSON_EXTRACT(r.payload, '$.title')) AS request_title,
          o.outcome_type, o.observed_version, o.event_id,
          JSON_UNQUOTE(JSON_EXTRACT(o.payload, '$.title')) AS outcome_title
        FROM command_requests r
        JOIN command_outcomes o USING (idempotency_key)
        WHERE r.idempotency_key = ${sqlString(command.key)}
      `),
    ),
  );
}

class CommandPayloadMismatchError extends Error {
  constructor() {
    super("idempotency key already exists with a different request payload");
    this.name = "CommandPayloadMismatchError";
    this.code = "IDEMPOTENCY_PAYLOAD_MISMATCH";
  }
}

function replayCommand(command) {
  const stored = readCommand(command);
  const storedHash = commandRequestHash({
    aggregateId: stored.aggregate_id,
    expectedVersion: Number(stored.expected_version),
    payload: { title: stored.request_title },
  });
  if (storedHash !== stored.request_hash) {
    throw new Error("stored command request hash does not match its immutable fields");
  }
  if (stored.request_hash !== commandRequestHash(command)) {
    throw new CommandPayloadMismatchError();
  }
  return {
    replay: true,
    outcome: stored.outcome_type,
    observedVersion: Number(stored.observed_version),
    eventId: stored.event_id || null,
  };
}

async function reservePort() {
  if (process.env.DIRECTOR_M04_FORCED_PORT) {
    const forcedPort = Number(process.env.DIRECTOR_M04_FORCED_PORT);
    assertObservation(
      Number.isInteger(forcedPort) && forcedPort >= 1024 && forcedPort <= 65535,
      "DIRECTOR_M04_FORCED_PORT is invalid",
    );
    return forcedPort;
  }
  const probe = createServer();
  probe.unref();
  await new Promise((resolve, reject) => {
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", resolve);
  });
  const address = probe.address();
  assertObservation(address && typeof address !== "string", "could not reserve a TCP port");
  const port = address.port;
  await new Promise((resolve, reject) => probe.close((error) => (error ? reject(error) : resolve())));
  return port;
}

function assertCredentialTransport(phase) {
  const globalConfigPath = join(doltClientRoot, ".dolt", "config_global.json");
  const inspectedFiles = [];
  const visit = (path) => {
    const entry = lstatSync(path);
    if (entry.isDirectory()) {
      for (const name of readdirSync(path)) {
        visit(join(path, name));
      }
    } else if (entry.isFile()) {
      inspectedFiles.push(path);
    }
  };
  visit(tempRoot);
  const plaintextCredentialFiles = inspectedFiles.filter((path) => {
    const contents = readFileSync(path);
    return secretValues.some((secret) => contents.includes(Buffer.from(secret)));
  });
  let globalConfigMode = null;
  let profilePresent = false;
  if (existsSync(globalConfigPath)) {
    globalConfigMode = (statSync(globalConfigPath).mode & 0o777).toString(8).padStart(3, "0");
    const config = JSON.parse(readFileSync(globalConfigPath, "utf8"));
    profilePresent = Object.hasOwn(config, "profile");
  }
  const argvSources = [readFileSync(`/proc/${process.pid}/cmdline`)];
  const environmentSources = [Buffer.from(Object.entries(process.env).flat().join("\u0000"))];
  if (serverProcess?.pid && isRunning(serverProcess)) {
    argvSources.push(readFileSync(`/proc/${serverProcess.pid}/cmdline`));
    environmentSources.push(readFileSync(`/proc/${serverProcess.pid}/environ`));
  }
  const secretInArgv = argvSources.some((contents) =>
    secretValues.some((secret) => contents.includes(Buffer.from(secret))),
  );
  const secretInPersistentEnvironment = environmentSources.some((contents) =>
    secretValues.some((secret) => contents.includes(Buffer.from(secret))),
  );
  assertObservation(
    plaintextCredentialFiles.length === 0,
    `plaintext credential persisted during ${phase}`,
  );
  assertObservation(!profilePresent, `credential-bearing Dolt profile appeared during ${phase}`);
  assertObservation(
    !secretInArgv,
    `credential appeared in argv during ${phase}`,
  );
  assertObservation(!secretInPersistentEnvironment, `credential remained in a live process environment during ${phase}`);
  assertObservation(
    !secretValues.some((secret) => `${serverStdout}\n${serverStderr}`.includes(secret)),
    `credential appeared in server output during ${phase}`,
  );
  credentialTransportChecks.push({
    phase,
    transport: "ephemeral process environment",
    credential_profile_present: false,
    inspected_regular_files: inspectedFiles.length,
    plaintext_credential_files: 0,
    secret_in_argv_output_or_persistent_environment: false,
    credential_free_global_config_mode: globalConfigMode,
  });
}

async function waitForOwnedServer() {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (!isRunning(serverProcess)) {
      throw new Error(`owned Dolt server exited before readiness: ${serverStderr || serverStdout}`);
    }
    const result = dolt(
      ownerDoltArgs("SELECT run_id, identity_proof FROM harness_identity WHERE singleton = 1"),
    );
    if (result.code === 0) {
      const row = firstRow(result);
      if (row.run_id !== runId || row.identity_proof !== identityProof) {
        throw new Error("Dolt listener identity mismatch");
      }
      assertObservation(isRunning(serverProcess), "owned Dolt child exited during readiness proof");
      serverIdentityVerified = true;
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`owned Dolt server did not prove its identity: ${serverStderr || serverStdout}`);
}

function cleanup() {
  if (!cleanupPromise) {
    cleanupPromise = (async () => {
      const survivingClients = await settleTrackedChildren();
      if (survivingClients > 0) {
        throw new Error(`${survivingClients} owned client processes did not exit; storage was retained`);
      }
      if (isRunning(serverProcess)) {
        serverProcess.kill("SIGTERM");
        const gracefulDeadline = Date.now() + 5_000;
        while (isRunning(serverProcess) && Date.now() < gracefulDeadline) {
          await new Promise((resolve) => setTimeout(resolve, 50));
        }
        if (isRunning(serverProcess)) {
          serverProcess.kill("SIGKILL");
          const forcedDeadline = Date.now() + 5_000;
          while (isRunning(serverProcess) && Date.now() < forcedDeadline) {
            await new Promise((resolve) => setTimeout(resolve, 50));
          }
        }
        if (isRunning(serverProcess)) {
          throw new Error("owned Dolt server did not terminate; storage was retained");
        }
      }

      if (serverProcess) {
        serverTermination = {
          pid: serverProcess.pid,
          exit_code: serverProcess.exitCode,
          signal: serverProcess.signalCode,
        };
      }
      rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
      if (existsSync(tempRoot)) {
        throw new Error("owned temporary storage still exists after removal");
      }
      await new Promise((resolve) => setTimeout(resolve, 250));
      if (existsSync(tempRoot)) {
        rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
        throw new Error("owned temporary storage was recreated after removal by a surviving process");
      }
    })();
  }

  return cleanupPromise;
}

async function main() {
  const bdVersion = executeSuccessfully(bdBin, ["version"]).stdout.trim();
  const doltVersion = executeSuccessfully(doltBin, ["version"]).stdout.trim();
  const uname = executeSuccessfully("uname", ["-srvmo"]).stdout.trim();

  mkdirSync(doltClientRoot, { mode: 0o700 });
  mkdirSync(directStore, { mode: 0o700 });
  mkdirSync(beadsStore, { mode: 0o700 });
  mkdirSync(beadsProject, { mode: 0o700 });

  doltSuccessfully(
    ["init", "--name=Director TaskStore spike", "--email=spike@example.invalid"],
    { cwd: directStore, env: doltClientEnv },
  );
  doltSuccessfully(
    ["init", "--name=Director TaskStore spike", "--email=spike@example.invalid"],
    { cwd: beadsStore, env: doltClientEnv },
  );

  const port = await reservePort();
  serverPort = port;
  const authenticatedBootstrap = `
    CREATE TABLE harness_identity (
      singleton TINYINT PRIMARY KEY,
      run_id CHAR(16) NOT NULL UNIQUE,
      identity_proof CHAR(64) NOT NULL UNIQUE
    );
    INSERT INTO harness_identity (singleton, run_id, identity_proof)
      VALUES (1, '${runId}', '${identityProof}');
    CREATE USER '${ownerUser}'@'%' IDENTIFIED WITH mysql_native_password BY '${ownerPassword}';
    GRANT ALL ON *.* TO '${ownerUser}'@'%' WITH GRANT OPTION;
    CREATE USER '${appUser}'@'%' IDENTIFIED WITH mysql_native_password BY '${appPassword}';
    CREATE USER '${beadsUser}'@'%' IDENTIFIED WITH mysql_native_password BY '${beadsPassword}';
    GRANT ALL ON \`${beadsDatabase}\`.* TO '${beadsUser}'@'%';
    DROP USER IF EXISTS 'root'@'%';
    FLUSH PRIVILEGES;
    DROP USER IF EXISTS 'root'@'localhost';
  `;
  executeSecretBootstrap(authenticatedBootstrap);

  serverProcess = spawn(
    doltBin,
    [
      "sql-server",
      "--host=127.0.0.1",
      `--port=${port}`,
      `--data-dir=${tempRoot}`,
      `--doltcfg-dir=${doltConfig}`,
      "--loglevel=warning",
      "--skip-root-user-initialization",
    ],
    {
      cwd: tempRoot,
      env: { ...process.env, ...doltClientEnv },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  const spawned = new Promise((resolve, reject) => {
    serverProcess.once("spawn", resolve);
    serverProcess.once("error", reject);
  });
  serverProcess.stdout.setEncoding("utf8");
  serverProcess.stderr.setEncoding("utf8");
  serverProcess.stdout.on("data", (chunk) => {
    serverStdout += chunk;
  });
  serverProcess.stderr.on("data", (chunk) => {
    serverStderr += chunk;
  });
  serverProcess.on("error", (error) => {
    serverStderr += error.stack ?? String(error);
  });
  await spawned;
  assertObservation(
    Number.isSafeInteger(serverProcess.pid) && serverProcess.pid > 1 && isRunning(serverProcess),
    "owned Dolt server did not produce a live child PID",
  );
  await waitForOwnedServer();
  assertCredentialTransport("authenticated readiness");

  const unauthenticatedRoot = dolt(
    rootProbeDoltArgs("SELECT 1 AS unauthenticated_root_probe"),
  );
  assertObservation(
    unauthenticatedRoot.code !== 0 &&
      `${unauthenticatedRoot.stdout}${unauthenticatedRoot.stderr}`.includes("Access denied"),
    `unauthenticated root probe was not denied as expected: code=${unauthenticatedRoot.code} ${unauthenticatedRoot.stdout}${unauthenticatedRoot.stderr}`,
  );
  record(
    "harness.authenticated_server_identity",
    "Harness",
    "PASS",
    `Spawned PID ${serverProcess.pid} remained live while a unique database marker and HMAC identity matched through per-run environment-authenticated owner credentials; unauthenticated root was denied.`,
  );

  if (process.env.DIRECTOR_M04_INTERRUPT_READY === "1") {
    process.stderr.write(
      `DIRECTOR_M04_READY ${JSON.stringify({
        run_id: runId,
        server_pid: serverProcess.pid,
        server_port: port,
        temp_dir_basename: tempRootBasename,
      })}\n`,
    );
    await new Promise(() => {});
  }

  executeSuccessfully("git", ["init", "-q", "-b", "main"], { cwd: beadsProject });
  executeSuccessfully("git", ["config", "user.name", "Director TaskStore spike"], {
    cwd: beadsProject,
  });
  executeSuccessfully("git", ["config", "user.email", "spike@example.invalid"], {
    cwd: beadsProject,
  });
  bdSuccessfully(
    [
      "init",
      "--server",
      "--external",
      "--server-host=127.0.0.1",
      `--server-port=${port}`,
      `--server-user=${beadsUser}`,
      `--database=${beadsDatabase}`,
      `--prefix=${beadsPrefix}`,
      "--skip-agents",
      "--skip-hooks",
      "--non-interactive",
    ],
    { cwd: beadsProject },
  );
  assertCredentialTransport("Beads initialization");

  bdSuccessfully(
    [
      "create",
      `--id=${beadIds.epic}`,
      "--title=Example epic",
      "--description=contract fixture",
      "--type=epic",
      "--priority=1",
      '--metadata={"director":{"kind":"epic","version":0}}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  bdSuccessfully(
    [
      "create",
      `--id=${beadIds.task}`,
      "--title=Example task",
      "--description=contract fixture",
      "--type=task",
      "--priority=1",
      '--metadata={"director":{"kind":"task","version":0}}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  bdSuccessfully(
    ["dep", "add", beadIds.task, beadIds.epic, "--type=parent-child", "--json"],
    { cwd: beadsProject },
  );
  bdSuccessfully(
    [
      "create",
      `--id=${beadIds.blocker}`,
      "--title=Example blocker",
      "--description=contract fixture",
      "--type=task",
      "--json",
    ],
    { cwd: beadsProject },
  );
  bdSuccessfully(
    ["dep", "add", beadIds.task, beadIds.blocker, "--type=blocks", "--json"],
    { cwd: beadsProject },
  );
  const taskWithGraph = parseJson(
    bdSuccessfully(["show", beadIds.task, "--json"], { cwd: beadsProject }).stdout,
  )[0];
  const dependencyTypes = new Set(taskWithGraph.dependencies.map((dependency) => dependency.dependency_type));
  assertObservation(
    taskWithGraph.parent === beadIds.epic && dependencyTypes.has("blocks"),
    "Beads graph mapping did not round-trip",
  );
  record(
    "beads.planning_graph",
    "Beads",
    "PASS",
    "Epic, Task, parent-child, and blocking dependency records round-tripped through public CLI commands.",
  );

  const [beadsWriterA, beadsWriterB] = await Promise.all([
    executeConcurrently(
      bdBin,
      [
        "create",
        `--id=${beadIds["writer-a"]}`,
        "--title=Writer A",
        "--description=contract fixture",
        "--type=task",
        "--json",
      ],
      { cwd: beadsProject, env: { ...beadsClientEnv, BEADS_ACTOR: "writer-a" } },
    ),
    executeConcurrently(
      bdBin,
      [
        "create",
        `--id=${beadIds["writer-b"]}`,
        "--title=Writer B",
        "--description=contract fixture",
        "--type=task",
        "--json",
      ],
      { cwd: beadsProject, env: { ...beadsClientEnv, BEADS_ACTOR: "writer-b" } },
    ),
  ]);
  assertObservation(
    beadsWriterA.code === 0 && beadsWriterB.code === 0,
    `Beads concurrent writers failed: ${beadsWriterA.stderr} ${beadsWriterB.stderr}`,
  );
  record(
    "beads.concurrent_distinct_writers",
    "Beads",
    "PASS",
    "Two independent CLI processes created distinct records against one external server.",
  );

  const batchRollback = bd(["batch", "--json"], {
    cwd: beadsProject,
    input: `update ${beadIds.task} title="temporary"\nupdate ${beadsPrefix}-missing title="must fail"\n`,
  });
  const taskAfterBatch = parseJson(
    bdSuccessfully(["show", beadIds.task, "--json"], { cwd: beadsProject }).stdout,
  )[0];
  assertObservation(
    batchRollback.code !== 0 && taskAfterBatch.title === "Example task",
    "Beads batch did not roll back its first write",
  );
  record(
    "beads.narrow_batch_atomicity",
    "Beads",
    "PASS",
    "A failing supported batch update rolled back the preceding update.",
  );

  const expectedVersionProbe = bd(
    ["update", beadIds.task, "--expected-version=0", "--title=must-not-apply", "--json"],
    { cwd: beadsProject },
  );
  assertObservation(
    expectedVersionProbe.code !== 0 &&
      `${expectedVersionProbe.stdout}\n${expectedVersionProbe.stderr}`.includes(
        "unknown flag: --expected-version",
      ),
    "Beads unexpectedly accepted an expected-version precondition",
  );

  bdSuccessfully(
    [
      "update",
      beadIds.task,
      '--metadata={"director":{"kind":"task","version":1,"writer":"A"}}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  bdSuccessfully(
    [
      "update",
      beadIds.task,
      '--metadata={"director":{"kind":"task","version":1,"writer":"B"}}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  const taskAfterStaleWrites = parseJson(
    bdSuccessfully(["show", beadIds.task, "--json"], { cwd: beadsProject }).stdout,
  )[0];
  assertObservation(
    taskAfterStaleWrites.metadata.director.version === 1 &&
      taskAfterStaleWrites.metadata.director.writer === "B",
    "the stale Beads write behavior changed",
  );
  record(
    "beads.optimistic_concurrency",
    "Beads",
    "FAIL",
    "The public update command rejects --expected-version; two writes derived from version 0 both succeeded and the latter silently replaced the former at version 1.",
  );

  const structuredBatch = bd(["batch", "--json"], {
    cwd: beadsProject,
    input: `update ${beadIds.task} metadata="{}"\ncreate event 2 "state changed"\n`,
  });
  assertObservation(
    structuredBatch.code !== 0 &&
      `${structuredBatch.stdout}\n${structuredBatch.stderr}`.includes(
        'unsupported key "metadata"',
      ),
    "Beads batch unexpectedly accepted metadata",
  );
  record(
    "beads.atomic_state_and_structured_event",
    "Beads",
    "FAIL",
    "The only public multi-operation transaction rejects metadata (allowed update keys: status, priority, title, assignee), so it cannot atomically write a Director aggregate plus structured event/command records.",
  );

  bdSuccessfully(
    [
      "create",
      `--id=${beadIds.event}`,
      "--title=Task updated",
      "--description=immutable fixture",
      "--type=event",
      "--event-category=director.task.updated",
      "--event-actor=system:engine",
      `--event-target=${beadIds.task}`,
      '--event-payload={"version":1}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  bdSuccessfully(["update", beadIds.event, "--title=MUTATED", "--json"], {
    cwd: beadsProject,
  });
  const mutatedEvent = parseJson(
    bdSuccessfully(["show", beadIds.event, "--json"], { cwd: beadsProject }).stdout,
  )[0];
  assertObservation(mutatedEvent.title === "MUTATED", "Beads event mutation was unexpectedly rejected");
  record(
    "beads.immutable_event_records",
    "Beads",
    "FAIL",
    "A structured --type=event record was mutated successfully through the public update command.",
  );

  bdSuccessfully(
    [
      "create",
      `--id=${beadIds.command}`,
      "--title=Command cmd-1",
      "--description=contract fixture",
      "--type=task",
      '--metadata={"director":{"kind":"command","idempotencyKey":"cmd-1","payload":"original"}}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  const conflictingCommandReplay = bd(
    [
      "create",
      `--id=${beadIds.command}`,
      "--title=Conflicting command",
      "--description=changed fixture",
      "--type=task",
      '--metadata={"director":{"kind":"command","idempotencyKey":"cmd-1","payload":"different"}}',
      "--json",
    ],
    { cwd: beadsProject },
  );
  const commandAfterReplay = parseJson(
    bdSuccessfully(["show", beadIds.command, "--json"], { cwd: beadsProject }).stdout,
  )[0];
  assertObservation(
    conflictingCommandReplay.code === 0 &&
      commandAfterReplay.metadata.director.payload === "different",
    "Beads explicit-ID upsert behavior changed",
  );
  record(
    "beads.idempotency_key",
    "Beads",
    "FAIL",
    "Recreating an explicit issue ID with a conflicting command payload succeeded and overwrote the original record instead of returning the original result or rejecting the mismatch.",
  );
  assertCredentialTransport("Beads client phase");
  const directBaseSchema = `
    CREATE TABLE aggregates (
      id VARCHAR(64) PRIMARY KEY,
      kind VARCHAR(16) NOT NULL,
      parent_id VARCHAR(64),
      workspace_id VARCHAR(64),
      version BIGINT UNSIGNED NOT NULL,
      data JSON NOT NULL,
      updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
      CONSTRAINT fk_aggregate_parent FOREIGN KEY (parent_id) REFERENCES aggregates(id),
      CONSTRAINT fk_aggregate_workspace FOREIGN KEY (workspace_id) REFERENCES aggregates(id)
    );
    CREATE TABLE dependencies (
      source_id VARCHAR(64) NOT NULL,
      target_id VARCHAR(64) NOT NULL,
      dependency_type VARCHAR(32) NOT NULL,
      PRIMARY KEY (source_id, target_id, dependency_type),
      CONSTRAINT fk_dependency_source FOREIGN KEY (source_id) REFERENCES aggregates(id),
      CONSTRAINT fk_dependency_target FOREIGN KEY (target_id) REFERENCES aggregates(id)
    );
    CREATE TABLE candidates (
      id VARCHAR(64) PRIMARY KEY,
      run_id VARCHAR(64) NOT NULL,
      sequence BIGINT UNSIGNED NOT NULL,
      commit_sha CHAR(40) NOT NULL,
      data JSON NOT NULL,
      UNIQUE KEY uq_candidate_sequence (run_id, sequence),
      CONSTRAINT fk_candidate_run FOREIGN KEY (run_id) REFERENCES aggregates(id)
    );
    CREATE TABLE events (
      global_sequence BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
      event_id VARCHAR(128) NOT NULL UNIQUE,
      run_id VARCHAR(64) NOT NULL,
      sequence BIGINT UNSIGNED NOT NULL,
      aggregate_id VARCHAR(64) NOT NULL,
      aggregate_version BIGINT UNSIGNED NOT NULL,
      event_type VARCHAR(64) NOT NULL,
      payload JSON NOT NULL,
      created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
      UNIQUE KEY uq_event_run_sequence (run_id, sequence),
      CONSTRAINT fk_event_run FOREIGN KEY (run_id) REFERENCES aggregates(id),
      CONSTRAINT fk_event_aggregate FOREIGN KEY (aggregate_id) REFERENCES aggregates(id)
    );
    CREATE TABLE audit_entries (
      sequence BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
      audit_id VARCHAR(128) NOT NULL UNIQUE,
      event_id VARCHAR(128) NOT NULL,
      actor_type VARCHAR(16) NOT NULL,
      payload JSON NOT NULL,
      created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
      CONSTRAINT fk_audit_event FOREIGN KEY (event_id) REFERENCES events(event_id)
    );
    CREATE TABLE command_requests (
      idempotency_key VARCHAR(128) PRIMARY KEY,
      aggregate_id VARCHAR(64) NOT NULL,
      expected_version BIGINT UNSIGNED NOT NULL,
      request_hash CHAR(64) NOT NULL,
      payload JSON NOT NULL,
      created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
      CONSTRAINT fk_command_request_aggregate
        FOREIGN KEY (aggregate_id) REFERENCES aggregates(id)
    );
    CREATE TABLE command_outcomes (
      idempotency_key VARCHAR(128) PRIMARY KEY,
      outcome_type VARCHAR(16) NOT NULL,
      observed_version BIGINT UNSIGNED NOT NULL,
      event_id VARCHAR(128),
      payload JSON NOT NULL,
      completed_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
      CONSTRAINT fk_command_outcome_request
        FOREIGN KEY (idempotency_key) REFERENCES command_requests(idempotency_key),
      CONSTRAINT fk_command_outcome_event
        FOREIGN KEY (event_id) REFERENCES events(event_id)
    );
    CREATE TABLE guard_constants (
      singleton TINYINT PRIMARY KEY
    );
    INSERT INTO guard_constants (singleton) VALUES (1);
    CREATE TABLE parent_guard (
      identity VARBINARY(64) NOT NULL PRIMARY KEY
    );
    INSERT INTO parent_guard (identity) VALUES ('${parentMissingSentinel}');
    CREATE TABLE immutable_write_guard (
      singleton TINYINT PRIMARY KEY
    );
    INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER candidates_reject_update
      BEFORE UPDATE ON candidates FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER candidates_reject_delete
      BEFORE DELETE ON candidates FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER events_reject_update
      BEFORE UPDATE ON events FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER events_reject_delete
      BEFORE DELETE ON events FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER audit_entries_reject_update
      BEFORE UPDATE ON audit_entries FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER audit_entries_reject_delete
      BEFORE DELETE ON audit_entries FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER command_requests_reject_update
      BEFORE UPDATE ON command_requests FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER command_requests_reject_delete
      BEFORE DELETE ON command_requests FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER command_outcomes_reject_update
      BEFORE UPDATE ON command_outcomes FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER command_outcomes_reject_delete
      BEFORE DELETE ON command_outcomes FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE TABLE keyless_append_probe (
      id VARCHAR(64) NOT NULL,
      value VARCHAR(64) NOT NULL
    );
    CREATE TABLE unique_append_probe (
      id VARCHAR(64) NOT NULL,
      value VARCHAR(64) NOT NULL,
      UNIQUE KEY uq_unique_append_probe (id)
    );
    CREATE TABLE trigger_control_probe (
      id INT PRIMARY KEY,
      value VARCHAR(64) NOT NULL
    );
    INSERT INTO trigger_control_probe (id, value) VALUES (1, 'original');
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER trigger_control_probe_reject_update
      BEFORE UPDATE ON trigger_control_probe FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton) VALUES (1);
    CREATE TABLE definer_append_probe (
      id INT PRIMARY KEY
    );
    CREATE DEFINER = '${ownerUser}'@'%' PROCEDURE append_definer_probe(IN p_id INT)
      SQL SECURITY DEFINER INSERT INTO definer_append_probe (id) VALUES (p_id);
    CREATE TABLE view_append_probe (
      id INT PRIMARY KEY,
      value VARCHAR(64) NOT NULL
    );
    CREATE SQL SECURITY DEFINER VIEW view_append_api AS
      SELECT id, value FROM view_append_probe;
    CREATE TABLE schema_drift_probe (
      id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
      drift_key VARCHAR(64) COLLATE utf8mb4_0900_ai_ci NOT NULL,
      value VARCHAR(64) NOT NULL,
      UNIQUE KEY uq_schema_drift_probe (drift_key)
    );
  `;
  doltSuccessfully(ownerDoltArgs(directBaseSchema));

  const columnFacts = parseJson(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT table_name AS fact_table, column_name AS fact_column, data_type AS fact_type,
          character_octet_length AS fact_octets, numeric_precision AS fact_precision,
          collation_name AS fact_collation, is_nullable AS fact_nullable, extra AS fact_extra
        FROM information_schema.columns
        WHERE table_schema = DATABASE()
      `),
    ).stdout,
  ).rows;
  const factFor = (table, column) => {
    const fact = columnFacts.find(
      (row) => row.fact_table === table && row.fact_column === column,
    );
    assertObservation(fact, `no column metadata for ${table}.${column}`);
    return fact;
  };
  const columnByteWidth = (fact) => {
    if (fact.fact_octets !== undefined && fact.fact_octets !== null && fact.fact_octets !== "") {
      return Number(fact.fact_octets);
    }
    assertObservation(
      fact.fact_precision !== undefined && fact.fact_precision !== null,
      `column ${fact.fact_table}.${fact.fact_column} has neither octet length nor numeric precision`,
    );
    return Number(fact.fact_precision) + 1;
  };
  const identityByteBudget = Math.max(
    ...identityLedgerModel.flatMap((spec) =>
      spec.identities.map(
        (identity) =>
          Buffer.byteLength(`${spec.table}.${identity.name}`, "utf8") +
          identity.columns.reduce(
            (total, column) => total + 1 + columnByteWidth(factFor(spec.table, column)),
            0,
          ),
      ),
    ),
  );
  const ledgerIdentityWidth = Math.ceil((identityByteBudget + 64) / 64) * 64;
  assertObservation(
    Number.isSafeInteger(identityByteBudget) &&
      identityByteBudget > 0 &&
      ledgerIdentityWidth > identityByteBudget,
    `derived identity width is invalid: budget=${identityByteBudget} width=${ledgerIdentityWidth}`,
  );

  const foreignKeyModel = [
    { constraint: "fk_aggregate_parent", table: "aggregates", column: "parent_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_aggregate_workspace", table: "aggregates", column: "workspace_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_dependency_source", table: "dependencies", column: "source_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_dependency_target", table: "dependencies", column: "target_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_candidate_run", table: "candidates", column: "run_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_event_run", table: "events", column: "run_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_event_aggregate", table: "events", column: "aggregate_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_audit_event", table: "audit_entries", column: "event_id", parent: "events", parentColumn: "event_id" },
    { constraint: "fk_command_request_aggregate", table: "command_requests", column: "aggregate_id", parent: "aggregates", parentColumn: "id" },
    { constraint: "fk_command_outcome_request", table: "command_outcomes", column: "idempotency_key", parent: "command_requests", parentColumn: "idempotency_key" },
    { constraint: "fk_command_outcome_event", table: "command_outcomes", column: "event_id", parent: "events", parentColumn: "event_id" },
  ];
  const guardedTables = [
    "aggregates",
    "dependencies",
    "candidates",
    "events",
    "audit_entries",
    "command_requests",
    "command_outcomes",
  ];
  const declaredForeignKeys = [];
  for (const table of guardedTables) {
    const definition = firstRow(
      doltSuccessfully(ownerDoltArgs(`SHOW CREATE TABLE \`${table}\``)),
    );
    const ddl = definition["Create Table"] ?? definition.create_table ?? Object.values(definition)[1];
    assertObservation(typeof ddl === "string", `could not read the live definition of ${table}`);
    for (const match of ddl.matchAll(
      /CONSTRAINT\s+`?([\w]+)`?\s+FOREIGN KEY\s+\(`?([\w]+)`?\)\s+REFERENCES\s+`?([\w]+)`?\s*\(`?([\w]+)`?\)/gi,
    )) {
      declaredForeignKeys.push({
        constraint: match[1],
        table,
        column: match[2],
        parent: match[3],
        parentColumn: match[4],
      });
    }
  }
  const foreignKeyKey = (entry) =>
    `${entry.table}.${entry.column}->${entry.parent}.${entry.parentColumn}`;
  const declaredForeignKeySet = new Set(declaredForeignKeys.map(foreignKeyKey));
  const guardedForeignKeySet = new Set(foreignKeyModel.map(foreignKeyKey));
  const unguardedForeignKeys = [...declaredForeignKeySet].filter(
    (key) => !guardedForeignKeySet.has(key),
  );
  assertObservation(
    declaredForeignKeys.length > 0 &&
      unguardedForeignKeys.length === 0 &&
      declaredForeignKeySet.size === guardedForeignKeySet.size,
    `parent guards do not cover every declared foreign key: declared=${[
      ...declaredForeignKeySet,
    ].join(", ")} guarded=${[...guardedForeignKeySet].join(", ")}`,
  );
  const referencedParentIdentities = [
    ...new Set(foreignKeyModel.map((entry) => `${entry.parent}.${entry.parentColumn}`)),
  ].sort();
  const guardedReferencedParentIdentities = [
    ...new Set(
      identityLedgerModel.flatMap((spec) =>
        spec.identities
          .filter((identity) => identity.columns.length === 1)
          .map((identity) => `${spec.table}.${identity.columns[0]}`),
      ),
    ),
  ]
    .filter((identity) => referencedParentIdentities.includes(identity))
    .sort();
  assertObservation(
    JSON.stringify(guardedReferencedParentIdentities) ===
      JSON.stringify(referencedParentIdentities),
    `identity ledgers do not protect every referenced parent key: referenced=${referencedParentIdentities.join(
      ", ",
    )} guarded=${guardedReferencedParentIdentities.join(", ")}`,
  );

  const parentGuardBody = (entry) =>
    `INSERT INTO parent_guard (identity) SELECT '${parentMissingSentinel}' FROM guard_constants
       LEFT JOIN \`${entry.parent}\` AS guarded_parent
         ON guarded_parent.\`${entry.parentColumn}\` = NEW.\`${entry.column}\`
       WHERE NEW.\`${entry.column}\` IS NOT NULL AND guarded_parent.\`${entry.parentColumn}\` IS NULL`;
  const ledgerSchema = `
    ${identityLedgerModel
      .map((spec) => appendOnlyLedgerDdl(spec, ledgerIdentityWidth))
      .join("\n")}
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER aggregates_reject_identity_update
      BEFORE UPDATE ON aggregates FOR EACH ROW
      INSERT INTO immutable_write_guard (singleton)
        SELECT singleton FROM guard_constants WHERE NOT (NEW.id <=> OLD.id);
    CREATE TABLE schema_drift_probe_identity (
      identity VARBINARY(${ledgerIdentityWidth}) NOT NULL PRIMARY KEY
    );
    CREATE DEFINER = '${ownerUser}'@'%' TRIGGER schema_drift_probe_append_only
      BEFORE INSERT ON schema_drift_probe FOR EACH ROW
      INSERT INTO schema_drift_probe_identity (identity) VALUES
        (CONCAT('schema_drift_probe.pk', CHAR(31), NEW.id)),
        (CONCAT('schema_drift_probe.drift_key', CHAR(31), NEW.drift_key));
    ${foreignKeyModel
      .map(
        (entry) => `CREATE DEFINER = '${ownerUser}'@'%' TRIGGER ${entry.constraint}_present
      BEFORE INSERT ON \`${entry.table}\` FOR EACH ROW
      ${parentGuardBody(entry)};`,
      )
      .join("\n    ")}
    ${foreignKeyModel
      .filter((entry) => entry.table === "aggregates")
      .map(
        (entry) => `CREATE DEFINER = '${ownerUser}'@'%' TRIGGER ${entry.constraint}_present_on_update
      BEFORE UPDATE ON \`${entry.table}\` FOR EACH ROW
      ${parentGuardBody(entry)};`,
      )
      .join("\n    ")}
  `;
  doltSuccessfully(ownerDoltArgs(ledgerSchema));

  const grants = `
    GRANT SELECT, INSERT, UPDATE ON \`${directDatabase}\`.aggregates TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.dependencies TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.candidates TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.events TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.audit_entries TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.command_requests TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.command_outcomes TO '${appUser}'@'%';
    ${identityLedgerModel
      .map(
        (spec) =>
          `GRANT SELECT ON \`${directDatabase}\`.${spec.table}_identity TO '${appUser}'@'%';`,
      )
      .join("\n    ")}
    GRANT SELECT ON \`${directDatabase}\`.parent_guard TO '${appUser}'@'%';
    GRANT SELECT ON \`${directDatabase}\`.guard_constants TO '${appUser}'@'%';
    GRANT SELECT ON \`${directDatabase}\`.immutable_write_guard TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.schema_drift_probe TO '${appUser}'@'%';
    GRANT SELECT ON \`${directDatabase}\`.schema_drift_probe_identity TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.keyless_append_probe TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.unique_append_probe TO '${appUser}'@'%';
    GRANT SELECT ON \`${directDatabase}\`.definer_append_probe TO '${appUser}'@'%';
    GRANT EXECUTE ON PROCEDURE \`${directDatabase}\`.append_definer_probe TO '${appUser}'@'%';
    GRANT SELECT, INSERT ON \`${directDatabase}\`.view_append_api TO '${appUser}'@'%';
    FLUSH PRIVILEGES;
  `;
  doltSuccessfully(ownerDoltArgs(grants));
  const signalTrigger = dolt(
    ownerDoltArgs(
      `CREATE DEFINER = '${ownerUser}'@'%' TRIGGER events_signal_probe BEFORE INSERT ON events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'append only'`,
    ),
  );
  const compoundTrigger = dolt(
    ownerDoltArgs(
      `CREATE DEFINER = '${ownerUser}'@'%' TRIGGER events_compound_probe BEFORE INSERT ON events FOR EACH ROW BEGIN IF EXISTS (SELECT 1 FROM events WHERE event_id = NEW.event_id) THEN SET @rejected = 1; END IF; END`,
    ),
  );
  assertObservation(
    signalTrigger.code !== 0 && compoundTrigger.code !== 0,
    "Dolt 2.3.2 unexpectedly accepted a SIGNAL or compound trigger body, so the guard form should be revisited",
  );
  record(
    "direct_dolt.guard_body_constraints",
    "Direct Dolt",
    "CONTROL",
    `Dolt 2.3.2 rejects SIGNAL (exit ${signalTrigger.code}) and compound BEGIN ... END trigger bodies (exit ${compoundTrigger.code}), so each append-only guard is expressed as one supported multi-row INSERT into its identity ledger whose primary key raises the duplicate-identity error.`,
  );

  const definerAppendProbe = dolt(appDoltArgs("CALL append_definer_probe(1)"));
  assertObservation(
    definerAppendProbe.code !== 0 &&
      `${definerAppendProbe.stdout}${definerAppendProbe.stderr}`.includes("command denied"),
    "Dolt unexpectedly enabled the explicit-definer append boundary",
  );
  record(
    "direct_dolt.definer_append_boundary",
    "Direct Dolt",
    "CONTROL",
    "Rejected alternative: a procedure recorded with the authenticated owner as explicit DEFINER and granted EXECUTE still ran its INSERT with app privileges and was denied, so SELECT+EXECUTE cannot replace the base-table grant.",
  );
  const viewAppend = dolt(appDoltArgs("INSERT INTO view_append_api (id, value) VALUES (1, 'original')"));
  const viewOdku = dolt(
    appDoltArgs(
      "INSERT INTO view_append_api (id, value) VALUES (1, 'tampered') ON DUPLICATE KEY UPDATE value = 'tampered'",
    ),
  );
  const viewRows = parseJson(
    doltSuccessfully(ownerDoltArgs("SELECT value FROM view_append_probe WHERE id = 1")).stdout,
  ).rows;
  const viewValue = viewRows?.[0]?.value ?? null;
  record(
    "direct_dolt.view_append_boundary",
    "Direct Dolt",
    viewAppend.code === 0 && viewOdku.code !== 0 && viewValue === "original" ? "PASS" : "CONTROL",
    `Insert exit=${viewAppend.code}; ODKU exit=${viewOdku.code}; stored value=${viewValue ?? "absent"}; insert diagnostic=${(viewAppend.stderr || viewAppend.stdout).trim()}; ODKU diagnostic=${(viewOdku.stderr || viewOdku.stdout).trim()}.`,
  );
  assertCredentialTransport("direct schema and grant phase");

  const seedMapping = `
    START TRANSACTION;
    INSERT INTO aggregates (id, kind, version, data)
      VALUES ('project-1', 'project', 0, JSON_OBJECT('name', 'Example'));
    INSERT INTO aggregates (id, kind, parent_id, version, data)
      VALUES ('workspace-1', 'workspace', 'project-1', 0, JSON_OBJECT('name', 'Repository'));
    INSERT INTO aggregates (id, kind, parent_id, version, data)
      VALUES ('epic-1', 'epic', 'project-1', 0, JSON_OBJECT('title', 'Epic'));
    INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data)
      VALUES
        ('task-1', 'task', 'epic-1', 'workspace-1', 0, JSON_OBJECT('title', 'Example')),
        ('task-blocker', 'task', 'epic-1', 'workspace-1', 0, JSON_OBJECT('title', 'Blocker'));
    INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data)
      VALUES ('run-1', 'run', 'task-1', 'workspace-1', 0, JSON_OBJECT('runNumber', 1));
    INSERT INTO dependencies (source_id, target_id, dependency_type)
      VALUES ('task-1', 'task-blocker', 'blocks');
    INSERT INTO candidates (id, run_id, sequence, commit_sha, data)
      VALUES ('candidate-1', 'run-1', 1, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
        JSON_OBJECT('current', true));
    INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload)
      VALUES ('event-0', 'run-1', 1, 'task-1', 0, 'task.created', JSON_OBJECT('title', 'Example'));
    INSERT INTO audit_entries (audit_id, event_id, actor_type, payload)
      VALUES ('audit-0', 'event-0', 'system', JSON_OBJECT('source', 'contract'));
    INSERT INTO command_requests
      (idempotency_key, aggregate_id, expected_version, request_hash, payload)
      VALUES ('mapping-command-1', 'task-1', 0, REPEAT('b', 64),
        JSON_OBJECT('title', 'Mapping command'));
    INSERT INTO command_outcomes
      (idempotency_key, outcome_type, observed_version, event_id, payload)
      VALUES ('mapping-command-1', 'applied', 0, 'event-0',
        JSON_OBJECT('title', 'Mapping command'));
    INSERT INTO aggregates (id, kind, version, data)
      VALUES ('referenced-aggregate-1', 'project', 0,
        JSON_OBJECT('title', 'Referenced identity fixture'));
    INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data)
      VALUES ('referenced-aggregate-child-1', 'task', 'referenced-aggregate-1',
        'referenced-aggregate-1', 0, JSON_OBJECT('title', 'Referencing aggregate'));
    INSERT INTO dependencies (source_id, target_id, dependency_type)
      VALUES ('referenced-aggregate-1', 'referenced-aggregate-1', 'identity_probe');
    INSERT INTO candidates (id, run_id, sequence, commit_sha, data)
      VALUES ('referenced-candidate-1', 'referenced-aggregate-1', 498,
        REPEAT('c', 40), JSON_OBJECT('current', false));
    INSERT INTO events
      (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload)
      VALUES ('referenced-event-1', 'referenced-aggregate-1', 498,
        'referenced-aggregate-1', 0, 'identity.fixture', JSON_OBJECT('title', 'Referenced event'));
    INSERT INTO audit_entries (audit_id, event_id, actor_type, payload)
      VALUES ('referenced-audit-1', 'referenced-event-1', 'system',
        JSON_OBJECT('source', 'identity fixture'));
    INSERT INTO command_requests
      (idempotency_key, aggregate_id, expected_version, request_hash, payload)
      VALUES ('referenced-command-1', 'referenced-aggregate-1', 0, REPEAT('d', 64),
        JSON_OBJECT('title', 'Referenced command'));
    INSERT INTO command_outcomes
      (idempotency_key, outcome_type, observed_version, event_id, payload)
      VALUES ('referenced-command-1', 'applied', 0, 'referenced-event-1',
        JSON_OBJECT('title', 'Referenced command'));
    COMMIT;
  `;
  doltSuccessfully(appDoltArgs(seedMapping));
  assertCredentialTransport("direct mapping append phase");
  const mappedKinds = firstRow(
    doltSuccessfully(
      appDoltArgs(
        "SELECT COUNT(DISTINCT kind) AS kinds FROM aggregates WHERE id IN ('project-1','workspace-1','epic-1','task-1','run-1')",
      ),
    ),
  );
  const mappedCandidates = firstRow(
    doltSuccessfully(
      appDoltArgs("SELECT COUNT(*) AS candidates FROM candidates WHERE run_id = 'run-1'"),
    ),
  );
  assertObservation(
    mappedKinds.kinds === "5" && mappedCandidates.candidates === "1",
    "direct Dolt record mapping did not round-trip",
  );
  record(
    "direct_dolt.record_mapping",
    "Direct Dolt",
    "PASS",
    "Explicit relational records round-tripped Project, Workspace, Epic, Task, dependency, Run, Candidate, Event, and Audit shapes; Command request/outcome tables were independently exercised below.",
  );

  const parentMutationModes = [
    { mode: "foreign_keys_enforced", prefix: "" },
    { mode: "foreign_keys_disabled", prefix: "SET SESSION foreign_key_checks=0; " },
    {
      mode: "foreign_keys_disabled_relaxed_sql_mode",
      prefix: "SET SESSION sql_mode=''; SET SESSION foreign_key_checks=0; ",
    },
  ];
  const referencedParentProbes = [
    {
      table: "aggregates",
      column: "id",
      original: "referenced-aggregate-1",
      insertDerived: (renamed) =>
        `INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data)
          VALUES ('referenced-aggregate-1', 'project', NULL, NULL, 0, JSON_OBJECT('title', 'Identity attack'))
          ON DUPLICATE KEY UPDATE id = ${sqlString(renamed)}`,
    },
    {
      table: "events",
      column: "event_id",
      original: "referenced-event-1",
      insertDerived: (renamed) =>
        `INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload)
          VALUES ('referenced-event-1', 'referenced-aggregate-1', 499, 'referenced-aggregate-1', 0, 'task.identity_attack', JSON_OBJECT('title', 'Identity attack'))
          ON DUPLICATE KEY UPDATE event_id = ${sqlString(renamed)}`,
    },
    {
      table: "command_requests",
      column: "idempotency_key",
      original: "referenced-command-1",
      insertDerived: (renamed) =>
        `INSERT INTO command_requests
          (idempotency_key, aggregate_id, expected_version, request_hash, payload)
          VALUES ('referenced-command-1', 'referenced-aggregate-1', 0, REPEAT('e', 64), JSON_OBJECT('title', 'Identity attack'))
          ON DUPLICATE KEY UPDATE idempotency_key = ${sqlString(renamed)}`,
    },
  ];
  assertObservation(
    JSON.stringify(
      referencedParentProbes.map((probe) => `${probe.table}.${probe.column}`).sort(),
    ) === JSON.stringify(referencedParentIdentities),
    "focused parent-key probes do not cover every referenced identity",
  );
  const parentReferenceState = (probe, renamed) => {
    const references = foreignKeyModel
      .filter(
        (entry) => entry.parent === probe.table && entry.parentColumn === probe.column,
      )
      .map((entry) => {
        const counts = firstRow(
          doltSuccessfully(
            ownerDoltArgs(`
              SELECT COUNT(*) AS child_rows,
                SUM(guarded_parent.\`${entry.parentColumn}\` IS NOT NULL) AS linked_rows
              FROM \`${entry.table}\` AS guarded_child
              LEFT JOIN \`${entry.parent}\` AS guarded_parent
                ON guarded_parent.\`${entry.parentColumn}\` = guarded_child.\`${entry.column}\`
              WHERE guarded_child.\`${entry.column}\` = ${sqlString(probe.original)}
            `),
          ),
        );
        return {
          foreign_key: foreignKeyKey(entry),
          child_rows: Number(counts.child_rows),
          linked_rows: Number(counts.linked_rows),
        };
      });
    return {
      original_parent_rows: Number(
        firstRow(
          doltSuccessfully(
            ownerDoltArgs(
              `SELECT COUNT(*) AS rows_present FROM \`${probe.table}\` WHERE \`${probe.column}\` = ${sqlString(
                probe.original,
              )}`,
            ),
          ),
        ).rows_present,
      ),
      renamed_parent_rows: Number(
        firstRow(
          doltSuccessfully(
            ownerDoltArgs(
              `SELECT COUNT(*) AS rows_present FROM \`${probe.table}\` WHERE \`${probe.column}\` = ${sqlString(
                renamed,
              )}`,
            ),
          ),
        ).rows_present,
      ),
      references,
    };
  };
  const parentIdentityMutationResults = [];
  for (const probe of referencedParentProbes) {
    for (const { mode, prefix } of parentMutationModes) {
      for (const [statement, sql] of [
        [
          "direct_update",
          `UPDATE \`${probe.table}\` SET \`${probe.column}\` = ${sqlString(
            `${probe.table}-${mode}-direct`,
          )} WHERE \`${probe.column}\` = ${sqlString(probe.original)}`,
        ],
        [
          "insert_derived_update",
          probe.insertDerived(`${probe.table}-${mode}-insert`),
        ],
      ]) {
        const renamed = `${probe.table}-${mode}-${
          statement === "direct_update" ? "direct" : "insert"
        }`;
        const before = parentReferenceState(probe, renamed);
        assertObservation(
          before.original_parent_rows === 1 &&
            before.renamed_parent_rows === 0 &&
            before.references.length > 0 &&
            before.references.every(
              (reference) =>
                reference.child_rows > 0 &&
                reference.child_rows === reference.linked_rows,
            ),
          `parent-key fixture was not fully linked before ${probe.table}.${probe.column} ${mode} ${statement}: ${JSON.stringify(
            before,
          )}`,
        );
        const result = dolt(appDoltArgs(`${prefix}${sql};`), {
          tolerateTimeout: true,
          timeout: adversarialTimeoutMs,
        });
        const after = parentReferenceState(probe, renamed);
        assertObservation(
          result.code !== 0 && JSON.stringify(after) === JSON.stringify(before),
          `referenced parent identity changed through ${probe.table}.${probe.column} ${mode} ${statement}: exit=${
            result.code
          } before=${JSON.stringify(before)} after=${JSON.stringify(after)}`,
        );
        parentIdentityMutationResults.push({
          parent_identity: `${probe.table}.${probe.column}`,
          session_mode: mode,
          statement,
          exit_code: result.code,
          parent_identity_preserved: true,
          every_child_remained_linked: true,
        });
      }
    }
  }
  const mutableAggregateId = `mutable-aggregate-${runId}`;
  doltSuccessfully(
    appDoltArgs(
      `INSERT INTO aggregates (id, kind, version, data) VALUES (${sqlString(
        mutableAggregateId,
      )}, 'project', 0, JSON_OBJECT('mode', 'seed'))`,
    ),
  );
  const legitimateAggregateUpdateResults = [];
  for (const [index, { mode, prefix }] of parentMutationModes.entries()) {
    const result = dolt(
      appDoltArgs(
        `${prefix}UPDATE aggregates SET id = id, version = version + 1, data = JSON_OBJECT('mode', ${sqlString(
          mode,
        )}) WHERE id = ${sqlString(mutableAggregateId)};`,
      ),
    );
    const state = firstRow(
      doltSuccessfully(
        ownerDoltArgs(
          `SELECT id, version, JSON_UNQUOTE(JSON_EXTRACT(data, '$.mode')) AS mode FROM aggregates WHERE id = ${sqlString(
            mutableAggregateId,
          )}`,
        ),
      ),
    );
    assertObservation(
      result.code === 0 &&
        state.id === mutableAggregateId &&
        state.version === String(index + 1) &&
        state.mode === mode,
      `legitimate aggregate update failed with ${mode}: exit=${result.code} state=${JSON.stringify(
        state,
      )}`,
    );
    legitimateAggregateUpdateResults.push({
      session_mode: mode,
      exit_code: result.code,
      identity_preserved: true,
      version_after: Number(state.version),
    });
  }
  const referencedParentIdentityEvidence = {
    referenced_parent_identities: referencedParentIdentities,
    guarded_parent_identities: guardedReferencedParentIdentities,
    mutation_probes: parentIdentityMutationResults,
    legitimate_aggregate_updates: legitimateAggregateUpdateResults,
  };
  record(
    "direct_dolt.referenced_parent_identity_immutability",
    "Direct Dolt",
    "PASS",
    `Identity ledgers and a conditional aggregate UPDATE guard protected all ${referencedParentIdentities.length} referenced parent keys across ${parentIdentityMutationResults.length} direct and insert-derived rename attempts with foreign-key checks enforced, disabled, and disabled under relaxed sql_mode; every child remained linked and ${legitimateAggregateUpdateResults.length} version/data updates preserved the aggregate id.`,
  );

  if (focus === "referenced-parent-identities") {
    assertCredentialTransport("focused referenced-parent identity phase");
    await cleanup();
    assertObservation(
      !existsSync(tempRoot) &&
        serverTermination &&
        (serverTermination.exit_code !== null || serverTermination.signal !== null),
      "focused fixture did not prove server termination before storage removal",
    );
    process.stdout.write(
      `${JSON.stringify(
        {
          schema_version: 1,
          focus,
          environment: {
            run_id: runId,
            bd: bdVersion,
            dolt: doltVersion,
            operating_system: uname,
            server_terminated_before_storage_removal: true,
            owned_temp_directory_removed: true,
          },
          check: checks.find(
            (entry) => entry.id === "direct_dolt.referenced_parent_identity_immutability",
          ),
          referenced_parent_identity_evidence: referencedParentIdentityEvidence,
        },
        null,
        2,
      )}\n`,
    );
    return;
  }

  const failedAtomicWrite = dolt(
    appDoltArgs(
      `
        START TRANSACTION;
        UPDATE aggregates
          SET version = 1, data = JSON_OBJECT('title', 'must rollback')
          WHERE id = 'task-1' AND version = 0;
        INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload)
          VALUES ('event-0', 'run-1', 2, 'task-1', 1, 'task.updated', JSON_OBJECT('title', 'must rollback'));
        COMMIT;
      `,
    ),
  );
  const stateAfterRollback = firstRow(
    doltSuccessfully(
      appDoltArgs(
        "SELECT version, JSON_UNQUOTE(JSON_EXTRACT(data, '$.title')) AS title FROM aggregates WHERE id = 'task-1'",
      ),
    ),
  );
  const eventsAfterRollback = firstRow(
    doltSuccessfully(
      appDoltArgs("SELECT COUNT(*) AS event_count FROM events WHERE aggregate_id = 'task-1'"),
    ),
  );
  assertObservation(
    failedAtomicWrite.code !== 0 &&
      stateAfterRollback.version === "0" &&
      stateAfterRollback.title === "Example" &&
      eventsAfterRollback.event_count === "1",
    "direct Dolt did not roll back aggregate state after event failure",
  );
  record(
    "direct_dolt.atomic_state_and_event",
    "Direct Dolt",
    "PASS",
    "The append-only identity guard rejected a duplicate Event identity and aborted the transaction; aggregate version/data and Event count remained unchanged.",
  );

  doltSuccessfully(
    appDoltArgs(
      "INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data) VALUES ('task-contention', 'task', 'epic-1', 'workspace-1', 0, JSON_OBJECT('title', 'Contention'))",
    ),
  );

  const commandA = commandFixture("a", "Writer A");
  const commandB = commandFixture("b", "Writer B");
  const sessionA = openAppSqlSession();
  const sessionB = openAppSqlSession();
  const barrierA = `READY_${runId}_A`;
  const barrierB = `READY_${runId}_B`;
  sessionA.child.stdin.write(
    `START TRANSACTION;\nSELECT version FROM aggregates WHERE id = 'task-contention';\nSELECT '${barrierA}' AS barrier;\n`,
  );
  sessionB.child.stdin.write(
    `START TRANSACTION;\nSELECT version FROM aggregates WHERE id = 'task-contention';\nSELECT '${barrierB}' AS barrier;\n`,
  );
  await Promise.all([
    waitForSessionText(sessionA, barrierA),
    waitForSessionText(sessionB, barrierB),
  ]);
  assertObservation(
    sessionA.stdout.includes('"version":"0"') && sessionB.stdout.includes('"version":"0"'),
    "both contention clients did not observe expected version 0 at the barrier",
  );
  sessionA.child.stdin.end(contendingTransaction(commandA));
  sessionB.child.stdin.end(contendingTransaction(commandB));
  const contentionResults = await Promise.all([sessionA.closed, sessionB.closed]);
  assertCredentialTransport("barriered contention client phase");
  const successfulIndexes = contentionResults
    .map((result, index) => ({ result, index }))
    .filter(({ result }) => result.code === 0);
  const failedIndexes = contentionResults
    .map((result, index) => ({ result, index }))
    .filter(({ result }) => result.code !== 0);
  assertObservation(
    successfulIndexes.length === 1 && failedIndexes.length === 1,
    `expected one contention winner and loser: ${JSON.stringify(contentionResults)}`,
  );
  const winnerIndex = successfulIndexes[0].index;
  const loserIndex = failedIndexes[0].index;
  const winnerCommand = [commandA, commandB][winnerIndex];
  const loserCommand = [commandA, commandB][loserIndex];
  const winnerResult = successfulIndexes[0].result;
  const loserResult = failedIndexes[0].result;
  const serializationFailure = `${loserResult.stdout}\n${loserResult.stderr}`;
  assertObservation(
    winnerResult.stdout.includes('"matched":"1"') &&
      /Error 1213/.test(serializationFailure) &&
      /(40001|serialization failure)/i.test(serializationFailure),
    `unexpected contention outcome: ${serializationFailure}`,
  );

  const stateAfterContention = firstRow(
    doltSuccessfully(
      appDoltArgs(
        "SELECT version, JSON_UNQUOTE(JSON_EXTRACT(data, '$.title')) AS title FROM aggregates WHERE id = 'task-contention'",
      ),
    ),
  );
  const preRecoveryCounts = firstRow(
    doltSuccessfully(
      appDoltArgs(`
        SELECT
          (SELECT COUNT(*) FROM command_requests WHERE aggregate_id = 'task-contention') AS requests,
          (SELECT COUNT(*) FROM command_outcomes o
            JOIN command_requests r USING (idempotency_key)
            WHERE r.aggregate_id = 'task-contention') AS outcomes,
          (SELECT COUNT(*) FROM events WHERE aggregate_id = 'task-contention') AS events,
          (SELECT COUNT(*) FROM audit_entries a JOIN events e USING (event_id)
            WHERE e.aggregate_id = 'task-contention') AS audits,
          (SELECT COUNT(*) FROM command_requests
            WHERE idempotency_key = ${sqlString(loserCommand.key)}) AS loser_requests,
          (SELECT COUNT(*) FROM command_requests_identity
            WHERE identity = CONCAT('command_requests.pk', CHAR(31), ${sqlString(
              loserCommand.key,
            )})) AS loser_identity_rows,
          (SELECT COUNT(*) FROM events_identity
            WHERE identity = CONCAT('events.event_id', CHAR(31), ${sqlString(
              loserCommand.eventId,
            )})) AS loser_event_identity_rows
      `),
    ),
  );
  assertObservation(
    stateAfterContention.version === "1" &&
      stateAfterContention.title === winnerCommand.payload.title &&
      preRecoveryCounts.requests === "1" &&
      preRecoveryCounts.outcomes === "1" &&
      preRecoveryCounts.events === "1" &&
      preRecoveryCounts.audits === "1" &&
      preRecoveryCounts.loser_requests === "0" &&
      preRecoveryCounts.loser_identity_rows === "0" &&
      preRecoveryCounts.loser_event_identity_rows === "0",
    "serialization loser left partial state or the winner duplicated facts",
  );
  record(
    "direct_dolt.concurrent_same_version_cas",
    "Direct Dolt",
    "PASS",
    `Two barriered clients read version 0 on the same aggregate; one committed one state/Event/Audit/outcome and the other rolled back with SQLSTATE 40001 / Error 1213. Winner=${winnerCommand.key}.`,
  );

  doltSuccessfully(
    appDoltArgs(`
      START TRANSACTION;
      INSERT INTO command_requests
        (idempotency_key, aggregate_id, expected_version, request_hash, payload)
        VALUES (${sqlString(loserCommand.key)}, ${sqlString(loserCommand.aggregateId)},
          ${loserCommand.expectedVersion}, ${sqlString(loserCommand.requestHash)},
          JSON_OBJECT('title', '${loserCommand.payload.title}'));
      INSERT INTO command_outcomes
        (idempotency_key, outcome_type, observed_version, event_id, payload)
        VALUES (${sqlString(loserCommand.key)}, 'conflict', ${stateAfterContention.version},
          NULL, JSON_OBJECT('reason', 'expected_version_conflict'));
      COMMIT;
    `),
  );

  const winnerReplay = replayCommand(winnerCommand);
  const loserReplay = replayCommand(loserCommand);
  const secondLoserReplay = replayCommand(loserCommand);
  assertObservation(
    winnerReplay.replay &&
      winnerReplay.outcome === "applied" &&
      winnerReplay.observedVersion === 1 &&
      loserReplay.replay &&
      loserReplay.outcome === "conflict" &&
      loserReplay.observedVersion === 1 &&
      secondLoserReplay.outcome === "conflict",
    "exact replay did not return the preserved command outcome",
  );

  const mismatchedReplay = {
    ...winnerCommand,
    payload: { title: "Different Payload" },
  };
  let mismatchError;
  const winnerBeforeMismatchedReplay = readCommand(winnerCommand);
  try {
    replayCommand(mismatchedReplay);
  } catch (error) {
    mismatchError = error;
  }
  const winnerAfterMismatchedReplay = readCommand(winnerCommand);
  assertObservation(
    mismatchError instanceof CommandPayloadMismatchError &&
      mismatchError.code === "IDEMPOTENCY_PAYLOAD_MISMATCH" &&
      JSON.stringify(winnerAfterMismatchedReplay) === JSON.stringify(winnerBeforeMismatchedReplay),
    "conflicting replay did not return the explicit mismatch error",
  );
  const duplicateEventId = `event-${runId}-shared`;
  const duplicateAppend = `INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload) VALUES (${sqlString(
    duplicateEventId,
  )}, 'run-1', 300, 'task-1', 0, 'task.shared', JSON_OBJECT('title', 'Shared'));\nCOMMIT;\n`;
  const sessionC = openAppSqlSession();
  const sessionD = openAppSqlSession();
  const barrierC = `SHARED_${runId}_C`;
  const barrierD = `SHARED_${runId}_D`;
  sessionC.child.stdin.write(`START TRANSACTION;\nSELECT '${barrierC}' AS barrier;\n`);
  sessionD.child.stdin.write(`START TRANSACTION;\nSELECT '${barrierD}' AS barrier;\n`);
  await Promise.all([
    waitForSessionText(sessionC, barrierC),
    waitForSessionText(sessionD, barrierD),
  ]);
  sessionC.child.stdin.end(duplicateAppend);
  sessionD.child.stdin.end(duplicateAppend);
  const sharedIdentityResults = await Promise.all([sessionC.closed, sessionD.closed]);
  const sharedWinners = sharedIdentityResults.filter((result) => result.code === 0);
  const sharedRows = firstRow(
    doltSuccessfully(
      appDoltArgs(
        `SELECT COUNT(*) AS rows_present FROM events WHERE event_id = ${sqlString(duplicateEventId)}`,
      ),
    ),
  );
  const sharedIdentityRows = firstRow(
    doltSuccessfully(
      ownerDoltArgs(
        `SELECT COUNT(*) AS ledger_rows FROM events_identity WHERE identity = CONCAT('events.event_id', CHAR(31), ${sqlString(
          duplicateEventId,
        )})`,
      ),
    ),
  );
  assertObservation(
    sharedWinners.length === 1 &&
      sharedRows.rows_present === "1" &&
      sharedIdentityRows.ledger_rows === "1",
    `two barriered clients appending one Event identity did not resolve to a single durable row: ${JSON.stringify(
      sharedIdentityResults.map((result) => result.code),
    )} rows=${sharedRows.rows_present} ledger=${sharedIdentityRows.ledger_rows}`,
  );
  const sharedLoserDiagnostic = sharedIdentityResults
    .filter((result) => result.code !== 0)
    .map((result) => `${result.stdout}\n${result.stderr}`)
    .join(" ");
  record(
    "direct_dolt.concurrent_identity_uniqueness",
    "Direct Dolt",
    "PASS",
    `Two barriered clients committed the same Event identity concurrently; exactly one transaction succeeded, one durable row and one ledger identity remained, and the loser failed explicitly (${
      /Error 1213|40001/.test(sharedLoserDiagnostic)
        ? "serialization failure"
        : "append-only identity guard"
    }).`,
  );

  assertCredentialTransport("exact and mismatched replay phase");

  const triggerControlValue = () =>
    firstRow(doltSuccessfully(ownerDoltArgs("SELECT value FROM trigger_control_probe WHERE id = 1")))
      .value;
  const triggerControlBefore = triggerControlValue();
  const triggerControlUpdate = dolt(
    ownerDoltArgs("UPDATE trigger_control_probe SET value = 'updated' WHERE id = 1"),
  );
  const triggerControlAfterUpdate = triggerControlValue();
  const triggerControlOdku = dolt(
    ownerDoltArgs(
      "INSERT INTO trigger_control_probe (id, value) VALUES (1, 'odku') ON DUPLICATE KEY UPDATE value = 'odku'",
    ),
  );
  const triggerControlAfterOdku = triggerControlValue();
  const triggerGuardRowsAfterControl = firstRow(
    doltSuccessfully(ownerDoltArgs("SELECT COUNT(*) AS guard_rows FROM immutable_write_guard")),
  ).guard_rows;
  const triggerActivatesOnUpdate =
    triggerControlUpdate.code !== 0 && triggerControlAfterUpdate === triggerControlBefore;
  const triggerActivatesOnOdku =
    triggerControlOdku.code !== 0 && triggerControlAfterOdku === triggerControlAfterUpdate;
  const triggerControl = {
    identity: "database owner with full UPDATE privilege",
    direct_update_exit_code: triggerControlUpdate.code,
    direct_update_activated_guard: triggerActivatesOnUpdate,
    odku_exit_code: triggerControlOdku.code,
    odku_activated_guard: triggerActivatesOnOdku,
    value_before: triggerControlBefore,
    value_after_direct_update: triggerControlAfterUpdate,
    value_after_odku: triggerControlAfterOdku,
    guard_rows_after_control: Number(triggerGuardRowsAfterControl),
  };
  assertObservation(
    triggerActivatesOnUpdate && !triggerActivatesOnOdku,
    `trigger activation control did not reproduce an active BEFORE UPDATE guard that ON DUPLICATE KEY UPDATE bypasses: ${JSON.stringify(
      triggerControl,
    )}`,
  );
  record(
    "direct_dolt.trigger_activation_control",
    "Direct Dolt",
    "CONTROL",
    `A BEFORE UPDATE guard trigger is active on Dolt 2.3.2: a direct owner UPDATE exited ${triggerControlUpdate.code} and preserved the row. The same owner then changed the row through INSERT ... ON DUPLICATE KEY UPDATE with exit ${triggerControlOdku.code}, so the ODKU update path does not activate BEFORE UPDATE triggers for any identity.`,
  );

  const keylessRows = () =>
    parseJson(
      doltSuccessfully(
        ownerDoltArgs("SELECT id, value FROM keyless_append_probe ORDER BY id, value"),
      ).stdout,
    ).rows;
  doltSuccessfully(
    appDoltArgs("INSERT INTO keyless_append_probe (id, value) VALUES ('k1', 'original')"),
  );
  const keylessBefore = keylessRows();
  const keylessOdku = dolt(
    appDoltArgs(
      "INSERT INTO keyless_append_probe (id, value) VALUES ('k1', 'tampered') ON DUPLICATE KEY UPDATE value = 'tampered'",
    ),
  );
  const keylessReplace = dolt(
    appDoltArgs("REPLACE INTO keyless_append_probe (id, value) VALUES ('k1', 'replaced')"),
  );
  const keylessAfter = keylessRows();
  const keylessOriginalPreserved = keylessAfter.some(
    (row) => row.id === "k1" && row.value === "original",
  );
  doltSuccessfully(
    appDoltArgs("INSERT INTO unique_append_probe (id, value) VALUES ('u1', 'original')"),
  );
  const uniqueOdku = dolt(
    appDoltArgs(
      "INSERT INTO unique_append_probe (id, value) VALUES ('u1', 'tampered') ON DUPLICATE KEY UPDATE value = 'tampered'",
    ),
  );
  const uniqueRows = parseJson(
    doltSuccessfully(ownerDoltArgs("SELECT id, value FROM unique_append_probe ORDER BY id")).stdout,
  ).rows;
  const uniquenessEvidence = {
    keyless_table: {
      unique_index: false,
      odku_exit_code: keylessOdku.code,
      replace_exit_code: keylessReplace.code,
      rows_before: keylessBefore.length,
      rows_after: keylessAfter.length,
      original_row_preserved: keylessOriginalPreserved,
      duplicate_identifier_rows: keylessAfter.filter((row) => row.id === "k1").length,
    },
    unique_indexed_table: {
      unique_index: true,
      odku_exit_code: uniqueOdku.code,
      rows: uniqueRows.length,
      stored_value: uniqueRows[0]?.value ?? null,
    },
  };
  assertObservation(
    keylessOriginalPreserved &&
      keylessAfter.length > keylessBefore.length &&
      uniquenessEvidence.keyless_table.duplicate_identifier_rows > 1 &&
      uniqueOdku.code === 0 &&
      uniqueRows.length === 1 &&
      uniquenessEvidence.unique_indexed_table.stored_value === "tampered",
    `keyless and unique append probes did not reproduce the uniqueness/immutability tradeoff: ${JSON.stringify(
      uniquenessEvidence,
    )}`,
  );
  record(
    "direct_dolt.keyless_uniqueness_tradeoff",
    "Direct Dolt",
    "CONTROL",
    `Without a unique index the existing row survived, because ODKU had no key to match and appended instead (exit ${keylessOdku.code}, ${uniquenessEvidence.keyless_table.rows_before} to ${uniquenessEvidence.keyless_table.rows_after} rows, ${uniquenessEvidence.keyless_table.duplicate_identifier_rows} rows for one identifier) and REPLACE was denied (exit ${keylessReplace.code}); such a table cannot enforce idempotency-key or event-id uniqueness. Adding back the required UNIQUE index immediately restored the bypass: ODKU exited ${uniqueOdku.code} and changed the stored value.`,
  );
  const protectedRows = firstRow(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT
          (SELECT global_sequence FROM events WHERE event_id = 'event-0') AS event_pk,
          (SELECT sequence FROM audit_entries WHERE audit_id = 'audit-0') AS audit_pk
      `),
    ),
  );
  const existingEventPk = Number(protectedRows.event_pk);
  const existingAuditPk = Number(protectedRows.audit_pk);
  assertObservation(
    Number.isSafeInteger(existingEventPk) && Number.isSafeInteger(existingAuditPk),
    "protected auto-increment identities could not be read",
  );

  const tamperedTitle = "JSON_OBJECT('title', 'Tampered')";
  const immutableProbes = [
    {
      table: "command_requests",
      predicate: `idempotency_key = ${sqlString(winnerCommand.key)}`,
      witness: "expected_version",
      tamper: `expected_version = 999, request_hash = REPEAT('0', 64), payload = ${tamperedTitle}`,
      columns: ["idempotency_key", "aggregate_id", "expected_version", "request_hash", "payload"],
      collision: (identity, tag) => {
        assertObservation(identity === "pk", "unknown command_requests identity");
        return [sqlString(winnerCommand.key), "'task-contention'", "999", "REPEAT('0', 64)", tamperedTitle];
      },
      append: (tag) => [
        sqlString(`${winnerCommand.key}-append-${tag}`),
        "'task-contention'",
        "0",
        "REPEAT('c', 64)",
        `JSON_OBJECT('title', 'Append ${tag}')`,
      ],
    },
    {
      table: "command_outcomes",
      predicate: `idempotency_key = ${sqlString(winnerCommand.key)}`,
      witness: "outcome_type",
      tamper: `outcome_type = 'tampered', observed_version = 999, payload = ${tamperedTitle}`,
      columns: ["idempotency_key", "outcome_type", "observed_version", "event_id", "payload"],
      collision: (identity, tag) => {
        assertObservation(identity === "pk", "unknown command_outcomes identity");
        return [sqlString(winnerCommand.key), "'tampered'", "999", "NULL", tamperedTitle];
      },
      append: (tag) => [
        sqlString(`${winnerCommand.key}-append-${tag}`),
        "'applied'",
        "0",
        "NULL",
        `JSON_OBJECT('title', 'Append ${tag}')`,
      ],
      appendPrerequisite: (tag) =>
        `INSERT INTO command_requests (idempotency_key, aggregate_id, expected_version, request_hash, payload) VALUES (${sqlString(
          `${winnerCommand.key}-append-${tag}`,
        )}, 'task-contention', 0, REPEAT('c', 64), JSON_OBJECT('title', 'Append ${tag}'))`,
    },
    {
      table: "candidates",
      predicate: "id = 'candidate-1'",
      witness: "commit_sha",
      tamper: `commit_sha = REPEAT('b', 40), data = JSON_OBJECT('current', false)`,
      columns: ["id", "run_id", "sequence", "commit_sha", "data"],
      collision: (identity, tag) => {
        if (identity === "pk") {
          return ["'candidate-1'", "'run-1'", `${700 + tag}`, "REPEAT('b', 40)", "JSON_OBJECT('current', false)"];
        }
        assertObservation(identity === "run_sequence", "unknown candidates identity");
        return [sqlString(`candidate-collide-${tag}`), "'run-1'", "1", "REPEAT('b', 40)", "JSON_OBJECT('current', false)"];
      },
      append: (tag) => [
        sqlString(`candidate-append-${tag}`),
        "'run-1'",
        `${900 + tag}`,
        "REPEAT('c', 40)",
        "JSON_OBJECT('current', false)",
      ],
    },
    {
      table: "events",
      predicate: "event_id = 'event-0'",
      witness: "event_type",
      tamper: `event_type = 'tampered', payload = ${tamperedTitle}`,
      columns: [
        "global_sequence",
        "event_id",
        "run_id",
        "sequence",
        "aggregate_id",
        "aggregate_version",
        "event_type",
        "payload",
      ],
      collision: (identity, tag) => {
        if (identity === "pk") {
          return [
            `${existingEventPk}`,
            sqlString(`event-collide-${tag}`),
            "'run-1'",
            `${700 + tag}`,
            "'task-1'",
            "0",
            "'tampered'",
            tamperedTitle,
          ];
        }
        if (identity === "event_id") {
          return [null, "'event-0'", "'run-1'", `${750 + tag}`, "'task-1'", "0", "'tampered'", tamperedTitle];
        }
        assertObservation(identity === "run_sequence", "unknown events identity");
        return [null, sqlString(`event-collide-rs-${tag}`), "'run-1'", "1", "'task-1'", "0", "'tampered'", tamperedTitle];
      },
      explicitAutoIncrement: (tag) => `${8000 + tag}`,
      append: (tag) => [
        null,
        sqlString(`event-append-${tag}`),
        "'run-1'",
        `${900 + tag}`,
        "'task-1'",
        "0",
        "'task.appended'",
        `JSON_OBJECT('title', 'Append ${tag}')`,
      ],
    },
    {
      table: "audit_entries",
      predicate: "audit_id = 'audit-0'",
      witness: "actor_type",
      tamper: `actor_type = 'tampered', payload = JSON_OBJECT('source', 'tampered')`,
      columns: ["sequence", "audit_id", "event_id", "actor_type", "payload"],
      collision: (identity, tag) => {
        if (identity === "pk") {
          return [
            `${existingAuditPk}`,
            sqlString(`audit-collide-${tag}`),
            "'event-0'",
            "'tampered'",
            "JSON_OBJECT('source', 'tampered')",
          ];
        }
        assertObservation(identity === "audit_id", "unknown audit_entries identity");
        return [null, "'audit-0'", "'event-0'", "'tampered'", "JSON_OBJECT('source', 'tampered')"];
      },
      explicitAutoIncrement: (tag) => `${8000 + tag}`,
      append: (tag) => [
        null,
        sqlString(`audit-append-${tag}`),
        "'event-0'",
        "'system'",
        `JSON_OBJECT('source', 'append-${tag}')`,
      ],
    },
  ];

  assertObservation(
    immutableProbes.length === immutableTableModel.length &&
      immutableProbes.every((probe, index) => probe.table === immutableTableModel[index].table),
    "the adversarial probe list does not match the guarded table model",
  );

  const declaredIdentities = parseJson(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT table_name AS declared_table, index_name AS declared_index,
          GROUP_CONCAT(column_name ORDER BY seq_in_index) AS declared_columns
        FROM information_schema.statistics
        WHERE table_schema = DATABASE() AND non_unique = 0
          AND table_name IN (${immutableTableModel.map((spec) => sqlString(spec.table)).join(", ")})
        GROUP BY table_name, index_name
      `),
    ).stdout,
  ).rows;
  assertObservation(
    declaredIdentities.length > 0 &&
      declaredIdentities.every((row) => row.declared_table && row.declared_columns),
    "declared unique identities could not be read from information_schema",
  );
  const declaredKey = new Set(
    declaredIdentities.map((row) => `${row.declared_table}:${row.declared_columns.split(",").join("+")}`),
  );
  const guardedKey = new Set(
    immutableTableModel.flatMap((spec) =>
      spec.identities.map((identity) => `${spec.table}:${identity.columns.join("+")}`),
    ),
  );
  const uncoveredIdentities = [...declaredKey].filter((key) => !guardedKey.has(key));
  assertObservation(
    uncoveredIdentities.length === 0 && declaredKey.size === guardedKey.size,
    `guards do not cover every declared unique identity: declared=${[...declaredKey].join(
      ", ",
    )} guarded=${[...guardedKey].join(", ")}`,
  );

  const buildInsert = (probe, values) => {
    const kept = probe.columns.filter((column, index) => values[index] !== null);
    const literals = values.filter((value) => value !== null);
    return { columns: kept.join(", "), values: literals.join(", ") };
  };

  const statementResults = [];
  const deferredTransactionAttacks = [];
  const snapshotRow = (probe) =>
    JSON.stringify(
      firstRow(doltSuccessfully(ownerDoltArgs(`SELECT * FROM ${probe.table} WHERE ${probe.predicate}`))),
    );
  const tableCount = (table) =>
    Number(firstRow(doltSuccessfully(ownerDoltArgs(`SELECT COUNT(*) AS rows_present FROM ${table}`))).rows_present);
  const recordStatement = (entry) => {
    statementResults.push(entry);
    return entry;
  };
  const runProbe = async (probe, label, identity, sql, expectation) => {
    const before = snapshotRow(probe);
    const rowsBefore = tableCount(probe.table);
    const result =
      expectation === "denied"
        ? dolt(appDoltArgs(sql), { tolerateTimeout: true, timeout: adversarialTimeoutMs })
        : dolt(appDoltArgs(sql));
    const after = snapshotRow(probe);
    const rowsAfter = tableCount(probe.table);
    const entry = {
      table: probe.table,
      identity,
      statement: label,
      exit_code: result.code,
      protected_row_preserved: before === after,
      rows_before: rowsBefore,
      rows_after: rowsAfter,
    };
    if (expectation === "denied") {
      entry.diagnostic = (result.stderr || result.stdout).trim().split("\n").slice(-1)[0].slice(-160);
      entry.blocked_without_returning = Boolean(result.timedOut);
      assertObservation(
        result.code !== 0 && before === after && rowsBefore === rowsAfter,
        `${probe.table} ${identity} ${label} was not rejected without side effects: exit=${result.code} preserved=${
          before === after
        } rows ${rowsBefore}->${rowsAfter}`,
      );
    } else {
      assertObservation(
        result.code === 0 && before === after && rowsAfter === rowsBefore + 1,
        `${probe.table} ${identity} ${label} did not append exactly one new row: exit=${result.code} rows ${rowsBefore}->${rowsAfter} ${(
          result.stderr || result.stdout
        ).trim()}`,
      );
    }
    return recordStatement(entry);
  };

  let probeTag = 0;
  for (const probe of immutableProbes) {
    const spec = immutableTableModel.find((candidate) => candidate.table === probe.table);
    const appendStatement = (tag) => {
      if (probe.appendPrerequisite) {
        doltSuccessfully(appDoltArgs(probe.appendPrerequisite(tag)));
      }
      const insert = buildInsert(probe, probe.append(tag));
      return `INSERT INTO ${probe.table} (${insert.columns}) VALUES (${insert.values})`;
    };

    probeTag += 1;
    await runProbe(probe, "append_control_before", "none", appendStatement(probeTag), "appended");

    for (const identity of spec.identities) {
      probeTag += 1;
      const collisionValues = probe.collision(identity.name, probeTag);
      const collision = buildInsert(probe, collisionValues);
      const multiRowTag = probeTag + 500;
      if (probe.appendPrerequisite) {
        doltSuccessfully(appDoltArgs(probe.appendPrerequisite(multiRowTag)));
      }
      const companionValues = probe
        .append(multiRowTag)
        .map((value, index) =>
          value === null && collisionValues[index] !== null
            ? probe.explicitAutoIncrement(multiRowTag)
            : value,
        );
      const companion = buildInsert(probe, companionValues);
      assertObservation(
        companion.columns === collision.columns,
        `${probe.table} multi-row probe column lists diverge`,
      );
      const forms = [
        ["plain_duplicate", `INSERT INTO ${probe.table} (${collision.columns}) VALUES (${collision.values})`],
        [
          "insert_ignore",
          `INSERT IGNORE INTO ${probe.table} (${collision.columns}) VALUES (${collision.values})`,
        ],
        [
          "odku_literal",
          `INSERT INTO ${probe.table} (${collision.columns}) VALUES (${collision.values}) ON DUPLICATE KEY UPDATE ${probe.tamper}`,
        ],
        [
          "odku_values_function",
          `INSERT INTO ${probe.table} (${collision.columns}) VALUES (${collision.values}) ON DUPLICATE KEY UPDATE ${probe.witness} = VALUES(${probe.witness})`,
        ],
        [
          "insert_ignore_odku",
          `INSERT IGNORE INTO ${probe.table} (${collision.columns}) VALUES (${collision.values}) ON DUPLICATE KEY UPDATE ${probe.tamper}`,
        ],
        [
          "insert_select_odku",
          `INSERT INTO ${probe.table} (${collision.columns}) SELECT ${collision.values} FROM ${probe.table}_identity LIMIT 1 ON DUPLICATE KEY UPDATE ${probe.tamper}`,
        ],
        [
          "multi_row_odku",
          `INSERT INTO ${probe.table} (${collision.columns}) VALUES (${companion.values}), (${collision.values}) ON DUPLICATE KEY UPDATE ${probe.tamper}`,
        ],
      ];
      deferredTransactionAttacks.push({
        scope: probe.table,
        identity: identity.name,
        sql: `INSERT INTO ${probe.table} (${collision.columns}) VALUES (${collision.values}) ON DUPLICATE KEY UPDATE ${probe.tamper}`,
        verify: () => snapshotRow(probe),
      });
      for (const [label, sql] of forms) {
        await runProbe(probe, label, identity.name, sql, "denied");
      }
    }

    const exact = buildInsert(probe, probe.collision(spec.identities[0].name, probeTag + 1));
    for (const [label, sql] of [
      ["replace_collision", `REPLACE INTO ${probe.table} (${exact.columns}) VALUES (${exact.values})`],
      ["update", `UPDATE ${probe.table} SET ${probe.tamper} WHERE ${probe.predicate}`],
      ["delete", `DELETE FROM ${probe.table} WHERE ${probe.predicate}`],
      ["truncate", `TRUNCATE TABLE ${probe.table}`],
      ["alter", `ALTER TABLE ${probe.table} ADD COLUMN forbidden_mutation_probe INT`],
      ["drop_append_guard", `DROP TRIGGER ${probe.table}_append_only`],
      [
        "replace_append_guard",
        `CREATE TRIGGER ${probe.table}_bypass BEFORE INSERT ON ${probe.table} FOR EACH ROW SET @bypass = 1`,
      ],
      ["delete_identity_ledger", `DELETE FROM ${probe.table}_identity`],
      ["truncate_identity_ledger", `TRUNCATE TABLE ${probe.table}_identity`],
      ["drop_identity_ledger", `DROP TABLE ${probe.table}_identity`],
      ["alter_identity_ledger", `ALTER TABLE ${probe.table}_identity DROP PRIMARY KEY`],
    ]) {
      await runProbe(probe, label, "table", sql, "denied");
    }

    probeTag += 1;
    await runProbe(probe, "append_control_after", "none", appendStatement(probeTag), "appended");
  }

  const versionControlProbes = [];
  for (const procedure of [
    "CALL DOLT_RESET('--hard')",
    "CALL DOLT_CHECKOUT('-b', 'director-m04-bypass')",
    "CALL DOLT_REVERT('HEAD')",
    "CALL DOLT_BRANCH('director-m04-bypass')",
    "CALL DOLT_COMMIT('-A', '-m', 'bypass')",
  ]) {
    const before = snapshotRow(immutableProbes[3]);
    const result = dolt(appDoltArgs(procedure), {
      tolerateTimeout: true,
      timeout: adversarialTimeoutMs,
    });
    const after = snapshotRow(immutableProbes[3]);
    assertObservation(
      result.code !== 0 && before === after,
      `${procedure} was not denied to the application identity`,
    );
    versionControlProbes.push({
      procedure,
      exit_code: result.code,
      denied: true,
      protected_row_preserved: true,
      diagnostic: (result.stderr || result.stdout).trim().split("\n").slice(-1)[0].slice(-160),
    });
  }

  const guardState = firstRow(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT
          (SELECT commit_sha FROM candidates WHERE id = 'candidate-1') AS candidate_sha,
          (SELECT event_type FROM events WHERE event_id = 'event-0') AS event_type,
          (SELECT actor_type FROM audit_entries WHERE audit_id = 'audit-0') AS audit_actor,
          (SELECT COUNT(*) FROM immutable_write_guard) AS guard_rows,
          (SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema = DATABASE())
            AS installed_triggers,
          (SELECT COUNT(*) FROM information_schema.columns
            WHERE table_schema = DATABASE() AND column_name = 'forbidden_mutation_probe')
            AS forbidden_columns
      `),
    ),
  );
  const winnerAfterMutationProbes = firstRow(
    doltSuccessfully(
      appDoltArgs(`
        SELECT r.expected_version, r.request_hash,
          JSON_UNQUOTE(JSON_EXTRACT(r.payload, '$.title')) AS request_title,
          o.outcome_type, o.observed_version,
          JSON_UNQUOTE(JSON_EXTRACT(o.payload, '$.title')) AS outcome_title
        FROM command_requests r JOIN command_outcomes o USING (idempotency_key)
        WHERE r.idempotency_key = ${sqlString(winnerCommand.key)}
      `),
    ),
  );
  const expectedTriggers =
    immutableTableModel.length * 3 +
    mutableAggregateIdentityModel.length * 2 +
    1 +
    foreignKeyModel.length +
    foreignKeyModel.filter((entry) => entry.table === "aggregates").length +
    1;
  const deniedStatements = statementResults.filter((entry) => entry.exit_code !== 0);
  const appendedStatements = statementResults.filter((entry) => entry.exit_code === 0);
  const guardedIdentityCount = immutableTableModel.reduce(
    (total, spec) => total + spec.identities.length,
    0,
  );
  assertObservation(
    guardState.candidate_sha === "a".repeat(40) &&
      guardState.event_type === "task.created" &&
      guardState.audit_actor === "system" &&
      winnerAfterMutationProbes.expected_version === "0" &&
      winnerAfterMutationProbes.request_hash === winnerCommand.requestHash &&
      winnerAfterMutationProbes.request_title === winnerCommand.payload.title &&
      winnerAfterMutationProbes.outcome_type === "applied" &&
      winnerAfterMutationProbes.observed_version === "1" &&
      winnerAfterMutationProbes.outcome_title === winnerCommand.payload.title,
    `an immutable record changed during the adversarial matrix: ${JSON.stringify(
      guardState,
    )} ${JSON.stringify(winnerAfterMutationProbes)}`,
  );
  assertObservation(
    guardState.guard_rows === "1" &&
      guardState.installed_triggers === String(expectedTriggers) &&
      guardState.forbidden_columns === "0",
    `guard installation changed during the adversarial matrix: expected ${expectedTriggers} triggers, observed ${JSON.stringify(
      guardState,
    )}`,
  );
  assertObservation(
    appendedStatements.length === immutableProbes.length * 2 &&
      deniedStatements.length === statementResults.length - appendedStatements.length &&
      statementResults.length === guardedIdentityCount * 7 + immutableProbes.length * 13,
    `unexpected adversarial matrix size: ${statementResults.length} observations, ${appendedStatements.length} appends`,
  );
  immutabilityEvidence = {
    mechanism:
      "owner-definer BEFORE INSERT trigger per immutable table that records every declared unique identity in an append-only VARBINARY identity ledger with its own primary key",
    guarded_tables: immutableTableModel.map((spec) => spec.table),
    guarded_identities: immutableTableModel.flatMap((spec) =>
      spec.identities.map((identity) => `${spec.table}(${identity.columns.join(", ")})`),
    ),
    declared_unique_identities: [...declaredKey].sort(),
    uncovered_identities: uncoveredIdentities,
    installed_triggers: Number(guardState.installed_triggers),
    trigger_guard_rows: Number(guardState.guard_rows),
    trigger_activation_control: triggerControl,
    uniqueness_tradeoff: uniquenessEvidence,
    base_table_observations: statementResults.length,
    base_table_denied_statements: deniedStatements.length,
    base_table_successful_appends: appendedStatements.length,
    odku_existing_row_mutations: 0,
    version_control_probes: versionControlProbes,
    statement_results: statementResults,
    preserved_values: {
      command_request_expected_version: winnerAfterMutationProbes.expected_version,
      command_outcome_type: winnerAfterMutationProbes.outcome_type,
      candidate_sha: guardState.candidate_sha,
      event_type: guardState.event_type,
      audit_actor: guardState.audit_actor,
    },
  };
  record(
    "direct_dolt.database_boundary_immutability",
    "Direct Dolt",
    "PASS",
    `BEFORE INSERT identity guards on ${immutableTableModel.length} immutable tables covering all ${guardedIdentityCount} declared unique identities rejected every one of ${deniedStatements.length} non-transaction-wrapped base-table adversarial statements, including exact/mismatched, VALUES(), INSERT ... SELECT, primary-key-targeted, multi-row and IGNORE ON DUPLICATE KEY UPDATE, REPLACE, UPDATE, DELETE, TRUNCATE, ALTER, guard-drop/replace and ledger-tamper attempts, while ${appendedStatements.length} distinct appends before and after the base matrix succeeded and every protected value stayed unchanged. Deferred transaction-wrapped attacks are reported separately.`,
  );
  const ledgerTables = [
    ...identityLedgerModel.map((spec) => `${spec.table}_identity`),
    "parent_guard",
    "guard_constants",
    "immutable_write_guard",
  ];
  const numericGuardTables = new Set(["guard_constants", "immutable_write_guard"]);
  const ledgerDigest = (table) =>
    createHash("sha256")
      .update(
        parseJson(
          doltSuccessfully(
            ownerDoltArgs(`SELECT HEX(${numericGuardTables.has(table) ? "singleton" : "identity"}) AS row_hex FROM ${table} ORDER BY 1`),
          ).stdout,
        )
          .rows.map((row) => row.row_hex)
          .join("|"),
      )
      .digest("hex");
  const ledgerAttackResults = [];
  for (const ledger of ledgerTables) {
    const keyColumn = numericGuardTables.has(ledger) ? "singleton" : "identity";
    const sample = parseJson(
      doltSuccessfully(
        ownerDoltArgs(
          `SELECT HEX(${keyColumn}) AS sample_hex FROM ${ledger} ORDER BY 1 LIMIT 1`,
        ),
      ).stdout,
    ).rows[0];
    assertObservation(
      sample !== undefined && /^[0-9A-F]*$/.test(sample.sample_hex),
      `${ledger} had no row to defend`,
    );
    const existing =
      numericGuardTables.has(ledger) ? String(parseInt(sample.sample_hex, 16)) : `UNHEX('${sample.sample_hex}')`;
    const fresh = numericGuardTables.has(ledger) ? "99" : sqlString("unused.reservation.probe");
    const renamed = numericGuardTables.has(ledger) ? "98" : sqlString("freed.by.attacker");
    const forms = [
      ["reserve_unused_identity", `INSERT INTO ${ledger} (${keyColumn}) VALUES (${fresh})`],
      ["insert_ignore_unused", `INSERT IGNORE INTO ${ledger} (${keyColumn}) VALUES (${fresh})`],
      [
        "odku_rename_existing",
        `INSERT INTO ${ledger} (${keyColumn}) VALUES (${existing}) ON DUPLICATE KEY UPDATE ${keyColumn} = ${renamed}`,
      ],
      [
        "odku_values_function",
        `INSERT INTO ${ledger} (${keyColumn}) VALUES (${existing}) ON DUPLICATE KEY UPDATE ${keyColumn} = VALUES(${keyColumn})`,
      ],
      [
        "insert_ignore_odku",
        `INSERT IGNORE INTO ${ledger} (${keyColumn}) VALUES (${existing}) ON DUPLICATE KEY UPDATE ${keyColumn} = ${renamed}`,
      ],
      [
        "insert_select_odku",
        `INSERT INTO ${ledger} (${keyColumn}) SELECT ${existing} FROM guard_constants LIMIT 1 ON DUPLICATE KEY UPDATE ${keyColumn} = ${renamed}`,
      ],
      [
        "multi_row_odku",
        `INSERT INTO ${ledger} (${keyColumn}) VALUES (${fresh}), (${existing}) ON DUPLICATE KEY UPDATE ${keyColumn} = ${renamed}`,
      ],
      ["replace", `REPLACE INTO ${ledger} (${keyColumn}) VALUES (${existing})`],
      ["update", `UPDATE ${ledger} SET ${keyColumn} = ${renamed} WHERE ${keyColumn} = ${existing}`],
      ["delete", `DELETE FROM ${ledger} WHERE ${keyColumn} = ${existing}`],
      ["truncate", `TRUNCATE TABLE ${ledger}`],
      ["alter_drop_key", `ALTER TABLE ${ledger} DROP PRIMARY KEY`],
      ["drop_table", `DROP TABLE ${ledger}`],
    ];
    deferredTransactionAttacks.push({
      scope: ledger,
      identity: keyColumn,
      sql: `INSERT INTO ${ledger} (${keyColumn}) VALUES (${existing}) ON DUPLICATE KEY UPDATE ${keyColumn} = ${renamed}`,
      verify: () => ledgerDigest(ledger),
    });
    for (const [label, sql] of forms) {
      const before = ledgerDigest(ledger);
      const result = dolt(appDoltArgs(sql), {
        tolerateTimeout: true,
        timeout: adversarialTimeoutMs,
      });
      const after = ledgerDigest(ledger);
      assertObservation(
        result.code !== 0 && before === after,
        `${ledger} ${label} was not denied without side effects: exit=${result.code} digest ${before} -> ${after}`,
      );
      ledgerAttackResults.push({
        ledger,
        statement: label,
        exit_code: result.code,
        blocked_without_returning: Boolean(result.timedOut),
        contents_preserved: true,
        diagnostic: (result.stderr || result.stdout).trim().split("\n").slice(-1)[0].slice(-140),
      });
    }
  }

  const twoStepResults = [];
  for (const probe of immutableProbes) {
    const spec = immutableTableModel.find((candidate) => candidate.table === probe.table);
    const ledger = `${probe.table}_identity`;
    for (const identity of spec.identities) {
      const protectedValues = firstRow(
        doltSuccessfully(
          ownerDoltArgs(
            `SELECT HEX(CONCAT('${probe.table}.${identity.name}', CHAR(31), ${identity.columns.join(
              ", CHAR(31), ",
            )})) AS identity_hex FROM ${probe.table} WHERE ${probe.predicate}`,
          ),
        ),
      ).identity_hex;
      const rowBefore = snapshotRow(probe);
      const ledgerBefore = ledgerDigest(ledger);
      const step1 = dolt(
        appDoltArgs(
          `INSERT INTO ${ledger} (identity) VALUES (${sqlString(
            "unused.two.step.probe",
          )}) ON DUPLICATE KEY UPDATE identity = CONCAT(identity, CHAR(31), 'freed')`,
        ),
        { tolerateTimeout: true, timeout: adversarialTimeoutMs },
      );
      const collision = buildInsert(probe, probe.collision(identity.name, 4000));
      const step2 = dolt(
        appDoltArgs(
          `INSERT INTO ${probe.table} (${collision.columns}) VALUES (${collision.values}) ON DUPLICATE KEY UPDATE ${probe.tamper}`,
        ),
        { tolerateTimeout: true, timeout: adversarialTimeoutMs },
      );
      const rowAfter = snapshotRow(probe);
      const ledgerAfter = ledgerDigest(ledger);
      assertObservation(
        step1.code !== 0 &&
          step2.code !== 0 &&
          rowBefore === rowAfter &&
          ledgerBefore === ledgerAfter,
        `two-step attack on ${probe.table}.${identity.name} was not fully denied: step1=${step1.code} step2=${step2.code} row_preserved=${
          rowBefore === rowAfter
        } ledger_preserved=${ledgerBefore === ledgerAfter}`,
      );
      twoStepResults.push({
        table: probe.table,
        identity: identity.name,
        protected_identity_present: typeof protectedValues === "string" && protectedValues.length > 0,
        step1_ledger_rename_exit_code: step1.code,
        step2_base_odku_exit_code: step2.code,
        base_row_preserved: true,
        ledger_preserved: true,
      });
    }
  }
  record(
    "direct_dolt.ledger_boundary",
    "Direct Dolt",
    "PASS",
    `The application identity holds SELECT and no write privilege on any identity ledger: ${ledgerAttackResults.length} direct ledger statements were denied with byte-identical ledger contents, unused-identity reservation is impossible, and the two-step ledger-rename attack was denied at both steps for all ${twoStepResults.length} table/identity pairs.`,
  );

  const referentialProbes = [
    {
      label: "command_outcomes_without_request",
      sql: `INSERT INTO command_outcomes (idempotency_key, outcome_type, observed_version, event_id, payload) VALUES ('cmd-orphan-${runId}', 'applied', 1, NULL, JSON_OBJECT('title', 'Orphan'))`,
    },
    {
      label: "audit_without_event",
      sql: `INSERT INTO audit_entries (audit_id, event_id, actor_type, payload) VALUES ('audit-orphan-${runId}', 'event-does-not-exist', 'system', JSON_OBJECT('source', 'orphan'))`,
    },
    {
      label: "event_without_run",
      sql: `INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload) VALUES ('event-orphan-${runId}', 'run-does-not-exist', 401, 'task-1', 0, 'task.orphan', JSON_OBJECT('title', 'Orphan'))`,
    },
    {
      label: "candidate_without_run",
      sql: `INSERT INTO candidates (id, run_id, sequence, commit_sha, data) VALUES ('candidate-orphan-${runId}', 'run-does-not-exist', 402, REPEAT('d', 40), JSON_OBJECT('current', false))`,
    },
    {
      label: "dependency_without_aggregate",
      sql: `INSERT INTO dependencies (source_id, target_id, dependency_type) VALUES ('task-1', 'task-does-not-exist', 'blocks')`,
    },
    {
      label: "aggregate_without_parent",
      sql: `INSERT INTO aggregates (id, kind, parent_id, version, data) VALUES ('task-orphan-${runId}', 'task', 'parent-does-not-exist', 0, JSON_OBJECT('title', 'Orphan'))`,
    },
    {
      label: "aggregate_update_to_missing_parent",
      sql: `UPDATE aggregates SET parent_id = 'parent-does-not-exist' WHERE id = 'task-1'`,
    },
  ];
  const referentialResults = [];
  for (const probe of referentialProbes) {
    for (const [mode, prefix] of [
      ["enforced", ""],
      ["fk_checks_disabled", "SET SESSION foreign_key_checks=0; "],
      ["fk_checks_disabled_relaxed_sql_mode", "SET SESSION sql_mode=''; SET SESSION foreign_key_checks=0; "],
    ]) {
      const result = dolt(appDoltArgs(`${prefix}${probe.sql};`), {
        tolerateTimeout: true,
        timeout: adversarialTimeoutMs,
      });
      assertObservation(
        result.code !== 0,
        `${probe.label} was accepted with ${mode}: ${(result.stderr || result.stdout).trim()}`,
      );
      referentialResults.push({
        probe: probe.label,
        session_mode: mode,
        exit_code: result.code,
        denied: true,
      });
    }
  }
  const orphanRows = firstRow(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT
          (SELECT COUNT(*) FROM command_outcomes o
            LEFT JOIN command_requests r USING (idempotency_key) WHERE r.idempotency_key IS NULL) AS orphan_outcomes,
          (SELECT COUNT(*) FROM audit_entries a
            LEFT JOIN events e ON e.event_id = a.event_id WHERE e.event_id IS NULL) AS orphan_audits,
          (SELECT COUNT(*) FROM events e
            LEFT JOIN aggregates g ON g.id = e.run_id WHERE g.id IS NULL) AS orphan_events,
          (SELECT COUNT(*) FROM candidates c
            LEFT JOIN aggregates g ON g.id = c.run_id WHERE g.id IS NULL) AS orphan_candidates,
          (SELECT COUNT(*) FROM aggregates a
            LEFT JOIN aggregates p ON p.id = a.parent_id
            WHERE a.parent_id IS NOT NULL AND p.id IS NULL) AS orphan_aggregates
      `),
    ),
  );
  const validWithFkDisabled = dolt(
    appDoltArgs(
      `SET SESSION foreign_key_checks=0; INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload) VALUES ('event-fkoff-${runId}', 'run-1', 403, 'task-1', 0, 'task.fkoff', JSON_OBJECT('title', 'Valid'));`,
    ),
  );
  assertObservation(
    orphanRows.orphan_outcomes === "0" &&
      orphanRows.orphan_audits === "0" &&
      orphanRows.orphan_events === "0" &&
      orphanRows.orphan_candidates === "0" &&
      orphanRows.orphan_aggregates === "0" &&
      validWithFkDisabled.code === 0,
    `referential integrity was not preserved: ${JSON.stringify(orphanRows)} valid_append_with_fk_disabled=${validWithFkDisabled.code}`,
  );
  const parentChildSessionA = openAppSqlSession();
  const parentChildBarrier = `PARENT_${runId}`;
  parentChildSessionA.child.stdin.write(
    `START TRANSACTION;\nINSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data) VALUES ('run-uncommitted-${runId}', 'run', 'task-1', 'workspace-1', 0, JSON_OBJECT('runNumber', 2));\nSELECT '${parentChildBarrier}' AS barrier;\n`,
  );
  await waitForSessionText(parentChildSessionA, parentChildBarrier);
  const childBeforeParentCommit = dolt(
    appDoltArgs(
      `SET SESSION foreign_key_checks=0; INSERT INTO candidates (id, run_id, sequence, commit_sha, data) VALUES ('candidate-race-${runId}', 'run-uncommitted-${runId}', 404, REPEAT('e', 40), JSON_OBJECT('current', false));`,
    ),
    { tolerateTimeout: true, timeout: adversarialTimeoutMs },
  );
  parentChildSessionA.child.stdin.end("COMMIT;\n");
  const parentCommit = await parentChildSessionA.closed;
  const childAfterParentCommit = dolt(
    appDoltArgs(
      `INSERT INTO candidates (id, run_id, sequence, commit_sha, data) VALUES ('candidate-race-${runId}', 'run-uncommitted-${runId}', 404, REPEAT('e', 40), JSON_OBJECT('current', false))`,
    ),
  );
  assertObservation(
    childBeforeParentCommit.code !== 0 &&
      parentCommit.code === 0 &&
      childAfterParentCommit.code === 0,
    `concurrent parent/child ordering was not enforced: child_before=${childBeforeParentCommit.code} parent=${parentCommit.code} child_after=${childAfterParentCommit.code}`,
  );
  const rollbackProbe = dolt(
    appDoltArgs(
      `START TRANSACTION; INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data) VALUES ('run-rollback-${runId}', 'run', 'task-1', 'workspace-1', 0, JSON_OBJECT('runNumber', 3)); INSERT INTO candidates (id, run_id, sequence, commit_sha, data) VALUES ('candidate-rollback-${runId}', 'run-rollback-${runId}', 405, REPEAT('f', 40), JSON_OBJECT('current', false)); ROLLBACK;`,
    ),
  );
  const rollbackState = firstRow(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT
          (SELECT COUNT(*) FROM aggregates WHERE id = 'run-rollback-${runId}') AS parent_rows,
          (SELECT COUNT(*) FROM candidates WHERE id = 'candidate-rollback-${runId}') AS child_rows,
          (SELECT COUNT(*) FROM candidates_identity
            WHERE identity = CONCAT('candidates.pk', CHAR(31), 'candidate-rollback-${runId}')) AS ledger_rows
      `),
    ),
  );
  const replayAfterRollback = dolt(
    appDoltArgs(
      `START TRANSACTION; INSERT INTO aggregates (id, kind, parent_id, workspace_id, version, data) VALUES ('run-rollback-${runId}', 'run', 'task-1', 'workspace-1', 0, JSON_OBJECT('runNumber', 3)); INSERT INTO candidates (id, run_id, sequence, commit_sha, data) VALUES ('candidate-rollback-${runId}', 'run-rollback-${runId}', 405, REPEAT('f', 40), JSON_OBJECT('current', false)); COMMIT;`,
    ),
  );
  assertObservation(
    rollbackState.parent_rows === "0" &&
      rollbackState.child_rows === "0" &&
      rollbackState.ledger_rows === "0" &&
      replayAfterRollback.code === 0,
    `rolled-back parent/child work left residue or blocked replay: ${JSON.stringify(
      rollbackState,
    )} replay=${replayAfterRollback.code} rollback_exit=${rollbackProbe.code}`,
  );
  record(
    "direct_dolt.referential_parent_guards",
    "Direct Dolt",
    "PASS",
    `Supported BEFORE INSERT and BEFORE UPDATE parent-existence guards covering all ${foreignKeyModel.length} declared foreign keys denied every orphan append across ${referentialResults.length} probes with foreign_key_checks enforced, disabled, and disabled under a relaxed sql_mode, while valid appends still succeeded with foreign_key_checks disabled; a child could not commit against an uncommitted parent, a rolled-back parent/child transaction left no row or ledger residue, and the identical work replayed successfully afterwards.`,
  );

  const transactionAttackResults = [];
  for (const attack of deferredTransactionAttacks) {
    const before = attack.verify();
    const result = await runTransactionProbe(attack.sql);
    const after = attack.verify();
    assertObservation(
      result.code !== 0 && before === after,
      `transaction-wrapped attack on ${attack.scope}.${attack.identity} was not denied without effect: exit=${result.code} preserved=${
        before === after
      }`,
    );
    transactionAttackResults.push({
      scope: attack.scope,
      identity: attack.identity,
      statement: "transaction_wrapped_odku",
      exit_code: result.code,
      target_preserved: true,
    });
  }
  const lockRetentionProbe = dolt(
    appDoltArgs("INSERT INTO events_identity (identity) VALUES ('post.transaction.probe')"),
    { tolerateTimeout: true, timeout: adversarialTimeoutMs },
  );
  record(
    "direct_dolt.transaction_wrapped_attacks",
    "Direct Dolt",
    "PASS",
    `All ${transactionAttackResults.length} transaction-wrapped ON DUPLICATE KEY UPDATE attacks against every immutable table identity and every ledger were rejected inside their transaction and left their target byte-identical. These run last because Dolt 2.3.2 keeps the write locks of a transaction whose client disconnects after a failed statement, which can block later writers to the same table until the server stops.`,
  );
  immutabilityEvidence.deferred_transaction_attack_observations =
    transactionAttackResults.length;

  const indexRows = parseJson(
    doltSuccessfully(
      ownerDoltArgs(`
        SELECT table_name AS idx_table, index_name AS idx_name, seq_in_index AS idx_position,
          column_name AS idx_column, sub_part AS idx_prefix, expression AS idx_expression,
          nullable AS idx_nullable, index_type AS idx_type
        FROM information_schema.statistics
        WHERE table_schema = DATABASE() AND non_unique = 0
        ORDER BY table_name, index_name, seq_in_index
      `),
    ).stdout,
  ).rows;
  const isEmpty = (value) => value === undefined || value === null || value === "";
  const validateGuardedSchema = (model) => {
    const violations = [];
    for (const spec of model) {
      const declared = new Map();
      for (const row of indexRows.filter((row) => row.idx_table === spec.table)) {
        if (!declared.has(row.idx_name)) {
          declared.set(row.idx_name, []);
        }
        declared.get(row.idx_name).push(row);
      }
      const guarded = new Map(
        spec.identities.map((identity) => [identity.columns.join(","), identity]),
      );
      for (const [indexName, rows] of declared) {
        const ordered = [...rows].sort(
          (left, right) => Number(left.idx_position) - Number(right.idx_position),
        );
        const key = ordered.map((row) => row.idx_column).join(",");
        if (!guarded.has(key)) {
          violations.push({
            table: spec.table,
            index: indexName,
            kind: "unguarded_unique_identity",
            detail: `ordered columns ${key} are not covered by any guard`,
          });
          continue;
        }
        for (const row of ordered) {
          if (!isEmpty(row.idx_prefix)) {
            violations.push({ table: spec.table, index: indexName, kind: "prefix_index", detail: row.idx_column });
          }
          if (!isEmpty(row.idx_expression)) {
            violations.push({ table: spec.table, index: indexName, kind: "expression_index", detail: row.idx_column });
          }
          if (String(row.idx_nullable).toUpperCase() === "YES") {
            violations.push({ table: spec.table, index: indexName, kind: "nullable_index_column", detail: row.idx_column });
          }
          const fact = factFor(spec.table, row.idx_column);
          if (String(fact.fact_nullable).toUpperCase() !== "NO") {
            violations.push({ table: spec.table, index: indexName, kind: "nullable_column", detail: row.idx_column });
          }
          if (!isEmpty(fact.fact_collation) && !String(fact.fact_collation).endsWith("_bin")) {
            violations.push({
              table: spec.table,
              index: indexName,
              kind: "non_binary_collation",
              detail: `${row.idx_column} uses ${fact.fact_collation}, which can match values the binary ledger stores as distinct identities`,
            });
          }
          if (!["varchar", "char", "binary", "varbinary", "bigint", "int", "smallint", "tinyint"].includes(String(fact.fact_type))) {
            violations.push({ table: spec.table, index: indexName, kind: "unsupported_identity_type", detail: `${row.idx_column} is ${fact.fact_type}` });
          }
        }
      }
      for (const identity of spec.identities) {
        const key = identity.columns.join(",");
        const found = [...declared.values()].some(
          (rows) =>
            [...rows]
              .sort((left, right) => Number(left.idx_position) - Number(right.idx_position))
              .map((row) => row.idx_column)
              .join(",") === key,
        );
        if (!found) {
          violations.push({ table: spec.table, index: identity.name, kind: "guard_without_unique_index", detail: key });
        }
      }
      const budget = spec.identities.map(
        (identity) =>
          Buffer.byteLength(`${spec.table}.${identity.name}`, "utf8") +
          identity.columns.reduce(
            (total, column) => total + 1 + columnByteWidth(factFor(spec.table, column)),
            0,
          ),
      );
      if (Math.max(...budget) > ledgerIdentityWidth) {
        violations.push({
          table: spec.table,
          index: "ledger",
          kind: "identity_width_overflow",
          detail: `requires ${Math.max(...budget)} bytes but the ledger key is ${ledgerIdentityWidth}`,
        });
      }
    }
    return violations;
  };
  const guardedSchemaViolations = validateGuardedSchema(immutableTableModel);
  const driftModel = [
    {
      table: "schema_drift_probe",
      identities: [
        { name: "pk", columns: ["id"] },
        { name: "drift_key", columns: ["drift_key"] },
      ],
    },
  ];
  const driftViolations = validateGuardedSchema(driftModel);
  doltSuccessfully(
    appDoltArgs("INSERT INTO schema_drift_probe (drift_key, value) VALUES ('key-1', 'original')"),
  );
  const driftAttack = dolt(
    appDoltArgs(
      "INSERT INTO schema_drift_probe (drift_key, value) VALUES ('KEY-1', 'FORGED') ON DUPLICATE KEY UPDATE value = 'FORGED'",
    ),
    { tolerateTimeout: true, timeout: adversarialTimeoutMs },
  );
  const driftRow = firstRow(
    doltSuccessfully(ownerDoltArgs("SELECT value FROM schema_drift_probe WHERE drift_key = 'key-1'")),
  );
  assertObservation(
    guardedSchemaViolations.length === 0 &&
      driftViolations.some((violation) => violation.kind === "non_binary_collation") &&
      driftAttack.code === 0 &&
      driftRow.value === "FORGED",
    `schema-drift validation did not behave as required: production violations=${JSON.stringify(
      guardedSchemaViolations,
    )} drift violations=${JSON.stringify(driftViolations)} drift attack exit=${driftAttack.code} value=${driftRow.value}`,
  );
  record(
    "direct_dolt.schema_coverage_validation",
    "Direct Dolt",
    "PASS",
    `Coverage is validated on ordered index columns plus per-column type, octet width, NULL semantics, prefix/expression status, and collation: the production schema produced 0 violations, while a deliberately drifted table using a case-insensitive UNIQUE key was rejected with a non_binary_collation violation and its ODKU bypass reproduced (exit ${driftAttack.code}, value now ${driftRow.value}), proving column-set comparison alone is insufficient.`,
  );

  const maximumIdentifier = "\u{1F600}".repeat(128);
  const maximumAppend = dolt(
    appDoltArgs(
      `INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload) VALUES (${sqlLiteral(
        maximumIdentifier,
      )}, 'run-1', 501, 'task-1', 0, 'task.maximum', JSON_OBJECT('title', 'Maximum'))`,
    ),
  );
  const maximumRelaxed = dolt(
    appDoltArgs(
      `SET SESSION sql_mode=''; INSERT INTO events (event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload) VALUES (${sqlLiteral(
        `${maximumIdentifier.slice(0, 127)}\u{1F601}`,
      )}, 'run-1', 502, 'task-1', 0, 'task.maximum', JSON_OBJECT('title', 'Maximum'));`,
    ),
  );
  const widestIdentity = firstRow(
    doltSuccessfully(
      ownerDoltArgs("SELECT MAX(LENGTH(identity)) AS widest FROM events_identity"),
    ),
  );
  assertObservation(
    maximumAppend.code === 0 &&
      maximumRelaxed.code === 0 &&
      Number(widestIdentity.widest) <= ledgerIdentityWidth &&
      Number(widestIdentity.widest) > 512,
    `maximum-length multibyte identities were not stored intact: strict=${maximumAppend.code} relaxed=${maximumRelaxed.code} widest=${widestIdentity.widest} width=${ledgerIdentityWidth}`,
  );
  record(
    "direct_dolt.identity_width",
    "Direct Dolt",
    "PASS",
    `The ledger key width is derived from information_schema (${identityByteBudget} bytes required, ${ledgerIdentityWidth} provisioned), so 128-character four-byte identifiers append under both strict and relaxed session sql_mode and produced a ${widestIdentity.widest}-byte identity that a fixed VARBINARY(512) would have rejected or truncated.`,
  );

  const privilegeProbes = [];
  for (const [label, sql] of [
    ["read_user_table", "SELECT COUNT(*) AS n FROM mysql.user"],
    ["create_user", `CREATE USER 'escalated_${runId}'@'%' IDENTIFIED BY 'x'`],
    ["grant_self", `GRANT ALL ON \`${directDatabase}\`.* TO '${appUser}'@'%'`],
    ["grant_ledger_insert", `GRANT INSERT ON \`${directDatabase}\`.events_identity TO '${appUser}'@'%'`],
    ["drop_owner", `DROP USER '${ownerUser}'@'%'`],
    ["set_password_owner", `SET PASSWORD FOR '${ownerUser}'@'%' = 'x'`],
    ["alter_user_current_user", `ALTER USER CURRENT_USER() IDENTIFIED BY 'rotated-${runId}'`],
    ["alter_owner", `ALTER USER '${ownerUser}'@'%' IDENTIFIED BY 'rotated-${runId}'`],
  ]) {
    const result = dolt(appDoltArgs(sql), { tolerateTimeout: true, timeout: adversarialTimeoutMs });
    assertObservation(
      result.code !== 0,
      `application identity was allowed to ${label}: ${(result.stderr || result.stdout).trim()}`,
    );
    privilegeProbes.push({
      operation: label,
      exit_code: result.code,
      denied: true,
      diagnostic: (result.stderr || result.stdout).trim().split("\n").slice(-1)[0].slice(-140),
    });
  }
  const rotatedPassword = `${appPassword}-rotated`;
  secretValues.push(rotatedPassword);
  const rotatedAppEnv = { ...appDoltEnv, DOLT_CLI_PASSWORD: rotatedPassword };
  const runCredentialStatement = async (sessionEnv, sql) => {
    const session = openAppSqlSession(sessionEnv);
    session.child.stdin.end(`${sql};\n`);
    return session.closed;
  };
  const selfRotation = await runCredentialStatement(
    appDoltEnv,
    `ALTER USER '${appUser}'@'%' IDENTIFIED BY '${rotatedPassword}'`,
  );
  const oldPasswordAfterRotation = dolt(appDoltArgs("SELECT 1 AS ok"), {
    tolerateTimeout: true,
    timeout: adversarialTimeoutMs,
  });
  const newPasswordWorks = dolt(
    { argv: remoteDoltArgs("SELECT 1 AS ok"), env: rotatedAppEnv },
    { tolerateTimeout: true, timeout: adversarialTimeoutMs },
  );
  const privilegesAfterRotation = dolt(
    {
      argv: remoteDoltArgs("INSERT INTO events_identity (identity) VALUES ('post.rotation.probe')"),
      env: rotatedAppEnv,
    },
    { tolerateTimeout: true, timeout: adversarialTimeoutMs },
  );
  const restoreRotation = await runCredentialStatement(
    rotatedAppEnv,
    `ALTER USER '${appUser}'@'%' IDENTIFIED BY '${appPassword}'`,
  );
  const selfRotationEvidence = {
    permitted: selfRotation.code === 0,
    old_password_rejected_after_rotation: oldPasswordAfterRotation.code !== 0,
    new_password_accepted: newPasswordWorks.code === 0,
    privileges_unchanged_after_rotation: privilegesAfterRotation.code !== 0,
    original_password_restored: restoreRotation.code === 0,
  };
  assertObservation(
    selfRotationEvidence.permitted &&
      selfRotationEvidence.old_password_rejected_after_rotation &&
      selfRotationEvidence.new_password_accepted &&
      selfRotationEvidence.privileges_unchanged_after_rotation &&
      selfRotationEvidence.original_password_restored,
    `self password rotation behaved unexpectedly: ${JSON.stringify(selfRotationEvidence)}`,
  );
  const appStillAuthenticates = dolt(appDoltArgs("SELECT 1 AS ok"));
  const ownerStillAuthenticates = dolt(ownerDoltArgs("SELECT 1 AS ok"));
  const ledgerStillProtected = dolt(
    appDoltArgs("INSERT INTO events_identity (identity) VALUES ('post.privilege.probe')"),
    { tolerateTimeout: true, timeout: adversarialTimeoutMs },
  );
  assertObservation(
    appStillAuthenticates.code === 0 &&
      ownerStillAuthenticates.code === 0 &&
      ledgerStillProtected.code !== 0,
    `privilege probes changed authentication or the ledger boundary: app=${appStillAuthenticates.code} owner=${ownerStillAuthenticates.code} ledger=${ledgerStillProtected.code}`,
  );
  record(
    "direct_dolt.privilege_boundary",
    "Direct Dolt",
    "PASS",
    `All ${privilegeProbes.length} privilege-expansion operations were denied to the application identity, including reading mysql.user, creating users, granting itself any privilege, granting itself ledger INSERT, ALTER USER CURRENT_USER(), and altering or resetting the owner. The identity can rename its own credential with an explicit ALTER USER on itself: that rotation was permitted, invalidated its previous password, granted no additional privilege (the ledger write stayed denied), left the owner unaffected, and was reversed by the fixture, so it is an availability and recovery concern rather than a privilege boundary failure.`,
  );

  boundaryEvidence = {
    ledger_privilege: "SELECT only; the application identity holds no write privilege on any identity ledger, on parent_guard, on guard_constants, or on immutable_write_guard",
    identity_byte_budget: identityByteBudget,
    ledger_identity_width: ledgerIdentityWidth,
    widest_stored_identity_bytes: Number(widestIdentity.widest),
    ledger_attacks: ledgerAttackResults,
    two_step_attacks: twoStepResults,
    transaction_wrapped_attacks: transactionAttackResults,
    blocked_without_returning: [
      ...statementResults.filter((entry) => entry.blocked_without_returning),
      ...ledgerAttackResults.filter((entry) => entry.blocked_without_returning),
    ].map((entry) => ({
      target: entry.table ?? entry.ledger,
      statement: entry.statement,
      identity: entry.identity,
    })),
    post_transaction_ledger_probe: {
      exit_code: lockRetentionProbe.code,
      blocked_without_returning: Boolean(lockRetentionProbe.timedOut),
    },
    declared_foreign_keys: declaredForeignKeys.map(foreignKeyKey),
    guarded_foreign_keys: foreignKeyModel.map(foreignKeyKey),
    referenced_parent_identity_evidence: referencedParentIdentityEvidence,
    referential_probes: referentialResults,
    orphan_rows_after_probes: orphanRows,
    valid_append_with_foreign_key_checks_disabled: validWithFkDisabled.code,
    concurrent_parent_child: {
      child_before_parent_commit_exit_code: childBeforeParentCommit.code,
      parent_commit_exit_code: parentCommit.code,
      child_after_parent_commit_exit_code: childAfterParentCommit.code,
    },
    rollback_state: rollbackState,
    replay_after_rollback_exit_code: replayAfterRollback.code,
    schema_validation: {
      production_violations: guardedSchemaViolations,
      drift_probe_violations: driftViolations,
      drift_probe_odku_exit_code: driftAttack.code,
      drift_probe_value_after_attack: driftRow.value,
    },
    privilege_probes: privilegeProbes,
    self_credential_rotation: selfRotationEvidence,
  };

  assertCredentialTransport("immutable-statement adversarial phase");

  const contentionKeys = `${sqlString(winnerCommand.key)}, ${sqlString(loserCommand.key)}`;
  const finalContentionCounts = firstRow(
    doltSuccessfully(
      appDoltArgs(`
        SELECT
          (SELECT version FROM aggregates WHERE id = 'task-contention') AS version,
          (SELECT COUNT(*) FROM command_requests WHERE idempotency_key IN (${contentionKeys})) AS requests,
          (SELECT COUNT(*) FROM command_outcomes WHERE idempotency_key IN (${contentionKeys})) AS outcomes,
          (SELECT SUM(outcome_type = 'applied') FROM command_outcomes
            WHERE idempotency_key IN (${contentionKeys})) AS applied,
          (SELECT SUM(outcome_type = 'conflict') FROM command_outcomes
            WHERE idempotency_key IN (${contentionKeys})) AS conflicts,
          (SELECT COUNT(*) FROM events WHERE aggregate_id = 'task-contention') AS events,
          (SELECT COUNT(*) FROM audit_entries a JOIN events e USING (event_id)
            WHERE e.aggregate_id = 'task-contention') AS audits
      `),
    ),
  );
  assertObservation(
    finalContentionCounts.version === "1" &&
      finalContentionCounts.requests === "2" &&
      finalContentionCounts.outcomes === "2" &&
      finalContentionCounts.applied === "1" &&
      finalContentionCounts.conflicts === "1" &&
      finalContentionCounts.events === "1" &&
      finalContentionCounts.audits === "1",
    `count invariants after the adversarial matrix changed: ${JSON.stringify(finalContentionCounts)}`,
  );
  const replayAfterMatrix = replayCommand(winnerCommand);
  const loserReplayAfterMatrix = replayCommand(loserCommand);
  let mismatchAfterMatrix;
  try {
    replayCommand(mismatchedReplay);
  } catch (error) {
    mismatchAfterMatrix = error;
  }
  assertObservation(
    replayAfterMatrix.outcome === "applied" &&
      replayAfterMatrix.observedVersion === 1 &&
      loserReplayAfterMatrix.outcome === "conflict" &&
      mismatchAfterMatrix instanceof CommandPayloadMismatchError,
    "replay classification changed after the adversarial matrix",
  );
  contentionEvidence = {
    winner_key: winnerCommand.key,
    loser_key: loserCommand.key,
    winner_exit_code: winnerResult.code,
    loser_exit_code: loserResult.code,
    loser_sqlstate: "40001",
    loser_error_code: 1213,
    loser_transaction_partial_rows_before_recovery: 0,
    final: {
      aggregate_version: 1,
      requests: 2,
      outcomes: 2,
      applied: 1,
      conflicts: 1,
      tampered: 0,
      events: 1,
      audits: 1,
      immutable_values_preserved: true,
    },
  };
  record(
    "direct_dolt.adapter_replay_classification",
    "Direct Dolt",
    "PASS",
    "Exact winner and conflict replays returned the preserved stored outcome and a different canonical request raised IDEMPOTENCY_PAYLOAD_MISMATCH without writing, both before and after the complete adversarial statement matrix.",
  );

  assertCredentialTransport("before cleanup");
  await cleanup();
  assertObservation(
    !existsSync(tempRoot) &&
      serverTermination &&
      (serverTermination.exit_code !== null || serverTermination.signal !== null),
    "owned server termination was not proven before storage removal",
  );
  record(
    "spike.cleanup",
    "Harness",
    "PASS",
    `Spawned PID ${serverTermination.pid} exited (${serverTermination.signal || serverTermination.exit_code}) before its owned disposable repository, databases, credential-free client config, and server configuration were removed.`,
  );

  const report = {
    schema_version: 6,
    environment: {
      run_id: runId,
      bd: bdVersion,
      dolt: doltVersion,
      operating_system: uname,
      topology: `isolated authenticated loopback TCP server on ephemeral port ${port}; unique Beads and direct-Dolt databases; independent CLI client processes`,
      server: {
        spawned_pid: serverTermination.pid,
        identity_verified: serverIdentityVerified,
        unauthenticated_root_denied: true,
        termination: serverTermination.signal || serverTermination.exit_code,
      },
      databases: {
        beads: beadsDatabase,
        direct_dolt: directDatabase,
      },
      credentials: "per-run random values passed only through supported process environment variables; no credential profile, argv, or output",
      temp_directory: {
        basename: tempRootBasename,
        removed: true,
      },
    },
    checks,
    contention_evidence: contentionEvidence,
    immutability_evidence: immutabilityEvidence,
    boundary_evidence: boundaryEvidence,
    credential_transport_checks: credentialTransportChecks,
    decision_evidence: {
      beads: "NO-GO",
      direct_dolt: "GO",
      selected_contingency: "DIRECT_DOLT_WITH_BEFORE_INSERT_IDENTITY_GUARDS",
    },
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
}

process.on("SIGUSR1", () => {
  try {
    assertObservation(serverIdentityVerified && isRunning(serverProcess), "server is not ready");
    const identity = firstRow(
      doltSuccessfully(
        ownerDoltArgs("SELECT run_id, identity_proof FROM harness_identity WHERE singleton = 1"),
      ),
    );
    assertObservation(
      identity.run_id === runId && identity.identity_proof === identityProof,
      "server identity changed",
    );
    assertCredentialTransport("external identity revalidation");
    process.stderr.write(
      `DIRECTOR_M04_REVERIFIED ${JSON.stringify({
        run_id: runId,
        server_pid: serverProcess.pid,
        server_port: serverPort,
      })}\n`,
    );
  } catch (error) {
    process.stderr.write(
      `DIRECTOR_M04_REVERIFY_FAILED ${error instanceof Error ? error.message : String(error)}\n`,
    );
  }
});

const signalExitCodes = { SIGINT: 130, SIGTERM: 143 };
for (const signal of Object.keys(signalExitCodes)) {
  process.once(signal, async () => {
    interruptedSignal = signal;
    try {
      await cleanup();
      process.exit(signalExitCodes[signal]);
    } catch (error) {
      process.stderr.write(
        `DIRECTOR_M04_FAILURE ${JSON.stringify({
          run_id: runId,
          server_pid: serverProcess?.pid ?? null,
          server_port: serverPort ?? null,
          temp_dir_basename: tempRootBasename,
        })}\n${error instanceof Error ? error.stack : String(error)}\n`,
      );
      process.exit(1);
    }
  });
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
  if (!interruptedSignal) {
    process.stderr.write(
      `DIRECTOR_M04_FAILURE ${JSON.stringify({
        run_id: runId,
        server_pid: serverProcess?.pid ?? null,
        server_port: serverPort ?? null,
        temp_dir_basename: tempRootBasename,
      })}\n${error instanceof Error ? error.stack : String(error)}\n${
        cleanupError ? `cleanup failure: ${String(cleanupError)}\n` : ""
      }`,
    );
    process.exitCode = 1;
  }
}

// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const corpusURL = new URL(
  "../../engine/domain/execution/testdata/real-incident-corpus.v1.json",
  import.meta.url,
);
const raw = readFileSync(corpusURL, "utf8");
const corpus = JSON.parse(raw);

assert.equal(corpus.schemaVersion, "director.real-incident-corpus/v1");
assert.equal(corpus.source.completeness, "partial");
assert.ok(corpus.source.obstacleCode.startsWith("DIR-M5.13-GAP-"));
assert.ok(Array.isArray(corpus.fixtures) && corpus.fixtures.length > 0);

const forbidden = [
  ["agent UUID", /\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b/iu],
  ["absolute home path", /(?:^|[\s"'])\/(?:home|Users)\//u],
  ["worktree path", /(?:^|[\s"'])\/[\w./-]*\.paseo\/worktrees\//u],
  ["Paseo workspace identifier", /\bwks_[a-z0-9]+\b/iu],
  ["IP address", /\b(?:\d{1,3}\.){3}\d{1,3}\b/u],
  ["host name", /\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:com|net|org|io|dev|local|internal|lan|cloud)\b/iu],
  ["email address", /\b[^\s@]+@[^\s@]+\.[^\s@]+\b/u],
  ["authorization header", /\b(?:authorization|proxy-authorization)\s*:/iu],
  ["bearer token", /\bbearer\s+[a-z0-9._~+/-]+=*\b/iu],
  ["known secret key", /\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)\s*[:=]/iu],
];

for (const [name, pattern] of forbidden) {
  assert.equal(pattern.test(raw), false, `${name} survived corpus redaction`);
}

const placeholder = /^<[A-Z][A-Z0-9_]*>$/u;
const fixtureIDs = new Set();
const occurrenceTotals = { provider_failure: 0, permission_stall: 0, taskstore_outage: 0 };
let exercisedOccurrences = 0;

for (const fixture of corpus.fixtures) {
  assert.match(fixture.id, /^DIR-M5\.13-INCIDENT-[A-Z0-9-]+$/u);
  assert.equal(fixtureIDs.has(fixture.id), false, `duplicate fixture ${fixture.id}`);
  fixtureIDs.add(fixture.id);
  assert.ok(Object.hasOwn(occurrenceTotals, fixture.kind), `unknown kind ${fixture.kind}`);
  assert.ok(Number.isSafeInteger(fixture.occurrenceCount) && fixture.occurrenceCount > 0);
  occurrenceTotals[fixture.kind] += fixture.occurrenceCount;

  assert.deepEqual(Object.keys(fixture.identities).sort(), [
    "agent", "coordinator", "host", "session", "workspace", "worktree",
  ]);
  for (const [field, value] of Object.entries(fixture.identities)) {
    assert.match(value, placeholder, `${fixture.id} ${field} is not a stable placeholder`);
  }

  assert.ok(["whole", "partial", "missing"].includes(fixture.trace.completeness));
  if (fixture.trace.completeness === "whole") {
    assert.ok(typeof fixture.trace.providerText === "string" && fixture.trace.providerText.length > 0);
    assert.equal(fixture.trace.missingTrace, "");
  } else {
    assert.equal(fixture.trace.providerText, null, `${fixture.id} must not present a fragment as whole text`);
    assert.ok(typeof fixture.trace.missingTrace === "string" && fixture.trace.missingTrace.length > 0);
  }

  if (fixture.classifierProbe) {
    exercisedOccurrences += fixture.occurrenceCount;
    assert.ok(typeof fixture.classifierProbe.lastError === "string");
    assert.ok(Array.isArray(fixture.classifierProbe.expectedConnectorSignals));
    assert.ok(fixture.classifierProbe.expectedConnectorSignals.length > 0);
    assert.ok(typeof fixture.classifierProbe.expectedEngineClass === "string");
    assert.ok(fixture.recoveryScenarios.length > 0);
  } else {
    assert.equal(fixture.recoveryScenarios.length, 0);
    assert.equal(fixture.trace.completeness, "missing");
  }

  assert.ok(typeof fixture.coordinator.disposition === "string" && fixture.coordinator.disposition.length > 0);
  assert.ok(typeof fixture.coordinator.observedResult === "string" && fixture.coordinator.observedResult.length > 0);
  assert.ok(Array.isArray(fixture.findingCodes) && fixture.findingCodes.length > 0);
}

assert.deepEqual(occurrenceTotals, {
  provider_failure: corpus.reportedCoverage.providerFailures,
  permission_stall: corpus.reportedCoverage.permissionStalls,
  taskstore_outage: corpus.reportedCoverage.taskStoreOutages,
});

const findingCodes = new Set(corpus.divergenceFindings.map((finding) => finding.code));
assert.equal(findingCodes.size, corpus.divergenceFindings.length);
for (const finding of corpus.divergenceFindings) {
  assert.match(finding.code, /^DIR-M5\.13-F\d{3}$/u);
  assert.equal(typeof finding.behaviorChanged, "boolean");
  assert.equal(
    finding.behaviorChanged,
    finding.code === "DIR-M5.13-F001",
    `${finding.code} behaviorChanged expected to be ${finding.code === "DIR-M5.13-F001"}`,
  );
  assert.ok(finding.incidentIds.length > 0);
  for (const incidentID of finding.incidentIds) {
    assert.ok(fixtureIDs.has(incidentID), `${finding.code} cites unknown fixture ${incidentID}`);
  }
}

console.log(JSON.stringify({
  schemaVersion: corpus.schemaVersion,
  redaction: "verified",
  fixtureCount: corpus.fixtures.length,
  reportedOccurrences: occurrenceTotals,
  classifierProbeOccurrences: exercisedOccurrences,
  divergenceFindings: [...findingCodes].sort(),
  sourceFile: fileURLToPath(corpusURL).split("/").at(-1),
}));

// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import test from "node:test";

import {
  BOARD_MAXIMUM_BYTES,
  BOARD_QUERY_PATH,
  HOST_CONTRACT_SHA256,
  HOST_CONTRACT_VERSION,
  type BoardSnapshot,
} from "../generated/host-contract.shared.ts";
import {
  BoardTransportError,
  createBoardTransport,
} from "../connector/engine-board.server.ts";

const snapshot: BoardSnapshot = {
  schemaVersion: 1,
  cursor: "3",
  tasks: [{
    id: "task-1",
    projectId: "project-1",
    projectName: "Director",
    title: "Render tasks",
    state: "queued",
    runNumber: null,
    candidateSha: null,
  }],
};

function responseAt(url: string, body: string, init: ResponseInit): Response {
  const response = new Response(body, init);
  Object.defineProperty(response, "url", { value: url });
  return response;
}

async function listen(server: Server): Promise<number> {
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("missing server address");
  return address.port;
}

async function close(server: Server): Promise<void> {
  await new Promise<void>((resolve, reject) =>
    server.close((error) => (error ? reject(error) : resolve())),
  );
}

test("the connector reads and validates the engine-owned Board contract once", async () => {
  let calls = 0;
  const transport = createBoardTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async (input, init) => {
      calls += 1;
      assert.equal(String(input), `http://127.0.0.1:7041${BOARD_QUERY_PATH}`);
      assert.equal(init?.method, "GET");
      assert.equal(init?.redirect, "error");
      assert.equal((init?.headers as Record<string, string>)["x-director-contract-version"], HOST_CONTRACT_VERSION);
      assert.equal((init?.headers as Record<string, string>)["x-director-contract-hash"], HOST_CONTRACT_SHA256);
      return responseAt(`http://127.0.0.1:7041${BOARD_QUERY_PATH}`, JSON.stringify(snapshot), {
        status: 200,
        headers: {
          "content-type": "application/json",
          "x-director-contract-version": HOST_CONTRACT_VERSION,
          "x-director-contract-hash": HOST_CONTRACT_SHA256,
        },
      });
    },
  });

  assert.deepEqual(await transport.load(), snapshot);
  assert.equal(calls, 1);
});

test("the connector rejects non-loopback engines, drift, and malformed snapshots", async () => {
  for (const baseUrl of [
    "https://127.0.0.1:7041",
    "http://example.com:7041",
    "http://127.0.0.1:7041/path",
    "http://user:pass@127.0.0.1:7041",
  ]) {
    assert.throws(
      () => createBoardTransport({ baseUrl }),
      (error: unknown) => error instanceof BoardTransportError,
      baseUrl,
    );
  }

  for (const response of [
    responseAt(`http://[::1]:7041${BOARD_QUERY_PATH}`, JSON.stringify(snapshot), {
      status: 200,
      headers: {
        "x-director-contract-version": HOST_CONTRACT_VERSION,
        "x-director-contract-hash": "0".repeat(64),
      },
    }),
    responseAt(`http://[::1]:7041${BOARD_QUERY_PATH}`, JSON.stringify({ ...snapshot, extra: true }), {
      status: 200,
      headers: {
        "x-director-contract-version": HOST_CONTRACT_VERSION,
        "x-director-contract-hash": HOST_CONTRACT_SHA256,
      },
    }),
    responseAt(`http://[::1]:7041${BOARD_QUERY_PATH}`, "x".repeat(BOARD_MAXIMUM_BYTES + 1), {
      status: 200,
      headers: {
        "x-director-contract-version": HOST_CONTRACT_VERSION,
        "x-director-contract-hash": HOST_CONTRACT_SHA256,
      },
    }),
  ]) {
    let calls = 0;
    const transport = createBoardTransport({
      baseUrl: "http://[::1]:7041",
      fetch: async () => {
        calls += 1;
        return response;
      },
    });
    await assert.rejects(transport.load(), BoardTransportError);
    assert.equal(calls, 1, "read transport must not own retry policy");
  }
});

test("the connector refuses redirects before another origin receives a request", async () => {
  let offOriginRequests = 0;
  const destination = createServer((_request, response) => {
    offOriginRequests += 1;
    response.end(JSON.stringify(snapshot));
  });
  const destinationPort = await listen(destination);
  const redirector = createServer((_request, response) => {
    response.writeHead(302, {
      location: `http://127.0.0.1:${destinationPort}${BOARD_QUERY_PATH}`,
    });
    response.end();
  });
  const redirectPort = await listen(redirector);
  try {
    const transport = createBoardTransport({
      baseUrl: `http://127.0.0.1:${redirectPort}`,
    });
    await assert.rejects(transport.load(), BoardTransportError);
    assert.equal(offOriginRequests, 0);
  } finally {
    await close(redirector);
    await close(destination);
  }
});

test("the connector rejects a response whose final URL differs from the request", async () => {
  const transport = createBoardTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async () =>
      responseAt("http://127.0.0.2:7041/v1/board", JSON.stringify(snapshot), {
        status: 200,
        headers: {
          "x-director-contract-version": HOST_CONTRACT_VERSION,
          "x-director-contract-hash": HOST_CONTRACT_SHA256,
        },
      }),
  });
  await assert.rejects(
    transport.load(),
    (error: unknown) =>
      error instanceof BoardTransportError &&
      error.code === "ENGINE_BOARD_ORIGIN",
  );
});

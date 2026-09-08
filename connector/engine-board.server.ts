// SPDX-License-Identifier: Apache-2.0
// Policy-free same-host transport for the engine-owned Board query.

import { isIP } from "node:net";

import {
  assertBoardSnapshot,
  BOARD_MAXIMUM_BYTES,
  BOARD_QUERY_METHOD,
  BOARD_QUERY_PATH,
  HOST_CONTRACT_SHA256,
  HOST_CONTRACT_VERSION,
  type BoardSnapshot,
} from "../generated/host-contract.shared.ts";

const requestTimeoutMilliseconds = 10_000;

export type BoardTransport = {
  load(): Promise<BoardSnapshot>;
};

export type BoardFetch = (
  input: string | URL,
  init?: RequestInit,
) => Promise<Response>;

export class BoardTransportError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "BoardTransportError";
    this.code = code;
  }
}

async function boundedResponseValue(response: Response): Promise<unknown> {
  const declaredLength = response.headers.get("content-length");
  if (
    declaredLength !== null &&
    (!/^(?:0|[1-9][0-9]*)$/.test(declaredLength) ||
      Number(declaredLength) > BOARD_MAXIMUM_BYTES)
  ) {
    throw new BoardTransportError(
      "ENGINE_BOARD_PAYLOAD",
      "Director Engine Board response is invalid",
    );
  }
  if (!response.body) {
    throw new BoardTransportError(
      "ENGINE_BOARD_PAYLOAD",
      "Director Engine Board response is invalid",
    );
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    length += value.byteLength;
    if (length > BOARD_MAXIMUM_BYTES) {
      await reader.cancel();
      throw new BoardTransportError(
        "ENGINE_BOARD_PAYLOAD",
        "Director Engine Board response is invalid",
      );
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  return JSON.parse(text) as unknown;
}

function loopbackBaseUrl(value: string | undefined): URL {
  let parsed: URL;
  try {
    parsed = new URL(value ?? "");
  } catch {
    throw new BoardTransportError(
      "ENGINE_BOARD_URL",
      "DIRECTOR_ENGINE_URL must be an absolute loopback HTTP URL",
    );
  }
  const hostname = parsed.hostname.startsWith("[")
    ? parsed.hostname.slice(1, -1)
    : parsed.hostname;
  const loopback =
    (isIP(hostname) === 4 && hostname.startsWith("127.")) || hostname === "::1";
  if (
    parsed.protocol !== "http:" ||
    !loopback ||
    parsed.port === "" ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.pathname !== "/" ||
    parsed.search !== "" ||
    parsed.hash !== ""
  ) {
    throw new BoardTransportError(
      "ENGINE_BOARD_URL",
      "DIRECTOR_ENGINE_URL must be an origin-only loopback HTTP URL with an explicit port",
    );
  }
  return parsed;
}

export function createBoardTransport(options: {
  baseUrl: string | undefined;
  fetch?: BoardFetch;
}): BoardTransport {
  const baseUrl = loopbackBaseUrl(options.baseUrl);
  const fetchBoard = options.fetch ?? globalThis.fetch;
  const url = new URL(BOARD_QUERY_PATH, baseUrl);
  return {
    async load(): Promise<BoardSnapshot> {
      let response: Response;
      try {
        response = await fetchBoard(url, {
          method: BOARD_QUERY_METHOD,
          redirect: "error",
          headers: {
            "x-director-contract-version": HOST_CONTRACT_VERSION,
            "x-director-contract-hash": HOST_CONTRACT_SHA256,
          },
          signal: AbortSignal.timeout(requestTimeoutMilliseconds),
        });
      } catch {
        throw new BoardTransportError(
          "ENGINE_BOARD_UNAVAILABLE",
          "Director Engine Board query is unavailable",
        );
      }
      if (response.url !== url.href) {
        throw new BoardTransportError(
          "ENGINE_BOARD_ORIGIN",
          "Director Engine Board response origin does not match",
        );
      }
      if (!response.ok) {
        throw new BoardTransportError(
          "ENGINE_BOARD_RESPONSE",
          "Director Engine rejected the Board query",
        );
      }
      if (
        response.headers.get("x-director-contract-version") !==
          HOST_CONTRACT_VERSION ||
        response.headers.get("x-director-contract-hash") !==
          HOST_CONTRACT_SHA256
      ) {
        throw new BoardTransportError(
          "ENGINE_BOARD_CONTRACT",
          "Director Engine Board contract does not match",
        );
      }
      let body: unknown;
      try {
        body = await boundedResponseValue(response);
        return assertBoardSnapshot(body);
      } catch {
        throw new BoardTransportError(
          "ENGINE_BOARD_PAYLOAD",
          "Director Engine Board response is invalid",
        );
      }
    },
  };
}

// SPDX-License-Identifier: Apache-2.0
// Policy-free same-host transport for the engine-owned planning query.

import { isIP } from "node:net";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_MAXIMUM_REQUEST_BYTES,
  PLANNING_MAXIMUM_RESPONSE_BYTES,
  PLANNING_QUERY_PATH,
  planningQueryInputSchema,
  planningSnapshotSchema,
  type PlanningQueryInput,
  type PlanningSnapshot,
} from "../generated/planning-contract.shared.ts";

const requestTimeoutMilliseconds = 10_000;

export type PlanningTransport = {
  query(input: PlanningQueryInput): Promise<PlanningSnapshot>;
};

export type PlanningFetch = (
  input: string | URL,
  init?: RequestInit,
) => Promise<Response>;

export class PlanningTransportError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "PlanningTransportError";
    this.code = code;
  }
}

function loopbackBaseUrl(value: string | undefined): URL {
  let parsed: URL;
  try {
    parsed = new URL(value ?? "");
  } catch {
    throw new PlanningTransportError(
      "ENGINE_PLANNING_URL",
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
    throw new PlanningTransportError(
      "ENGINE_PLANNING_URL",
      "DIRECTOR_ENGINE_URL must be an origin-only loopback HTTP URL with an explicit port",
    );
  }
  return parsed;
}

async function boundedResponseValue(response: Response): Promise<unknown> {
  const declaredLength = response.headers.get("content-length");
  if (
    declaredLength !== null &&
    (!/^(?:0|[1-9][0-9]*)$/.test(declaredLength) ||
      Number(declaredLength) > PLANNING_MAXIMUM_RESPONSE_BYTES)
  ) {
    throw new PlanningTransportError(
      "ENGINE_PLANNING_PAYLOAD",
      "Director Engine planning response is invalid",
    );
  }
  if (!response.body) {
    throw new PlanningTransportError(
      "ENGINE_PLANNING_PAYLOAD",
      "Director Engine planning response is invalid",
    );
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    length += value.byteLength;
    if (length > PLANNING_MAXIMUM_RESPONSE_BYTES) {
      await reader.cancel();
      throw new PlanningTransportError(
        "ENGINE_PLANNING_PAYLOAD",
        "Director Engine planning response is invalid",
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

export function createPlanningTransport(options: {
  baseUrl: string | undefined;
  fetch?: PlanningFetch;
}): PlanningTransport {
  const baseUrl = loopbackBaseUrl(options.baseUrl);
  const fetchPlanning = options.fetch ?? globalThis.fetch;
  const url = new URL(PLANNING_QUERY_PATH, baseUrl);
  return {
    async query(rawInput: PlanningQueryInput): Promise<PlanningSnapshot> {
      const input = planningQueryInputSchema.parse(rawInput);
      const body = JSON.stringify(input);
      if (
        new TextEncoder().encode(body).byteLength >
          PLANNING_MAXIMUM_REQUEST_BYTES
      ) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_INPUT",
          "Director Engine planning query exceeds the contract bound",
        );
      }
      let response: Response;
      try {
        response = await fetchPlanning(url, {
          method: "POST",
          redirect: "error",
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": PLANNING_CONTRACT_VERSION,
            "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
          },
          body,
          signal: AbortSignal.timeout(requestTimeoutMilliseconds),
        });
      } catch {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_UNAVAILABLE",
          "Director Engine planning query is unavailable",
        );
      }
      if (response.url !== url.href) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_ORIGIN",
          "Director Engine planning response origin does not match",
        );
      }
      if (!response.ok) {
        let responseCode: string | undefined;
        try {
          const body = await boundedResponseValue(response);
          if (
            body !== null &&
            typeof body === "object" &&
            !Array.isArray(body) &&
            Object.keys(body).length === 1 &&
            typeof (body as { code?: unknown }).code === "string"
          ) {
            responseCode = (body as { code: string }).code;
          }
        } catch {
          responseCode = undefined;
        }
        const cursorInvalidated =
          response.status === 409 &&
          responseCode === "PLANNING_CURSOR_INVALIDATED";
        throw new PlanningTransportError(
          cursorInvalidated
            ? "ENGINE_PLANNING_CURSOR_INVALIDATED"
            : "ENGINE_PLANNING_RESPONSE",
          cursorInvalidated
            ? "Director Engine planning snapshot changed; refresh from the first page"
            : "Director Engine rejected the planning query",
        );
      }
      if (
        response.headers.get("x-director-contract-version") !==
          PLANNING_CONTRACT_VERSION ||
        response.headers.get("x-director-contract-hash") !==
          PLANNING_CONTRACT_SHA256
      ) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_CONTRACT",
          "Director Engine planning contract does not match",
        );
      }
      try {
        return planningSnapshotSchema.parse(await boundedResponseValue(response));
      } catch {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_PAYLOAD",
          "Director Engine planning response is invalid",
        );
      }
    },
  };
}

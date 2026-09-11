// SPDX-License-Identifier: Apache-2.0
// Policy-free same-host transport for the engine-owned planning query.

import { isIP } from "node:net";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_MAXIMUM_REQUEST_BYTES,
  PLANNING_MAXIMUM_RESPONSE_BYTES,
  PLANNING_MUTATION_ACTOR_HEADERS,
  PLANNING_MUTATION_PATH,
  PLANNING_HOME_QUERY_PATH,
  PLANNING_ORGANIZER_MUTATION_PATH,
  PLANNING_QUERY_PATH,
  PLANNING_TASK_DETAIL_QUERY_PATH,
  planningMutationInputSchema,
  planningMutationResultSchema,
  planningQueryInputSchema,
  planningSnapshotSchema,
  taskDetailQueryInputSchema,
  taskDetailSnapshotSchema,
  homeQueryInputSchema,
  homeSnapshotSchema,
  organizerBootstrapInputSchema,
  organizerBootstrapResultSchema,
  type HomeQueryInput,
  type HomeSnapshot,
  type OrganizerBootstrapInput,
  type OrganizerBootstrapResult,
  type PlanningQueryInput,
  type PlanningSnapshot,
  type TaskDetailQueryInput,
  type TaskDetailSnapshot,
  type PlanningMutationInput,
  type PlanningMutationResult,
} from "../generated/planning-contract.shared.ts";

const requestTimeoutMilliseconds = 10_000;

export type PlanningTransport = {
  query(input: PlanningQueryInput): Promise<PlanningSnapshot>;
  taskDetail(input: TaskDetailQueryInput): Promise<TaskDetailSnapshot>;
  home?(input: HomeQueryInput): Promise<HomeSnapshot>;
  bootstrapOrganizer?(input: OrganizerBootstrapInput): Promise<OrganizerBootstrapResult>;
  mutate(input: PlanningMutationInput): Promise<PlanningMutationResult>;
};

export type PlanningFetch = (
  input: string | URL,
  init?: RequestInit,
) => Promise<Response>;

export type PlanningMutationActor = {
  readonly kind: "human";
  readonly id: string;
  readonly sessionId: string;
};

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
  mutationActor?: PlanningMutationActor;
}): PlanningTransport {
  const baseUrl = loopbackBaseUrl(options.baseUrl);
  const fetchPlanning = options.fetch ?? globalThis.fetch;
  const url = new URL(PLANNING_QUERY_PATH, baseUrl);
  const taskDetailUrl = new URL(PLANNING_TASK_DETAIL_QUERY_PATH, baseUrl);
  const homeUrl = new URL(PLANNING_HOME_QUERY_PATH, baseUrl);
  const organizerUrl = new URL(PLANNING_ORGANIZER_MUTATION_PATH, baseUrl);
  const mutationUrl = new URL(PLANNING_MUTATION_PATH, baseUrl);
  return {
    async bootstrapOrganizer(rawInput: OrganizerBootstrapInput): Promise<OrganizerBootstrapResult> {
      const actor = options.mutationActor;
      const identityPattern = /^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$/u;
      if (!actor || actor.kind !== "human" || !identityPattern.test(actor.id) || !identityPattern.test(actor.sessionId)) {
        throw new PlanningTransportError("ENGINE_ORGANIZER_ACTOR", "Organizer Preview/Apply requires a server-authenticated human session");
      }
      const input = organizerBootstrapInputSchema.parse(rawInput);
      const body = JSON.stringify(input);
      if (new TextEncoder().encode(body).byteLength > PLANNING_MAXIMUM_REQUEST_BYTES) {
        throw new PlanningTransportError("ENGINE_ORGANIZER_INPUT", "Organizer Preview/Apply exceeds the contract bound");
      }
      let response: Response;
      try {
        response = await fetchPlanning(organizerUrl, {
          method: "POST",
          redirect: "error",
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": PLANNING_CONTRACT_VERSION,
            "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
            [PLANNING_MUTATION_ACTOR_HEADERS.kind]: actor.kind,
            [PLANNING_MUTATION_ACTOR_HEADERS.id]: actor.id,
            [PLANNING_MUTATION_ACTOR_HEADERS.session]: actor.sessionId,
          },
          body,
          signal: AbortSignal.timeout(requestTimeoutMilliseconds),
        });
      } catch {
        throw new PlanningTransportError("ENGINE_ORGANIZER_UNAVAILABLE", "Organizer Preview/Apply is unavailable on this exact host");
      }
      if (response.url !== organizerUrl.href) {
        throw new PlanningTransportError("ENGINE_ORGANIZER_ORIGIN", "Organizer response origin does not match");
      }
      if (!response.ok) {
        let responseCode: string | undefined;
        try {
          const value = await boundedResponseValue(response);
          if (value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 1 && typeof (value as { code?: unknown }).code === "string") {
            responseCode = (value as { code: string }).code;
          }
        } catch {
          responseCode = undefined;
        }
        throw new PlanningTransportError(
          responseCode === "HOME_HOST_MISMATCH" ? "ENGINE_ORGANIZER_HOST_MISMATCH" : "ENGINE_ORGANIZER_RESPONSE",
          responseCode === "HOME_HOST_MISMATCH" ? "Organizer request names a different host" : "Director Engine rejected Organizer Preview/Apply",
        );
      }
      if (
        response.headers.get("x-director-contract-version") !== PLANNING_CONTRACT_VERSION ||
        response.headers.get("x-director-contract-hash") !== PLANNING_CONTRACT_SHA256
      ) {
        throw new PlanningTransportError("ENGINE_ORGANIZER_CONTRACT", "Organizer contract does not match");
      }
      try {
        const result = organizerBootstrapResultSchema.parse(await boundedResponseValue(response));
        if (result.hostId !== input.hostId || result.requestId !== input.requestId) {
          throw new PlanningTransportError("ENGINE_ORGANIZER_BINDING", "Organizer response binding does not match");
        }
        return result;
      } catch (error) {
        if (error instanceof PlanningTransportError) throw error;
        throw new PlanningTransportError("ENGINE_ORGANIZER_PAYLOAD", "Organizer response is invalid");
      }
    },
    async home(rawInput: HomeQueryInput): Promise<HomeSnapshot> {
      const input = homeQueryInputSchema.parse(rawInput);
      const body = JSON.stringify(input);
      if (new TextEncoder().encode(body).byteLength > PLANNING_MAXIMUM_REQUEST_BYTES) {
        throw new PlanningTransportError("ENGINE_HOME_INPUT", "Director Engine Home query exceeds the contract bound");
      }
      let response: Response;
      try {
        response = await fetchPlanning(homeUrl, {
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
        throw new PlanningTransportError("ENGINE_HOME_UNAVAILABLE", "The exact Director host is unavailable");
      }
      if (response.url !== homeUrl.href) {
        throw new PlanningTransportError("ENGINE_HOME_ORIGIN", "Director Engine Home response origin does not match");
      }
      if (!response.ok) {
        let responseCode: string | undefined;
        try {
          const value = await boundedResponseValue(response);
          if (value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 1 && typeof (value as { code?: unknown }).code === "string") {
            responseCode = (value as { code: string }).code;
          }
        } catch {
          responseCode = undefined;
        }
        const code = responseCode === "HOME_HOST_MISMATCH"
          ? "ENGINE_HOME_HOST_MISMATCH"
          : responseCode === "HOME_CURSOR_INVALIDATED"
            ? "ENGINE_HOME_CURSOR_INVALIDATED"
            : "ENGINE_HOME_RESPONSE";
        throw new PlanningTransportError(code, code === "ENGINE_HOME_HOST_MISMATCH"
          ? "Director Engine refused a different host identity"
          : code === "ENGINE_HOME_CURSOR_INVALIDATED"
            ? "Director Home changed; refresh from the first page"
            : "Director Engine rejected the Home query");
      }
      if (
        response.headers.get("x-director-contract-version") !== PLANNING_CONTRACT_VERSION ||
        response.headers.get("x-director-contract-hash") !== PLANNING_CONTRACT_SHA256
      ) {
        throw new PlanningTransportError("ENGINE_HOME_CONTRACT", "Director Engine Home contract does not match");
      }
      try {
        const snapshot = homeSnapshotSchema.parse(await boundedResponseValue(response));
        if (snapshot.page.host.id !== input.hostId) {
          throw new PlanningTransportError("ENGINE_HOME_HOST_MISMATCH", "Director Engine returned a different host identity");
        }
        return snapshot;
      } catch (error) {
        if (error instanceof PlanningTransportError) throw error;
        throw new PlanningTransportError("ENGINE_HOME_PAYLOAD", "Director Engine Home response is invalid");
      }
    },
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
    async taskDetail(rawInput: TaskDetailQueryInput): Promise<TaskDetailSnapshot> {
      const input = taskDetailQueryInputSchema.parse(rawInput);
      const body = JSON.stringify(input);
      if (new TextEncoder().encode(body).byteLength > PLANNING_MAXIMUM_REQUEST_BYTES) {
        throw new PlanningTransportError(
          "ENGINE_TASK_DETAIL_INPUT",
          "Director Engine Task detail query exceeds the contract bound",
        );
      }
      let response: Response;
      try {
        response = await fetchPlanning(taskDetailUrl, {
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
          "ENGINE_TASK_DETAIL_UNAVAILABLE",
          "Task detail is unavailable on this exact Director host",
        );
      }
      if (response.url !== taskDetailUrl.href) {
        throw new PlanningTransportError(
          "ENGINE_TASK_DETAIL_ORIGIN",
          "Director Engine Task detail response origin does not match",
        );
      }
      if (!response.ok) {
        let responseCode: string | undefined;
        try {
          const value = await boundedResponseValue(response);
          if (value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === 1 && typeof (value as { code?: unknown }).code === "string") {
            responseCode = (value as { code: string }).code;
          }
        } catch {
          responseCode = undefined;
        }
        const code = responseCode === "TASK_DETAIL_HOST_MISMATCH"
          ? "ENGINE_TASK_DETAIL_HOST_MISMATCH"
          : responseCode === "TASK_DETAIL_CURSOR_INVALIDATED"
            ? "ENGINE_TASK_DETAIL_CURSOR_INVALIDATED"
            : "ENGINE_TASK_DETAIL_RESPONSE";
        throw new PlanningTransportError(
          code,
          code === "ENGINE_TASK_DETAIL_HOST_MISMATCH"
            ? "Director Engine refused a different Task detail host identity"
            : code === "ENGINE_TASK_DETAIL_CURSOR_INVALIDATED"
              ? "Task activity changed; refresh this exact Task"
              : "Director Engine rejected the Task detail query",
        );
      }
      if (
        response.headers.get("x-director-contract-version") !== PLANNING_CONTRACT_VERSION ||
        response.headers.get("x-director-contract-hash") !== PLANNING_CONTRACT_SHA256
      ) {
        throw new PlanningTransportError(
          "ENGINE_TASK_DETAIL_CONTRACT",
          "Director Engine Task detail contract does not match",
        );
      }
      try {
        const snapshot = taskDetailSnapshotSchema.parse(await boundedResponseValue(response));
        if (snapshot.hostId !== input.hostId || JSON.stringify(snapshot.query) !== JSON.stringify(input)) {
          throw new PlanningTransportError(
            "ENGINE_TASK_DETAIL_BINDING",
            "Director Engine returned a different Task detail binding",
          );
        }
        return snapshot;
      } catch (error) {
        if (error instanceof PlanningTransportError) throw error;
        throw new PlanningTransportError(
          "ENGINE_TASK_DETAIL_PAYLOAD",
          "Director Engine Task detail response is invalid",
        );
      }
    },
    async mutate(rawInput: PlanningMutationInput): Promise<PlanningMutationResult> {
      const actor = options.mutationActor;
      const identityPattern = /^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$/u;
      if (!actor || actor.kind !== "human" || !identityPattern.test(actor.id) || !identityPattern.test(actor.sessionId)) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_ACTOR",
          "Director Engine planning mutation requires a server-authenticated human session",
        );
      }
      const input = planningMutationInputSchema.parse(rawInput);
      const body = JSON.stringify(input);
      if (new TextEncoder().encode(body).byteLength > PLANNING_MAXIMUM_REQUEST_BYTES) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_INPUT",
          "Director Engine planning mutation exceeds the contract bound",
        );
      }
      let response: Response;
      try {
        response = await fetchPlanning(mutationUrl, {
          method: "POST",
          redirect: "error",
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": PLANNING_CONTRACT_VERSION,
            "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
            [PLANNING_MUTATION_ACTOR_HEADERS.kind]: actor.kind,
            [PLANNING_MUTATION_ACTOR_HEADERS.id]: actor.id,
            [PLANNING_MUTATION_ACTOR_HEADERS.session]: actor.sessionId,
          },
          body,
          signal: AbortSignal.timeout(requestTimeoutMilliseconds),
        });
      } catch {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_UNAVAILABLE",
          "Director Engine planning mutation is unavailable",
        );
      }
      if (response.url !== mutationUrl.href) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_ORIGIN",
          "Director Engine planning mutation origin does not match",
        );
      }
      if (!response.ok) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_RESPONSE",
          "Director Engine rejected the planning mutation",
        );
      }
      if (
        response.headers.get("x-director-contract-version") !== PLANNING_CONTRACT_VERSION ||
        response.headers.get("x-director-contract-hash") !== PLANNING_CONTRACT_SHA256
      ) {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_CONTRACT",
          "Director Engine planning contract does not match",
        );
      }
      try {
        return planningMutationResultSchema.parse(await boundedResponseValue(response));
      } catch {
        throw new PlanningTransportError(
          "ENGINE_PLANNING_PAYLOAD",
          "Director Engine planning mutation response is invalid",
        );
      }
    },
  };
}

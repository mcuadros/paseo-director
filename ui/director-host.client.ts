// SPDX-License-Identifier: Apache-2.0
// The Director identity comes only from the authenticated connector status RPC.

import { useRpc } from "@getpaseo/plugin";
import { useQuery } from "@tanstack/react-query";

import { connectorStartupStatus } from "../rpc/startup.shared.ts";

export function useDirectorHostIdentity() {
  const loadStatus = useRpc(connectorStartupStatus);
  const query = useQuery({
    queryKey: ["director", "authenticated-host-identity"],
    queryFn: () => loadStatus({}),
    retry: false,
    staleTime: 0,
    refetchOnMount: "always",
    refetchOnReconnect: true,
  });
  return {
    identity: query.data?.host ?? null,
    isPending: query.isPending,
    isError: query.isError,
    error: query.error,
  };
}

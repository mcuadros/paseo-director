// SPDX-License-Identifier: Apache-2.0
// The one Director Home snapshot query, and the one cache key it lives under.
//
// The Console hosts the Home body beside tabs that need the same snapshot: the
// Workers tab fans out over the Workspaces the snapshot already carries. Both
// read it through this hook, so the Console issues one query against one cache
// key instead of one per consumer.

import { useRpc } from "@getpaseo/plugin";
import { useInfiniteQuery } from "@tanstack/react-query";

import type { HomeSnapshot } from "../generated/planning-contract.shared.ts";
import { homeQueryRpc } from "../rpc/planning.shared.ts";

export const HOME_PAGE_SIZE = 25;

/** The single cache key every Home snapshot consumer reads and invalidates. */
export function directorHomeQueryKey(hostId: string): readonly string[] {
  return ["director", "home", hostId];
}

export function useDirectorHomeSnapshot(hostId: string) {
  const loadHome = useRpc(homeQueryRpc);
  return useInfiniteQuery({
    queryKey: directorHomeQueryKey(hostId),
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const result = await loadHome({ hostId, cursor: pageParam, pageSize: HOME_PAGE_SIZE });
      if (!("page" in result)) throw result;
      return result;
    },
    getNextPageParam: (lastPage: HomeSnapshot) => lastPage.page.nextCursor,
    enabled: hostId !== "",
    retry: false,
    staleTime: 0,
    refetchOnMount: "always",
    refetchOnReconnect: true,
    refetchInterval: 30_000,
  });
}

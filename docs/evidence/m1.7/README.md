# Paseo 0.7.2 Board refresh measurement

This evidence closes ADR-0017's M1 prerequisite to measure the Paseo UI refresh
path before choosing a cadence. It is behavioral evidence, not an inferred host
guarantee.

## Exact bounded tuple

- Debian Linux `6.12.107+deb13-amd64`, x86-64.
- Paseo CLI/server/web UI `0.7.2`.
- Node.js `26.7.0`, npm `11.19.0`.
- React `19.1.0` and TanStack React Query `5.90.11` from the exact plugin
  development/runtime contract.
- Google Chrome for Testing `151.0.7922.34`, fresh disposable headless profile.
- One passwordless disposable daemon bound only to `127.0.0.1:17817`, with no
  relay, MCP, injected MCP, agent, Workspace, provider, user daemon, or real
  TaskStore effect.

The generated exact-0.7.2 plugin fixture registered the repository's actual
`ProjectBoard` as a global measurement surface and handled its existing strict
`boardSnapshotRpc` with controlled delayed-empty, data, and error responses.
The bundled Paseo web client was driven locally through Chrome DevTools Protocol
on loopback. The browser profile and daemon home were new disposable paths.

## Result

The mounted component reached these visible render states through the real
Paseo client/server/plugin path:

1. `Loading project tasks` while the first RPC was delayed.
2. `No tasks yet` after the delayed empty snapshot.
3. `Measured Board task` after changing the handler to a data snapshot.
4. The same cached task after the next scheduled RPC failed, without showing
   the initial-error scene.
5. `Board data is unavailable` after plugin reload cleared the installation
   query state and the first RPC failed.

Successive immediate data/error request starts were `2,006 ms` apart for the
configured `2,000 ms` interval. No missing-`QueryClientProvider` error occurred;
the actual `useRpc` plus `useQuery` surface rendered and refreshed. This proves
the provider/module identity and cadence only for exact Paseo `0.7.2`; it makes
no future-version, push-latency, background-client, or provider guarantee.

The normalized output is [refresh-output.json](refresh-output.json). Its
committed SHA-256 is
`4fa123fdaaee678ec434c5cc85b4aefa3f8cfb577da9b5be3e43e3609b294d5c`;
the pre-normalization measurement artifact was
`8a92bf9dabd753ebe0c17d2d032934c6049aefbe0c10150861fbac0bdbf841ed`.
The raw four-line request log SHA-256 was
`cc86a9b56fe9c4682c913ccb6ea73fd00e8ad0e5dd903091cf3f61efa3d4f3eb`.

## Reproduction and identity

The reusable driver and fixture-only entry/handler live under
`tools/measurements/dir-m1.7/`. Reproduce from a disposable directory:

1. Run `paseo plugin init <root>/plugin --id director-board-measure --json`.
2. Copy this repository's `ui`, `rpc`, and `generated` directories into that
   plugin, then replace `index.ts` and add `measurement.server.ts` from the
   fixture directory.
3. Install the fixture dependencies and run `npm run typecheck` before install.
4. Start exact Paseo `0.7.2` with a fresh home and loopback listener, using
   `--foreground --no-relay --no-mcp --no-inject-mcp --web-ui`; then enable
   plugins through the public SDK and install only the inspected fixture.
5. Start a fresh local headless Chrome profile with a loopback CDP endpoint and
   run `measure.mjs` with its required `DIRECTOR_MEASURE_*` variables.

Observed hashes before cleanup:

- copied `ui/shells.client.tsx`:
  `31945847b6ad7d5ddd9e3a50496f1ac65b4954fdc5c5a630f3330c06701202e8`;
- measurement entry:
  `573c50c01ec0178a7c32d0d5fa6ec0b579fc0b998fc00697d8e7031e906bbe05`;
- measurement handler:
  `6b34547c40e07f3f38608ecffc143fef02070bf688f98446ac2d08370e516445`;
- exact bundled web client containing the exercised top-level query provider:
  `4dab10101531e58883d8018b704a43aaaeda6da2e90883404dae876b99ecaed2`.

The disposable plugin, daemon, Chrome processes/profile, and fixture directory
are removed after their absence is verified. The committed evidence contains no
password, credential, browser session, daemon state, or generated dependency.

# ADR-0009: Distribute Director under Apache-2.0 through reviewed Git channels

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.9`
- **Plan gate:** M0 public licensing and distribution compatibility
- **Decision owner:** Project owner
- **Amended by:** [ADR-0011](0011-linux-only-platform-scope.md), which establishes Linux as the sole `1.0` platform

## Context

Director is intended to be a public Paseo plugin. Before product code begins, M0 must establish that Director can use Paseo's public plugin SDK, remain Apache-2.0, install and update through supported public mechanisms, disclose its dependency boundary, and use its public name without implying endorsement.

The distribution unit considered here is Director's Git source checkout as installed by Paseo. Director does not redistribute Paseo, external agent providers, Git, GitHub CLI, Beads, Dolt, or other system executables.

The current repository base also contains accepted [ADR-0002](0002-paseo-0.7.2-public-surface.md), which limits the first scaffold to the exact published `0.7.2` API, and [ADR-0008](0008-director-threat-model.md), whose independently reviewed No-go keeps M1 blocked on authority separation. Those decisions reinforce exact-version and trusted-update requirements but do not change this licensing result.

## Question or hypothesis

Can Director be publicly distributed from `mcuadros/paseo-director` under Apache-2.0 through Paseo's supported Git plugin lifecycle, without bundling undeclared external binaries, while satisfying license, dependency-disclosure, update, and descriptive naming requirements?

## Acceptance criteria

- The exact stable Paseo source and plugin SDK have terms compatible with an Apache-2.0 Director plugin.
- Paseo's official service terms do not add restrictions to local open-source plugin distribution.
- A supported Git install/update mechanism resolves and reports exact commits, distinguishes tracked branches from pinned refs, rejects a failed candidate without replacing the installed commit, and cleans managed checkouts.
- Shipped dependencies, development-only host modules, and separately installed executables have explicit boundaries and release-time disclosure gates.
- The public name uses Paseo only descriptively and does not imply affiliation, endorsement, or ownership of Paseo marks.

## Evidence

The reproducible record is [`docs/evidence/dir-m0.9/licensing-distribution.md`](../evidence/dir-m0.9/licensing-distribution.md).

Key facts:

- Paseo `v0.7.2`, exact tag commit `9400a49af670fdb5db4af58e73f8df98588dbea9`, carries an Apache-2.0 root license and Apache-2.0 root package metadata.
- Paseo's terms, last updated 2026-08-29, explicitly say that service terms do not replace or restrict the open-source license.
- The stable `0.7.x` plugin docs say Paseo supplies `@getpaseo/plugin` and other host modules at runtime; Git install/update runs trusted code on the daemon host.
- A disposable test with installed Paseo `0.7.2` reproduced exact-commit installation, explicit branch update, tag pinning, failed-candidate preservation, and managed cleanup.
- The official generated scaffold baseline uses only MIT or Apache-2.0 dependencies, except that the exact `@getpaseo/plugin@0.7.2` npm metadata/tarball omits its license field/file. Its tagged source is Apache-2.0. Director does not redistribute that package.
- Git and all forge, TaskStore, and provider CLIs remain separately installed prerequisites. Their code is not part of the Director distribution.
- Apache-2.0 does not grant broad trademark rights. **Director for Paseo** uses Paseo nominatively to describe compatibility, follows the descriptive pattern visible on Paseo's official related-projects page, and remains subject to the restrictions below.

## Alternatives considered

### Publish Director as an npm package

This adds a second distribution/update channel that Paseo does not require. It expands artifact, dependency, provenance, and notice obligations without a `1.0` benefit.

### Bundle Paseo SDK/runtime modules or external executables

This would duplicate host-provided code, enlarge the trusted supply chain, and potentially make Director responsible for licenses and notices of binary distributions. It conflicts with the plan's prerequisite and no-silent-download boundaries.

### Publish only immutable tags or commit pins

Pins give excellent reproducibility but Paseo correctly treats them as non-updating. Users would need a ref change or reinstall for every release, so pins are retained as an audit/rollback option rather than the normal channel.

### Track the repository default branch for every audience

This makes `paseo plugin update` simple but exposes stable users to unpromoted commits. Explicit `stable` and `beta` branches preserve the supported update mechanism while separating release channels.

### Rename the product to omit Paseo entirely

This avoids nominative use but makes compatibility less clear. The selected name uses `Paseo` only in the descriptive `for Paseo` construction and pairs it with an independence disclaimer.

## Decision

**Go.** Director may proceed under Apache-2.0 using Paseo's documented Git source installation and update mechanism, subject to all of these mandatory bounds:

1. Director's source distribution carries the unmodified English Apache-2.0 `LICENSE` and `Apache-2.0` SPDX/package metadata. `dir-m1.2` must add them in the same Candidate as the first product scaffold; product code is not committed first and licensed later.
2. Director does not vendor or redistribute Paseo, `@getpaseo/plugin`, host-provided UI/runtime modules, `node_modules`, provider CLIs, Git, GitHub CLI, Beads, or Dolt.
3. Any future shipped dependency, Git submodule, or generated bundle is audited from the exact release commit and lockfile. Required licenses, copyright notices, and upstream `NOTICE` content are included in a release-generated third-party notice file. An unknown, unlicensed, or incompatible shipped dependency blocks release.
4. External prerequisites are declared by feature with tested capability/version bounds, official install source, license/terms owner, and clear responsibility for installation/authentication. Director never silently downloads them.
5. Prefer an empty plugin `build` list. Any required build command is a reviewed direct-argv manifest entry, uses a committed lockfile, and is disclosed as trusted daemon-host execution.
6. Release-time moving branches named `stable` and `beta` are the supported update channels. They are protected, fast-forward-only refs. Each promotion points to an independently reviewed Candidate that also has an immutable semantic-version tag. No channel ref is created by this M0 spike.
7. Stable installation is documented as `paseo plugin add mcuadros/paseo-director --ref stable`; updates are explicit through `paseo plugin update director`. Tags and exact commits are documented as pinned installations that do not advance through update.
8. **Director for Paseo** is described as an independent community plugin, not affiliated with, endorsed by, or maintained by Paseo. Public materials use no Paseo logo, copied trade dress, `official`, `certified`, or partnership claim without separate permission.
9. Compatibility is declared from exact tested releases, not inferred from a major or minor line. This spike validates only `0.7.2` on Linux; every later stable release requires revalidation before entering the declared range. It does not authorize a production build against preview `0.8` or waive the Linux release gate.
10. `dir-m6.4` revalidates the license file and implements the final dependency inventory/notices, prerequisite disclosures, compatibility statement, naming disclaimer, and channel instructions before public beta.

The missing license metadata/file in the published `@getpaseo/plugin@0.7.2` tarball is a documented upstream packaging caveat, not a Director blocker under the no-vendoring and host-provided-module rules. If Director later redistributes any part of that artifact, this decision no longer covers the distribution until the applicable upstream license and notices are restored and reviewed.

## Consequences

- The M0 licensing/distribution stop condition is resolved once an independent reviewer approves the exact Candidate containing this ADR and evidence.
- Director can remain Apache-2.0 without inheriting a copyleft license from separately executed prerequisites.
- Public installation remains native to Paseo and does not require a Director npm package or bespoke updater.
- A moving release channel is a trust boundary: users opt into future commits when they run update, and every promotion must satisfy release gates.
- The initial root license/package metadata is part of `dir-m1.2`; dependency-license automation and final notices remain release work owned by `dir-m6.4`. The empty scaffold is not treated as a future dependency audit.
- This licensing Go does not override ADR-0008, authorize M1, or approve the same-user execution topology.
- A new Paseo trademark/plugin naming policy, a requested naming change, a bundled SDK/executable, or an incompatible production dependency requires re-evaluation before release.

## Independent verification

Pending. The reviewer must reproduce or inspect the raw evidence against the exact Candidate SHA and return one structured verdict. The Task remains open until that verdict is recorded.

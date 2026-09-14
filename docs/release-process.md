# Release process and channels

No tagged alpha, public beta, stable channel, semantic-version tag, or Director
Engine release has been published. `release/engine.json` must remain explicitly `unpublished`
until an authorized release coordinator completes the applicable gates below.
This document defines governance; it does not authorize a promotion.

## Authority and immutable inputs

Only the Director Engine or authorized coordinator performs publication,
integration, tagging, asset upload, channel movement, cleanup, and Task
closure. A Task Agent, Reviewer, model claim, test result, local build, or
changelog entry cannot authorize a lifecycle effect.

Every release attempt freezes one exact Candidate SHA, relevant base SHA,
source tree, version, linux-amd64 target, dependency lock/module/policy digests,
and release descriptor. A changed Candidate or relevant base invalidates prior
CI, Review, artifact, and readiness evidence.

## Channels

| Channel | Audience and compatibility | Promotion authority |
|---|---|---|
| Default `main` | Development/testing of the complete current product; declared install/update preparation compiles the exact checked-out Go bootstrap and Engine and verifies canonical precompiled Dolt `2.3.2`; reload never compiles | This named behavior is integrated and is not a release or fallback channel. |
| Tagged alpha | Internal/pre-release evaluation only; no public data-compatibility promise between builds | `1.0.0-alpha.1` assets must be built by the authorized release CI/process and pinned by a later reviewed metadata commit before an alpha ref/tag moves. |
| Public beta | Feature-complete `1.0` scope with operational backups/migrations and green Linux gates; P2 only when explicitly accepted and documented | The M6.6 owner manual hold must be lifted by a new explicit instruction before any beta Task, ref, tag, asset, or announcement is created. |
| Stable | Supported Semantic Versioning line after the complete stable-readiness campaign | Separate explicit stable promotion plus the independent M6.8 release audit is mandatory. |

`alpha`, `beta`, and `stable`, once created, are protected fast-forward-only Git branches.
Every promoted point also has an immutable semantic-version tag. A branch is an
explicit update channel; a tag or exact commit is an immutable pin and does not
advance through `paseo plugin update`. No channel is promoted automatically by
merging ordinary Task work.

## Tagged alpha, beta, and stable artifact preparation

Tagged releases never compile on an installed host. The authorized release
CI/process performs this sequence on the exact clean source Candidate:

1. Run the focused source/public/license checks and let the one authoritative
   remote Linux CI perform the sole complete Candidate validation.
2. Materialize the full locked build closure with dependency lifecycle scripts
   disabled.
3. Generate and independently verify `THIRD_PARTY_NOTICES.txt` from the exact
   Candidate:

   ```text
   node tools/release/notices.mjs generate --source . --candidate <candidate-sha> --installed-scope all --output <new-private-notices-path>
   node tools/release/notices.mjs verify --source . --candidate <candidate-sha> --installed-scope all --notices <private-notices-path>
   ```

4. Obtain the canonical DoltHub Dolt `2.3.2` linux-amd64 archive and its exact
   extracted executable in the release process. Verify their origin, version,
   regular-file identity, size, and digest.
5. Build the static bootstrap and Engine closure. The builder verifies the
   generated notices and both binary module graphs, then retains every current
   bootstrap/Engine/notices/Dolt size and SHA-256 metadata field:

   ```text
   node tools/release/build-engine.mjs --source <absolute-clean-engine-root> --candidate <candidate-sha> --version <semantic-version> --notices <private-notices-path> --dolt-version 2.3.2 --dolt-archive <absolute-canonical-dolt-archive> --dolt-executable <absolute-extracted-dolt> --output <new-private-output-directory>
   ```

6. Verify both binaries report the same version, build mode, source Candidate,
   and target; verify Engine additionally reports its executable/notices
   SHA-256 and contract version/hash. Verify generated bootstrap schema version
   1 and Engine schema version 2 metadata name only the canonical bootstrap,
   Engine, notices, and Dolt assets and their exact sizes/digests.
7. Preserve the precompiled bootstrap, Engine, notices, canonical Dolt
   observations, and exact builder metadata as private release inputs. Do not
   tag or upload them. A later, separately reviewed metadata Candidate copies
   the exact builder outputs into `release/bootstrap-linux-amd64.json` and
   `release/engine.json`, changing them from `unpublished` to `published`
   without rebuilding the assets.
8. Validate and integrate that metadata Candidate without compiling. Only then
   may the authorized release Task create the immutable semantic-version tag,
   upload the already built bootstrap/Engine/notices assets, verify their
   canonical URLs, sizes, and digests, and move the intended alpha, beta, or
   stable channel ref.

Local builds and private evidence do not authorize upload or promotion. The
source Candidate, later metadata Candidate, asset/tag publication, and channel
movement are distinct exact effects. An unavailable or mismatched asset blocks
channel movement; tagged installs never compile or fall back to source.

## Default-main preparation

Installing or updating the default `main` branch must compile the exact clean
checkout's Go bootstrap and Engine only during declared Paseo candidate
preparation, bind its Candidate/contract/executable/notices identity, and
atomically prepare it for the plugin-owned Go controller. This is an explicit
main-channel contract, not an ambient fallback. Plugin reload never compiles.
Canonical Dolt remains a verified precompiled `2.3.2` artifact. This behavior
is development evidence; it does not substitute for release-CI-built alpha
assets or published metadata.

## Non-bypassable checklist

The coordinator records each item as a current exact observation. `Pending`,
unknown, stale, ambiguous, or merely claimed evidence blocks release.

- [ ] The source is one clean exact Candidate reachable from the frozen base;
      the tree and changed-path digest are recorded.
- [ ] The root Apache-2.0 license, SPDX metadata, public security/support/
      compatibility/changelog material, and independent-plugin disclaimer pass.
- [ ] The complete npm lock and installed production/build closures, exact
      production `@getpaseo/client@0.7.2`, CI-only
      `@getpaseo/cli@0.7.2`, Go modules/toolchain, shipped binary content,
      attributions, non-SPDX build-only references, license texts, and notices
      inventory pass with no unknown, incompatible, missing, stale, or
      duplicate entry. Only shipped plugin and release-binary obligations
      appear in the notice asset.
- [ ] The notices are generated from the exact Candidate; notices, binary, and
      metadata digests and sizes match; replay produces the same output.
- [ ] Tagged release metadata preserves schema version 2 and the current Dolt
      `2.3.2` archive/executable origin, version, size, and digest fields. The
      tagged installed path has no compiler or source fallback; every runtime
      has no source-root, `PATH`, or system-service discovery, and reload never
      compiles.
- [ ] Default-main compilation retains the independently integrated ADR-0025
      proof: compile occurs only during declared add/update preparation; reload
      never compiles; failure preserves the prior install; canonical Dolt stays
      precompiled; unrelated agents/workspaces remain unchanged.
- [ ] Clean install, update, failed-candidate preservation, rollback, upgrade,
      incompatible-host diagnostics, and restart-free plugin-scoped activation
      pass on exact Paseo `0.7.2` without stopping unrelated agents/workspaces.
- [ ] A current real backup and fresh restore pass; migrations preserve the
      prior recoverable state.
- [ ] Real pull-request, direct-delivery, Review, CI, feedback, recovery, and
      cleanup workflows pass. Synthetic claims or fixture-only success cannot
      replace release-candidate evidence.
- [ ] The Linux scale gate passes at 25 Workspaces, 10,000 historical Tasks,
      500 open Tasks, and eight concurrent agents within the recorded resource
      bounds.
- [ ] Exactly one authoritative complete remote Linux CI run is recorded for
      this Candidate. A Reviewer or local author check does not start or replace
      it.
- [ ] One independent top-level Reviewer approves the exact Candidate/base and
      covers acceptance, correctness, security, maintainability, readability,
      design, quality, and rigor; every finding and human feedback item is
      resolved.
- [ ] Immediately before integration, the live remote head equals the reviewed
      Candidate, the relevant base is current, every configured/required check
      passes, the PR is mergeable, and feedback is clear. Integration uses an
      atomic expected-head condition such as `--match-head-commit
      <candidate-sha>`; a refetch alone is insufficient.
- [ ] Public beta has a new explicit owner instruction lifting the M6.6 manual
      hold. Stable has a separate explicit promotion and independent release
      audit. Neither approval is inferred from elapsed time or other gates.
- [ ] Stable additionally has zero known P0/P1, 14 consecutive beta days
      without a critical incident, at least 20 complete real workflows across
      several Projects and multi-repository cases, verified upgrade from the
      previous beta, verified restore of a real backup, current Linux/platform
      and scale evidence, and complete public documentation.
- [ ] Publication, integration, channel/tag movement, post-release observation,
      owned-resource cleanup, and Task closure are verified by the coordinator
      under their separate authorities.

## Restart-free and channel-separation gate

Release and support material must use the reviewed public plugin-scoped live
activation mechanism and safe non-secret defaults integrated by M6.10–M6.17.
It must not tell users to stop the Paseo daemon, reboot the machine, install a
system service, open a secondary Paseo connection/bridge, or interrupt unrelated
agents/workspaces. Main compilation remains add/update-only; tagged release
installation remains strictly precompiled. A full daemon or machine restart is
not an accepted workaround.

## Rollback and incident handling

Stop promotion on any P0/P1, unaccepted P2, secret exposure, wrong-SHA evidence,
artifact mismatch, failed restore, or ambiguous external effect. Preserve the
current channel, installed commit, external cache, TaskStore, Organizer,
configuration, and recovery material. Reconcile the attempted effect before
retrying.

A rollback selects a previously reviewed immutable tag or commit through the
documented Paseo Git lifecycle. It never rewrites a moving channel, deletes
unknown state, silently changes delivery mode, or substitutes a daemon/machine
restart for plugin-scoped activation. Historical incident snapshot
`3168ea518f4fb400551fca8221d6477466f8a2cd` is not a rollback target. Publish a security advisory or incident
notice only through the authorized private/coordinated process in
[SECURITY.md](../SECURITY.md).

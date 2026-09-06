---
name: director-commit
description: Prepare and create a focused, auditable Git commit for one paseo-director Beads Task. Use when an agent is ready to stage, commit, amend its own unshared commit, or report a candidate SHA in this repository.
---

# Director Commit

Create one reviewable commit that represents one logical change for one Beads Task.

## Preconditions

1. Read the Beads Task and confirm it is claimed by the current top-level Task Agent.
2. Inspect `git status`, the unstaged diff, and the staged diff.
3. Separate unrelated or user-owned changes; never include them for convenience.
4. Run the checks required by the Task and changed area.
5. Scan staged content for credentials, generated secrets, debug artifacts, and unintended binaries.
6. Refuse to commit unresolved conflict markers or a known broken state.

## Stage deliberately

- Add explicit intended paths.
- Do not use broad staging when unrelated changes exist.
- Review `git diff --cached --stat` and `git diff --cached` before committing.
- Ensure the staged patch is complete without relying on unstaged changes.

## Message format

Use Conventional Commits:

```text
<type>(<scope>): <imperative summary>

<optional body explaining why and important tradeoffs>

Beads: <issue-id>
```

Allowed types are `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, and `chore`.

- Keep the summary concise and imperative.
- Use a meaningful scope when one subsystem is affected; omit it when the change is genuinely cross-cutting.
- Explain rationale and non-obvious risk in the body, not a file-by-file narration.
- Include exactly one `Beads:` trailer for the owning Task.
- Do not invent co-author trailers or attribution.

## Verify the result

1. Capture the exact commit SHA.
2. Re-run `git status --short`.
3. Confirm no intended file was left behind and no unrelated file was committed.
4. Record the SHA and validation evidence in Beads.
5. Hand the exact SHA to an independent top-level Reviewer Agent.

Do not amend another author's commit, rewrite shared history, bypass hooks, or call a dirty worktree review-ready.

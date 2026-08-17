---
name: ctx-status
description: Inspect the single active local ctx context, latest checkpoint, and Git differences without changing ctx or Git.
---

# Inspect ctx status

1. Resolve `CTX_BIN` or `ctx` from `PATH` and verify `ctx --version`.
2. Run:

   ```text
   ctx --cwd <repo> --client <client> --json status
   ```

   Add `--store <CTX_STORE>` only when set.
3. Report the selected worktree and branch, title, context ID, latest checkpoint
   ID and time, summary, decisions (what + why), next actions, blockers, and Git
   differences.

This skill is read-only. Do not start, checkpoint, install, sync, or change Git.

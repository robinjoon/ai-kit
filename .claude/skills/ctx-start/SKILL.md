---
name: ctx-start
description: Start a new local ctx work context for the current Git repository when the user explicitly begins a new unit of work.
---

# Start a ctx context

Use this skill only for an explicit new unit of work. It replaces the single active
context for the current repository, worktree, and branch scope but never changes
Git. Other local worktrees and branches keep their own active contexts.

1. Use executable `CTX_BIN` when set, otherwise resolve `ctx` from `PATH`.
2. Run `ctx --version`. Accept `ctx dev` or release `0.1.0` and newer. Stop when
   the CLI is unavailable; do not install, search the repository, or use `go run`.
3. Derive a short title from the user's request.
4. Run:

   ```text
   ctx --cwd <repo> --client <client> --json start --title <title>
   ```

   Use `com.anthropic.claude-code` or `com.openai.codex` as the client. Add
   `--store <CTX_STORE>` only when that environment variable is set.
5. Report the selected worktree and branch, title, and returned
   context/checkpoint IDs.

Do not create aliases, tasks, handoffs, snapshots, parents, or merge records.

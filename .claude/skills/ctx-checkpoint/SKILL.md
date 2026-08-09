---
name: ctx-checkpoint
description: Automatically persist the active ctx work context after substantive progress, a durable decision, validation, completion, or before switching coding agents; also use when the user explicitly asks to save progress.
---

# Save the current context

Create a self-contained checkpoint without changing Git. Use this automatically
when an active ctx context exists and the current turn produced durable progress.
Do not checkpoint read-only discussion or a turn with no meaningful state change.

1. Resolve `CTX_BIN` or `ctx` from `PATH` and verify `ctx --version`. Do not
   install or fall back to `go run`.
2. Run `ctx --cwd <repo> --client <client> --json status`. The CLI selects the
   current repository, worktree, and branch scope. If that scope has no active
   context, do nothing unless the user explicitly asked to save; in that case ask
   them to start one.
3. Build one JSON object containing the complete current state:

   - `goal` and `summary` are required.
   - `decisions`, `next_actions`, and `blockers` are optional string arrays.
   - Include completed work, relevant files, and verification results in
     `summary` only when they matter for continuation.
   - Include current facts, not raw conversation or speculative history.

4. Write the JSON to a temporary file and run:

   ```text
   ctx --cwd <repo> --client <client> --json checkpoint --input <file> --reason <reason>
   ```

   Use reason `progress`, `decision`, `validation`, `handoff`, or `completion`
   only as a short descriptive label; it has no workflow semantics.
5. Remove the temporary file and report the checkpoint ID only when the user
   explicitly requested a save or handoff. Automatic background saves should not
   add noise to the user-facing response.

Do not add IDs, timestamps, Git data, parents, hashes, or schema fields to the
input. The CLI supplies its small amount of mechanical state.

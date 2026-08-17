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
3. Build one JSON object. Each field serves a specific purpose for the next agent
   reading this checkpoint cold, without access to conversation history:

   - `goal` — One sentence. The purpose of the work, not background context.
     Stays fixed unless the objective itself changes after start.
     Answers: "What am I here to accomplish?"

   - `summary` — A short paragraph describing current state. Write in the present
     tense ("X is done, Y is in progress"), not as an event log ("did X, then Y").
     Focus on where things stand, not on implementation detail.
     Answers: "How far along is this?"

   - `decisions` — An array of choices that explain why this direction was taken.
     Lets the next agent pick up without reopening the same questions. Skip
     obvious or self-evident choices. Each entry has `what` (one sentence) and
     `why` (one or two sentences, only when the reasoning is not obvious).
     Answers: "Why are we going this way?"

   - `next_actions` — A concrete list of what to do next. Specific enough that
     the next agent can start on the first item without further thought. One line
     per item.
     Answers: "What do I do first?"

   - `blockers` — Real external blockers only: conditions outside the agent's
     control that prevent progress. Not internal uncertainty or open questions.
     Omit when empty.
     Answers: "Why can't we move forward?"

   Example:
   ```json
   {
     "goal": "Change the decisions field in the ctx checkpoint schema to include reasoning.",
     "summary": "autocheckpoint and sessionlog packages are removed. decisions is now a [{what, why}] array. CLI rendering, Skills, and tests are all updated; 10 tests pass.",
     "decisions": [
       {
         "what": "Remove log-based automatic checkpointing.",
         "why": "Agent-driven checkpoints capture intent more accurately, and the pipeline added complexity with little practical benefit."
       }
     ],
     "next_actions": ["Fix the Go version requirement in README to 1.21.", "Commit all changes."],
     "blockers": []
   }
   ```

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

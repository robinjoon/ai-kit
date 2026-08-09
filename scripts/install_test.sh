#!/bin/sh

set -eu

repo_root=$(CDPATH= cd "$(dirname "$0")/.." && pwd -P)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/ctx-install-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT HUP INT TERM

bin_dir="$test_root/bin"
share_dir="$test_root/share"
claude_dir="$test_root/claude"
codex_dir="$test_root/codex"
marker_value=github.com/robinjoon/ai-kit/ctx-skill-v1

mkdir -p "$share_dir/ctx-handoff" "$claude_dir" "$codex_dir"
printf '%s\n' "$marker_value" > "$share_dir/ctx-handoff/.ctx-installer-managed"
ln -s "$share_dir/ctx-handoff" "$claude_dir/ctx-handoff"
ln -s "$share_dir/ctx-handoff" "$codex_dir/ctx-handoff"

"$repo_root/scripts/install.sh" \
  --bin-dir "$bin_dir" \
  --skill-share-dir "$share_dir" \
  --claude-skills-dir "$claude_dir" \
  --codex-skills-dir "$codex_dir"

[ "$("$bin_dir/ctx" --version)" = "ctx dev" ]

for skill in ctx-start ctx-checkpoint ctx-resume ctx-status; do
  [ -f "$share_dir/$skill/SKILL.md" ]
  [ -L "$claude_dir/$skill" ]
  [ -L "$codex_dir/$skill" ]
done

[ ! -e "$share_dir/ctx-handoff" ]
[ ! -L "$claude_dir/ctx-handoff" ]
[ ! -L "$codex_dir/ctx-handoff" ]

"$repo_root/scripts/install.sh" \
  --bin-dir "$bin_dir" \
  --skill-share-dir "$share_dir" \
  --claude-skills-dir "$claude_dir" \
  --codex-skills-dir "$codex_dir" >/dev/null

printf 'install_test: all tests passed\n'

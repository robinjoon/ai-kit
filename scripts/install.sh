#!/bin/sh

set -eu

usage() {
  cat <<'EOF'
Usage: ./scripts/install.sh [OPTIONS]

Options:
  --bin-dir DIR            CLI directory (default: ~/.local/bin)
  --version VERSION        dev or a stable SemVer (default: dev)
  --skill-share-dir DIR    shared skill directory
  --claude-skills-dir DIR  Claude Code skill directory
  --codex-skills-dir DIR   Codex skill directory
  -h, --help               show help
EOF
}

fail() {
  printf 'install ctx: %s\n' "$*" >&2
  exit 1
}

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd -P)
repo_root=$(CDPATH= cd "$script_dir/.." && pwd -P)
bin_dir=
version=dev
skill_share_dir=
claude_skills_dir=
codex_skills_dir=
skill_names="ctx-start ctx-checkpoint ctx-resume ctx-status"
obsolete_skill_names="ctx-handoff"
marker=.ctx-installer-managed
marker_value=github.com/robinjoon/ai-kit/ctx-skill-mvp
old_marker_value=github.com/robinjoon/ai-kit/ctx-skill-v1

while [ "$#" -gt 0 ]; do
  case "$1" in
    --bin-dir) bin_dir=${2:?}; shift 2 ;;
    --version) version=${2:?}; shift 2 ;;
    --skill-share-dir) skill_share_dir=${2:?}; shift 2 ;;
    --claude-skills-dir) claude_skills_dir=${2:?}; shift 2 ;;
    --codex-skills-dir) codex_skills_dir=${2:?}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

case "$version" in
  dev|[0-9]*.[0-9]*.[0-9]*) ;;
  *) fail "version must be dev or a stable SemVer" ;;
esac

: "${HOME:?HOME must be set}"
[ -n "$bin_dir" ] || bin_dir="$HOME/.local/bin"
[ -n "$skill_share_dir" ] || skill_share_dir="$HOME/.local/share/ctx/skills"
[ -n "$claude_skills_dir" ] || claude_skills_dir="$HOME/.claude/skills"
[ -n "$codex_skills_dir" ] || codex_skills_dir="$HOME/.agents/skills"

command -v go >/dev/null 2>&1 || fail "Go is required"
for directory in "$bin_dir" "$skill_share_dir" "$claude_skills_dir" "$codex_skills_dir"; do
  mkdir -p "$directory"
done

is_managed_skill() {
  directory=$1
  [ ! -L "$directory" ] && [ -f "$directory/$marker" ] || return 1
  installed_marker=$(sed -n '1p' "$directory/$marker")
  [ "$installed_marker" = "$marker_value" ] || [ "$installed_marker" = "$old_marker_value" ]
}

target="$bin_dir/ctx"
if [ -L "$target" ]; then
  fail "refusing to replace symbolic link: $target"
fi
if [ -e "$target" ]; then
  [ -f "$target" ] || fail "refusing to replace non-file: $target"
  go version -m "$target" 2>/dev/null | grep -q 'path[[:space:]]*github.com/robinjoon/ai-kit/cli/cmd/ctx' ||
    fail "refusing to replace unrelated file: $target"
fi

temporary=$(mktemp "$bin_dir/.ctx-install.XXXXXX") || fail "cannot create temporary binary"
cleanup() {
  rm -f "$temporary"
}
trap cleanup EXIT HUP INT TERM

(cd "$repo_root/cli" && go build -trimpath -ldflags "-X main.version=$version" -o "$temporary" ./cmd/ctx)
chmod 0755 "$temporary"
mv "$temporary" "$target"
temporary=

for skill in $skill_names; do
  source_skill="$repo_root/.claude/skills/$skill"
  [ -f "$source_skill/SKILL.md" ] || fail "missing skill source: $skill"
  destination="$skill_share_dir/$skill"
  if [ -e "$destination" ] || [ -L "$destination" ]; then
    is_managed_skill "$destination" || fail "refusing to replace unmanaged skill: $destination"
    rm -rf "$destination"
  fi
  mkdir -p "$destination"
  cp -R "$source_skill/." "$destination/"
  printf '%s\n' "$marker_value" > "$destination/$marker"

  for host_dir in "$claude_skills_dir" "$codex_skills_dir"; do
    host_skill="$host_dir/$skill"
    if [ -L "$host_skill" ]; then
      old_target=$(readlink "$host_skill")
      [ "$old_target" = "$destination" ] || fail "refusing to replace unrelated skill link: $host_skill"
      rm -f "$host_skill"
    elif [ -e "$host_skill" ]; then
      fail "refusing to replace unmanaged skill path: $host_skill"
    fi
    ln -s "$destination" "$host_skill"
  done
done

for skill in $obsolete_skill_names; do
  destination="$skill_share_dir/$skill"
  for host_dir in "$claude_skills_dir" "$codex_skills_dir"; do
    host_skill="$host_dir/$skill"
    if [ -L "$host_skill" ] && [ "$(readlink "$host_skill")" = "$destination" ]; then
      rm -f "$host_skill"
    fi
  done
  if is_managed_skill "$destination"; then
    rm -rf "$destination"
  fi
done

trap - EXIT HUP INT TERM
printf 'Installed ctx %s to %s\n' "$version" "$target"
printf 'Set CTX_BIN=%s and restart Claude Code and Codex if ctx is not on PATH.\n' "$target"

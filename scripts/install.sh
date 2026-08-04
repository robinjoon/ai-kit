#!/bin/sh

set -eu

usage() {
  cat <<'EOF'
Usage: ./scripts/install.sh [OPTIONS]

Build and install ctx and its Agent Skills from this checkout.

Options:
  --bin-dir DIR            Binary install directory (default: ~/.local/bin)
  --version VERSION        Embedded version (default: clean exact SemVer tag or dev)
  --skill-share-dir DIR    Canonical skill directory (default: ~/.local/share/ctx/skills)
  --claude-skills-dir DIR  Claude Code skill directory (default: ~/.claude/skills)
  --codex-skills-dir DIR   Codex skill directory (default: ~/.agents/skills)
  -h, --help               Show this help
EOF
}

fail() {
  printf 'install ctx: %s\n' "$*" >&2
  exit 1
}

is_stable_semver() {
  case "$1" in
    *'
'*)
      return 1
      ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
}

is_supported_version() (
  value=$1
  [ "$value" = dev ] && exit 0
  is_stable_semver "$value" || exit 1
  major=${value%%.*}
  remainder=${value#*.}
  minor=${remainder%%.*}
  [ "$major" -gt 0 ] || [ "$minor" -ge 1 ]
)

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd -P)
repo_root=$(CDPATH= cd "$script_dir/.." && pwd -P)
bin_dir=
version=
skill_share_dir=
claude_skills_dir=
codex_skills_dir=
skill_names="ctx-start ctx-checkpoint ctx-handoff ctx-resume ctx-status"
managed_marker=.ctx-installer-managed
managed_marker_value=github.com/robinjoon/ai-kit/ctx-skill-v1

while [ "$#" -gt 0 ]; do
  case "$1" in
    --bin-dir)
      [ "$#" -ge 2 ] || fail "--bin-dir requires a directory"
      [ -n "$2" ] || fail "--bin-dir requires a non-empty directory"
      bin_dir=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      [ -n "$2" ] || fail "--version requires a non-empty value"
      version=$2
      shift 2
      ;;
    --skill-share-dir)
      [ "$#" -ge 2 ] || fail "--skill-share-dir requires a directory"
      [ -n "$2" ] || fail "--skill-share-dir requires a non-empty directory"
      skill_share_dir=$2
      shift 2
      ;;
    --claude-skills-dir)
      [ "$#" -ge 2 ] || fail "--claude-skills-dir requires a directory"
      [ -n "$2" ] || fail "--claude-skills-dir requires a non-empty directory"
      claude_skills_dir=$2
      shift 2
      ;;
    --codex-skills-dir)
      [ "$#" -ge 2 ] || fail "--codex-skills-dir requires a directory"
      [ -n "$2" ] || fail "--codex-skills-dir requires a non-empty directory"
      codex_skills_dir=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

if [ -z "$bin_dir" ] || [ -z "$skill_share_dir" ] || [ -z "$claude_skills_dir" ] || [ -z "$codex_skills_dir" ]; then
  : "${HOME:?HOME must be set when an install directory option is omitted}"
fi
[ -n "$bin_dir" ] || bin_dir="$HOME/.local/bin"
[ -n "$skill_share_dir" ] || skill_share_dir="$HOME/.local/share/ctx/skills"
[ -n "$claude_skills_dir" ] || claude_skills_dir="$HOME/.claude/skills"
[ -n "$codex_skills_dir" ] || codex_skills_dir="$HOME/.agents/skills"

command -v go >/dev/null 2>&1 || fail "Go 1.26 or newer is required"
[ -f "$repo_root/cli/go.mod" ] || fail "cannot find cli/go.mod from $script_dir"

for skill in $skill_names; do
  source_skill="$repo_root/.claude/skills/$skill"
  [ -f "$source_skill/SKILL.md" ] || fail "cannot find Agent Skill source: $source_skill/SKILL.md"
done

if [ -z "$version" ]; then
  version=dev
  if command -v git >/dev/null 2>&1; then
    checkout_status=$(git -C "$repo_root" status --porcelain --untracked-files=normal 2>/dev/null || printf 'unknown')
    if [ -z "$checkout_status" ]; then
      tag=$(git -C "$repo_root" describe --tags --exact-match HEAD 2>/dev/null || true)
      candidate=${tag#v}
      if is_stable_semver "$candidate"; then
        version=$candidate
      fi
    fi
  fi
fi

is_supported_version "$version" || fail "version must be dev or a stable SemVer of 0.1.0 or newer"

prepare_directory() {
  requested_dir=$1
  if [ -e "$requested_dir" ] || [ -L "$requested_dir" ]; then
    [ -d "$requested_dir" ] || fail "install directory is not a directory: $requested_dir"
  else
    mkdir -p "$requested_dir" || fail "cannot create install directory: $requested_dir"
  fi
  (CDPATH= cd "$requested_dir" && pwd -P) || fail "cannot resolve install directory: $requested_dir"
}

is_managed_skill_directory() {
  managed_skill_dir=$1
  managed_skill_marker="$managed_skill_dir/$managed_marker"
  [ ! -L "$managed_skill_dir" ] && [ -d "$managed_skill_dir" ] &&
    [ ! -L "$managed_skill_marker" ] && [ -f "$managed_skill_marker" ] &&
    [ "$(sed -n '1p' "$managed_skill_marker")" = "$managed_marker_value" ] &&
    [ "$(wc -l < "$managed_skill_marker" | tr -d ' ')" = 1 ]
}

bin_dir=$(prepare_directory "$bin_dir")
skill_share_dir=$(prepare_directory "$skill_share_dir")
claude_skills_dir=$(prepare_directory "$claude_skills_dir")
codex_skills_dir=$(prepare_directory "$codex_skills_dir")

[ "$skill_share_dir" != "$claude_skills_dir" ] || fail "canonical and Claude skill directories must differ"
[ "$skill_share_dir" != "$codex_skills_dir" ] || fail "canonical and Codex skill directories must differ"

bin_dir=$(CDPATH= cd "$bin_dir" && pwd -P)
target="$bin_dir/ctx"

if [ -L "$target" ]; then
  fail "refusing to replace symbolic link: $target"
fi
if [ -e "$target" ]; then
  [ -f "$target" ] || fail "refusing to replace non-regular path: $target"
  existing_metadata=$(go version -m "$target" 2>/dev/null || true)
  printf '%s\n' "$existing_metadata" | grep -Eq '^[[:space:]]*path[[:space:]]+github\.com/robinjoon/ai-kit/cli/cmd/ctx[[:space:]]*$' ||
    fail "refusing to replace unrelated file: $target"
fi

for skill in $skill_names; do
  canonical_skill="$skill_share_dir/$skill"
  if [ -L "$canonical_skill" ]; then
    fail "refusing to replace unmanaged canonical skill link: $canonical_skill"
  fi
  if [ -e "$canonical_skill" ]; then
    is_managed_skill_directory "$canonical_skill" ||
      fail "refusing to replace unmanaged canonical skill directory: $canonical_skill"
  fi

  for host_dir in "$claude_skills_dir" "$codex_skills_dir"; do
    host_skill="$host_dir/$skill"
    if [ -L "$host_skill" ]; then
      existing_link_target=$(readlink "$host_skill")
      if [ "$existing_link_target" != "$canonical_skill" ]; then
        case "$existing_link_target" in
          /*) existing_skill_dir=$existing_link_target ;;
          *) existing_skill_dir="$host_dir/$existing_link_target" ;;
        esac
        is_managed_skill_directory "$existing_skill_dir" ||
          fail "refusing to replace unmanaged skill link: $host_skill"
      fi
    elif [ -e "$host_skill" ]; then
      fail "refusing to replace unmanaged skill path: $host_skill"
    fi
  done
done

skills_staging=$(mktemp -d "$skill_share_dir/.ctx-skills-install.XXXXXX") ||
  fail "cannot create temporary skill directory in $skill_share_dir"
temporary=
rollback_target=
rollback_backup=
rollback_link_path=
rollback_link_target=

cleanup() {
  if [ -n "$rollback_backup" ] && [ -e "$rollback_backup" ] && [ ! -e "$rollback_target" ] && [ ! -L "$rollback_target" ]; then
    mv "$rollback_backup" "$rollback_target" 2>/dev/null || true
  fi
  if [ -n "$rollback_link_path" ] && [ ! -e "$rollback_link_path" ] && [ ! -L "$rollback_link_path" ]; then
    ln -s "$rollback_link_target" "$rollback_link_path" 2>/dev/null || true
  fi
  [ -n "$temporary" ] && rm -f "$temporary"
  [ -n "$skills_staging" ] && rm -rf "$skills_staging"
}
interrupted() {
  trap - 0 HUP INT TERM
  cleanup
  exit 1
}
trap cleanup 0
trap interrupted HUP INT TERM

for skill in $skill_names; do
  staged_skill="$skills_staging/$skill"
  mkdir "$staged_skill" || fail "cannot stage Agent Skill: $skill"
  cp -R "$repo_root/.claude/skills/$skill/." "$staged_skill/" || fail "cannot copy Agent Skill: $skill"
  printf '%s\n' "$managed_marker_value" > "$staged_skill/$managed_marker" ||
    fail "cannot mark managed Agent Skill: $skill"
done

temporary=$(mktemp "$bin_dir/.ctx-install.XXXXXX") || fail "cannot create a temporary binary in $bin_dir"

(
  cd "$repo_root/cli"
  go build -trimpath -ldflags "-X main.version=$version" -o "$temporary" ./cmd/ctx
)
chmod 0755 "$temporary"

actual_version=$("$temporary" --version)
[ "$actual_version" = "ctx $version" ] || fail "built binary reported an unexpected version: $actual_version"

for skill in $skill_names; do
  canonical_skill="$skill_share_dir/$skill"
  staged_skill="$skills_staging/$skill"
  rollback_backup="$skills_staging/.backup-$skill"
  rollback_target=$canonical_skill
  if [ -e "$canonical_skill" ]; then
    mv "$canonical_skill" "$rollback_backup" || fail "cannot prepare Agent Skill update: $canonical_skill"
  else
    rollback_backup=
  fi

  if ! mv "$staged_skill" "$canonical_skill"; then
    fail "cannot install Agent Skill: $canonical_skill"
  fi
  if [ -n "$rollback_backup" ]; then
    rm -rf "$rollback_backup"
  fi
  rollback_target=
  rollback_backup=
done

for skill in $skill_names; do
  canonical_skill="$skill_share_dir/$skill"
  for host_dir in "$claude_skills_dir" "$codex_skills_dir"; do
    host_skill="$host_dir/$skill"
    if [ -L "$host_skill" ] && [ "$(readlink "$host_skill")" != "$canonical_skill" ]; then
      rollback_link_path=$host_skill
      rollback_link_target=$(readlink "$host_skill")
      rm -f "$host_skill" || fail "cannot replace Agent Skill link: $host_skill"
      ln -s "$canonical_skill" "$host_skill" || fail "cannot link Agent Skill: $host_skill"
      rollback_link_path=
      rollback_link_target=
    elif [ ! -L "$host_skill" ]; then
      ln -s "$canonical_skill" "$host_skill" || fail "cannot link Agent Skill: $host_skill"
    fi
  done
done

mv -f "$temporary" "$target"
temporary=
trap - 0 HUP INT TERM
cleanup

printf 'Installed ctx %s to %s\n' "$version" "$target"
printf 'Installed Agent Skills to %s\n' "$skill_share_dir"
printf 'Linked Agent Skills from %s and %s\n' "$claude_skills_dir" "$codex_skills_dir"
resolved_ctx=$(command -v ctx 2>/dev/null || true)
if [ -n "$resolved_ctx" ] && [ "$resolved_ctx" -ef "$target" ]; then
  printf 'Run: ctx --version\n'
else
  if [ -n "$resolved_ctx" ]; then
    printf '\nPATH currently resolves ctx to %s instead of the installed binary.\n' "$resolved_ctx"
  else
    printf '\nPATH does not currently resolve the installed binary.\n'
  fi
  printf 'Add %s to PATH or set CTX_BIN to %s in the app launch environment.\n' "$bin_dir" "$target"
fi
printf 'If an already-running app cannot resolve ctx, fully restart it after updating its launch environment.\n'

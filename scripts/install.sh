#!/bin/sh

set -eu

usage() {
  cat <<'EOF'
Usage: ./scripts/install.sh [--bin-dir DIR] [--version VERSION]

Build and install ctx from this checkout.

Options:
  --bin-dir DIR      Install directory (default: ~/.local/bin)
  --version VERSION  Version embedded in the binary (default: clean exact SemVer tag or dev)
  -h, --help         Show this help
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
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

if [ -z "$bin_dir" ]; then
  : "${HOME:?HOME must be set when --bin-dir is omitted}"
  bin_dir="$HOME/.local/bin"
fi

command -v go >/dev/null 2>&1 || fail "Go 1.26 or newer is required"
[ -f "$repo_root/cli/go.mod" ] || fail "cannot find cli/go.mod from $script_dir"

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

mkdir -p "$bin_dir" || fail "cannot create install directory: $bin_dir"
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

temporary=$(mktemp "$bin_dir/.ctx-install.XXXXXX") || fail "cannot create a temporary binary in $bin_dir"

cleanup() {
  rm -f "$temporary"
}
interrupted() {
  cleanup
  exit 1
}
trap cleanup 0
trap interrupted HUP INT TERM

(
  cd "$repo_root/cli"
  go build -trimpath -ldflags "-X main.version=$version" -o "$temporary" ./cmd/ctx
)
chmod 0755 "$temporary"

actual_version=$("$temporary" --version)
[ "$actual_version" = "ctx $version" ] || fail "built binary reported an unexpected version: $actual_version"

mv -f "$temporary" "$target"
trap - 0 HUP INT TERM

printf 'Installed ctx %s to %s\n' "$version" "$target"
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

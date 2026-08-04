#!/bin/sh

set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd -P)
repo_root=$(CDPATH= cd "$script_dir/.." && pwd -P)
installer="$repo_root/scripts/install.sh"
skill_names="ctx-start ctx-checkpoint ctx-handoff ctx-resume ctx-status"

test_root=$(mktemp -d "${TMPDIR:-/tmp}/ctx-install-test.XXXXXX")
test_root=$(CDPATH= cd "$test_root" && pwd -P)
fake_bin="$test_root/fake-bin"
mkdir -p "$fake_bin"

cleanup() {
  rm -rf "$test_root"
}
trap cleanup 0 HUP INT TERM

fail_test() {
  printf 'install_test: %s\n' "$*" >&2
  exit 1
}

assert_file() {
  [ -f "$1" ] || fail_test "expected file: $1"
}

assert_absent() {
  [ ! -e "$1" ] && [ ! -L "$1" ] || fail_test "expected absent path: $1"
}

assert_link_target() {
  link_path=$1
  expected_target=$2
  [ -L "$link_path" ] || fail_test "expected symbolic link: $link_path"
  actual_target=$(readlink "$link_path")
  [ "$actual_target" = "$expected_target" ] ||
    fail_test "unexpected link target for $link_path: $actual_target"
}

run_install() {
  install_home=$1
  shift
  HOME="$install_home" PATH="$fake_bin:/usr/bin:/bin" "$installer" "$@"
}

expect_install_failure() {
  install_home=$1
  shift
  if run_install "$install_home" "$@" > "$test_root/unexpected.stdout" 2> "$test_root/expected.stderr"; then
    fail_test "installer unexpectedly succeeded"
  fi
}

cat > "$fake_bin/go" <<'EOF'
#!/bin/sh
set -eu

case "${1-}" in
  version)
    [ "${2-}" = -m ] || exit 1
    target=${3-}
    grep -q '^# fake-ctx-module$' "$target" 2>/dev/null || exit 1
    printf '%s:\n' "$target"
    printf '\tpath\tgithub.com/robinjoon/ai-kit/cli/cmd/ctx\n'
    ;;
  build)
    shift
    output=
    flags=
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -o)
          output=$2
          shift 2
          ;;
        -ldflags)
          flags=$2
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    [ -n "$output" ] || exit 1
    version=${flags##*main.version=}
    [ -n "$version" ] || exit 1
    {
      printf '#!/bin/sh\n'
      printf '# fake-ctx-module\n'
      printf "printf '%%s\\\\n' 'ctx %s'\n" "$version"
    } > "$output"
    ;;
  *)
    exit 1
    ;;
esac
EOF
chmod 0755 "$fake_bin/go"

cat > "$fake_bin/git" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$fake_bin/git"

default_home="$test_root/default-home"
mkdir -p "$default_home"
run_install "$default_home" --version 0.1.0 > "$test_root/default.stdout"

default_bin="$default_home/.local/bin/ctx"
default_share="$default_home/.local/share/ctx/skills"
assert_file "$default_bin"
[ "$("$default_bin" --version)" = "ctx 0.1.0" ] || fail_test "installed binary has the wrong version"

for skill in $skill_names; do
  canonical="$default_share/$skill"
  assert_file "$canonical/SKILL.md"
  assert_file "$canonical/agents/openai.yaml"
  assert_file "$canonical/.ctx-installer-managed"
  cmp "$repo_root/.claude/skills/$skill/SKILL.md" "$canonical/SKILL.md" >/dev/null ||
    fail_test "canonical skill differs from source: $skill"
  assert_link_target "$default_home/.claude/skills/$skill" "$canonical"
  assert_link_target "$default_home/.agents/skills/$skill" "$canonical"
done
assert_file "$default_share/ctx-checkpoint/references/capture-input.md"
assert_file "$default_share/ctx-handoff/references/capture-input.md"

printf 'stale\n' > "$default_share/ctx-start/SKILL.md"
printf 'obsolete\n' > "$default_share/ctx-start/obsolete.txt"
run_install "$default_home" --version 0.1.1 > "$test_root/update.stdout"
cmp "$repo_root/.claude/skills/ctx-start/SKILL.md" "$default_share/ctx-start/SKILL.md" >/dev/null ||
  fail_test "managed skill was not refreshed on reinstall"
assert_absent "$default_share/ctx-start/obsolete.txt"
[ "$("$default_bin" --version)" = "ctx 0.1.1" ] || fail_test "binary was not updated on reinstall"

custom_home="$test_root/custom-home"
custom_bin="$test_root/custom bin"
custom_share="$test_root/custom share"
custom_claude="$test_root/custom claude"
custom_codex="$test_root/custom codex"
mkdir -p "$custom_home"
run_install "$custom_home" --version dev \
  --bin-dir "$custom_bin" \
  --skill-share-dir "$custom_share" \
  --claude-skills-dir "$custom_claude" \
  --codex-skills-dir "$custom_codex" > "$test_root/custom.stdout"
assert_file "$custom_bin/ctx"
for skill in $skill_names; do
  assert_link_target "$custom_claude/$skill" "$custom_share/$skill"
  assert_link_target "$custom_codex/$skill" "$custom_share/$skill"
done

relocate_home="$test_root/relocate-home"
relocate_bin="$test_root/relocate-bin"
relocate_share_one="$test_root/relocate-share-one"
relocate_share_two="$test_root/relocate-share-two"
relocate_claude="$test_root/relocate-claude"
relocate_codex="$test_root/relocate-codex"
mkdir -p "$relocate_home"
run_install "$relocate_home" --version dev \
  --bin-dir "$relocate_bin" \
  --skill-share-dir "$relocate_share_one" \
  --claude-skills-dir "$relocate_claude" \
  --codex-skills-dir "$relocate_codex" > "$test_root/relocate-one.stdout"
run_install "$relocate_home" --version dev \
  --bin-dir "$relocate_bin" \
  --skill-share-dir "$relocate_share_two" \
  --claude-skills-dir "$relocate_claude" \
  --codex-skills-dir "$relocate_codex" > "$test_root/relocate-two.stdout"
for skill in $skill_names; do
  assert_link_target "$relocate_claude/$skill" "$relocate_share_two/$skill"
  assert_link_target "$relocate_codex/$skill" "$relocate_share_two/$skill"
done

unmanaged_share_home="$test_root/unmanaged-share-home"
unmanaged_share="$unmanaged_share_home/.local/share/ctx/skills/ctx-start"
mkdir -p "$unmanaged_share"
printf 'keep\n' > "$unmanaged_share/sentinel"
expect_install_failure "$unmanaged_share_home" --version dev
[ "$(sed -n '1p' "$unmanaged_share/sentinel")" = keep ] || fail_test "unmanaged canonical skill was changed"
assert_absent "$unmanaged_share_home/.local/bin/ctx"

unmanaged_host_home="$test_root/unmanaged-host-home"
unmanaged_host="$unmanaged_host_home/.claude/skills/ctx-start"
mkdir -p "$unmanaged_host"
printf 'keep\n' > "$unmanaged_host/sentinel"
expect_install_failure "$unmanaged_host_home" --version dev
[ "$(sed -n '1p' "$unmanaged_host/sentinel")" = keep ] || fail_test "unmanaged host skill was changed"
assert_absent "$unmanaged_host_home/.local/share/ctx/skills/ctx-start"

foreign_link_home="$test_root/foreign-link-home"
foreign_target="$test_root/foreign-skill"
mkdir -p "$foreign_link_home/.agents/skills" "$foreign_target"
ln -s "$foreign_target" "$foreign_link_home/.agents/skills/ctx-start"
expect_install_failure "$foreign_link_home" --version dev
assert_link_target "$foreign_link_home/.agents/skills/ctx-start" "$foreign_target"
assert_absent "$foreign_link_home/.local/share/ctx/skills/ctx-start"

unrelated_binary_home="$test_root/unrelated-binary-home"
mkdir -p "$unrelated_binary_home/.local/bin"
printf 'do not replace\n' > "$unrelated_binary_home/.local/bin/ctx"
expect_install_failure "$unrelated_binary_home" --version dev
[ "$(sed -n '1p' "$unrelated_binary_home/.local/bin/ctx")" = "do not replace" ] ||
  fail_test "unrelated binary was changed"

printf 'install_test: all tests passed\n'

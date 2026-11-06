#!/usr/bin/env bash
#
# Tests script/release-notes.sh and script/prepare-release.sh on a sample
# changelog, script/homebrew-formula.sh on a sample checksums.txt, and checks
# that the real CHANGELOG.md can be read. Usage:
#
#   ./script/release-scripts-test.sh
set -euo pipefail

cd "$(dirname "$0")/.."
NOTES=$PWD/script/release-notes.sh
PREPARE=$PWD/script/prepare-release.sh
BREW=$PWD/script/homebrew-formula.sh
WORK=$(mktemp -d "${TMPDIR:-/tmp}/droot-release-test.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_eq() { # name actual expected
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
  echo "ok - $1"
}
assert_fails() { # name command...
  local name=$1; shift
  if "$@" >/dev/null 2>&1; then fail "$name: succeeded"; fi
  echo "ok - $name"
}

CL=$WORK/CHANGELOG.md
cat >"$CL" <<'EOF'
# Changelog

## [Unreleased]

### Fixed

- Something.

## [0.7.8] - 2020-01-02

- Older.

[Unreleased]: https://github.com/yuuki/droot/compare/v0.7.8...HEAD
[0.7.8]: https://github.com/yuuki/droot/releases/tag/v0.7.8
EOF

assert_eq "notes of Unreleased" "$("$NOTES" Unreleased "$CL")" $'### Fixed\n\n- Something.'
assert_eq "notes of a dated release, link references excluded" "$("$NOTES" v0.7.8 "$CL")" "- Older."
assert_eq "a pre-release falls back to Unreleased" "$("$NOTES" v0.8.0-rc.1 "$CL")" $'### Fixed\n\n- Something.'
assert_fails "notes of a missing release" "$NOTES" 0.9.0 "$CL"
assert_fails "notes of a non-semver tag" "$NOTES" v1.2 "$CL"

assert_fails "prepare a pre-release" "$PREPARE" 0.8.0-rc.1 "$CL"
assert_fails "prepare an older version" "$PREPARE" 0.7.1 "$CL"
assert_fails "prepare an existing version" "$PREPARE" 0.7.8 "$CL"
RELEASE_DATE=2026-11-05 "$PREPARE" v0.8.0 "$CL" >/dev/null
assert_eq "prepared changelog" "$(cat "$CL")" "$(cat <<'EOF'
# Changelog

## [Unreleased]

## [0.8.0] - 2026-11-05

### Fixed

- Something.

## [0.7.8] - 2020-01-02

- Older.

[Unreleased]: https://github.com/yuuki/droot/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/yuuki/droot/releases/tag/v0.8.0
[0.7.8]: https://github.com/yuuki/droot/releases/tag/v0.7.8
EOF
)"
assert_eq "notes of the prepared release" "$("$NOTES" 0.8.0 "$CL")" $'### Fixed\n\n- Something.'
assert_fails "notes of an empty Unreleased" "$NOTES" Unreleased "$CL"
assert_fails "prepare with an empty Unreleased" "$PREPARE" 0.8.1 "$CL"

# Without link references (like the first release made with these scripts).
printf '# Changelog\n\n## [Unreleased]\n\n- New.\n' >"$CL"
RELEASE_DATE=2026-11-05 "$PREPARE" 0.8.0 "$CL" >/dev/null
assert_eq "link references are added" "$(tail -n 3 "$CL")" "$(printf '%s\n' "" \
  "[Unreleased]: https://github.com/yuuki/droot/compare/v0.8.0...HEAD" \
  "[0.8.0]: https://github.com/yuuki/droot/releases/tag/v0.8.0")"

printf '# Changelog\n\n## [0.8.0]\n\n- Undated.\n' >"$CL"
assert_fails "notes of an undated release" "$NOTES" 0.8.0 "$CL"

# The Homebrew formula, from a checksums.txt like GoReleaser's.
SUMS=$WORK/checksums.txt
i=0
for asset in droot_darwin_amd64 droot_darwin_amd64.zip droot_darwin_arm64.zip \
  droot_linux_amd64.tar.gz droot_linux_arm64.tar.gz droot_linux_armv7.tar.gz; do
  i=$((i + 1))
  printf '%064x  %s\n' "$i" "$asset"
done >"$SUMS"
FORMULA=$WORK/droot.rb
"$BREW" v0.8.0 "$SUMS" >"$FORMULA"
assert_eq "formula version" "$(grep -E '^  version ' "$FORMULA")" '  version "0.8.0"'
assert_eq "formula archives" "$(grep -E '^ +(url|sha256) ' "$FORMULA" | sed 's/^ *//')" "$(cat <<'EOF'
url "https://github.com/yuuki/droot/releases/download/v0.8.0/droot_darwin_arm64.zip"
sha256 "0000000000000000000000000000000000000000000000000000000000000003"
url "https://github.com/yuuki/droot/releases/download/v0.8.0/droot_darwin_amd64.zip"
sha256 "0000000000000000000000000000000000000000000000000000000000000002"
url "https://github.com/yuuki/droot/releases/download/v0.8.0/droot_linux_arm64.tar.gz"
sha256 "0000000000000000000000000000000000000000000000000000000000000005"
url "https://github.com/yuuki/droot/releases/download/v0.8.0/droot_linux_amd64.tar.gz"
sha256 "0000000000000000000000000000000000000000000000000000000000000004"
url "https://github.com/yuuki/droot.git", branch: "master"
EOF
)"
if command -v ruby >/dev/null; then
  ruby -c "$FORMULA" >/dev/null || fail "the formula is not valid Ruby"
  echo "ok - the formula is valid Ruby"
fi
assert_fails "formula of a pre-release" "$BREW" 0.8.0-rc.1 "$SUMS"
grep -v linux_arm64 "$SUMS" >"$WORK/missing.txt"
assert_fails "formula with a missing archive" "$BREW" 0.8.0 "$WORK/missing.txt"
sed 's/^0*5 /xyz /' "$SUMS" >"$WORK/bad.txt"
assert_fails "formula with a malformed checksum" "$BREW" 0.8.0 "$WORK/bad.txt"

"$NOTES" Unreleased CHANGELOG.md >/dev/null 2>&1 || "$NOTES" "$(sed -n 's/^## \[\([0-9][^]]*\)\] - .*/\1/p' CHANGELOG.md | head -n 1)" >/dev/null ||
  fail "CHANGELOG.md has neither Unreleased entries nor a dated release"
echo "ok - CHANGELOG.md is readable"

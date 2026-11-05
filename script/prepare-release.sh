#!/usr/bin/env bash
#
# Prepares CHANGELOG.md for a release: moves the entries of the "Unreleased"
# section to a new "## [VERSION] - DATE" section and updates the link
# reference definitions at the end of the file. Commit the result, get it
# merged, then push the tag (see documentation/development.md#releasing).
#
#   script/prepare-release.sh VERSION [CHANGELOG]
#
# VERSION is a semantic version (0.8.0 or v0.8.0) newer than the last one in
# the changelog. Pre-releases (0.8.0-rc.1) need no preparation: tag them
# directly and they get the "Unreleased" section as release notes. DATE is
# today (UTC) unless RELEASE_DATE=YYYY-MM-DD is set.
set -euo pipefail

die() { echo "prepare-release: $*" >&2; exit 1; }
[[ $# -ge 1 && $# -le 2 ]] || { echo "usage: $0 VERSION [CHANGELOG]" >&2; exit 2; }

version=${1#v}
changelog=${2:-CHANGELOG.md}
date=${RELEASE_DATE:-$(date -u +%Y-%m-%d)}
repo=https://github.com/yuuki/droot
here=$(dirname "$0")

[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
  die "not a release version (MAJOR.MINOR.PATCH): $1"
[[ $date =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "RELEASE_DATE is not YYYY-MM-DD: $date"
[[ -f $changelog ]] || die "$changelog: no such file"
grep -qx '## \[Unreleased\]' "$changelog" || die "$changelog has no \"## [Unreleased]\" heading"
! grep -q "^## \[$version\]" "$changelog" || die "$changelog already has a section for $version"
"$here/release-notes.sh" Unreleased "$changelog" >/dev/null 2>&1 ||
  die "the Unreleased section of $changelog is empty: nothing to release"

previous=$(sed -n 's/^## \[\([0-9][0-9.]*\)\] - .*/\1/p' "$changelog" | head -n 1)
if [[ -n $previous ]]; then
  newest=$(printf '%s\n%s\n' "$previous" "$version" | sort -V | tail -n 1)
  [[ $version != "$previous" && $newest == "$version" ]] ||
    die "$version is not newer than $previous, the last release in $changelog"
fi

tmp=$(mktemp "$changelog.XXXXXX")
trap 'rm -f "$tmp"' EXIT
awk -v version="$version" -v date="$date" -v repo="$repo" '
  BEGIN {
    unreleased = "[Unreleased]: " repo "/compare/v" version "...HEAD"
    tag = "[" version "]: " repo "/releases/tag/v" version
  }
  $0 == "## [Unreleased]" {
    print; print ""; print "## [" version "] - " date
    next
  }
  /^\[Unreleased\]: / { print unreleased; print tag; linked = 1; last = tag; next }
  { print }
  { last = $0 }
  END {
    if (!linked) {
      # Link reference definitions go last, after a blank line.
      if (last != "") print ""
      print unreleased; print tag
    }
  }
' "$changelog" >"$tmp"
# Keep the mode of the original file.
cat "$tmp" >"$changelog"

echo "CHANGELOG: moved the Unreleased entries to [$version] - $date."
echo "Review the section, then commit, open a pull request and, once merged, tag it:"
echo "  git tag -s v$version -m v$version && git push origin v$version"

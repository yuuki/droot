#!/usr/bin/env bash
#
# Prints the release notes of a version: the body of its section in
# CHANGELOG.md, without the heading. The release workflow publishes them as
# the description of the GitHub release.
#
#   script/release-notes.sh VERSION [CHANGELOG]
#
# VERSION is a tag (v0.8.0), a version (0.8.0) or "Unreleased". A release
# needs a dated section ("## [0.8.0] - 2026-11-05", see
# script/prepare-release.sh); a pre-release (0.8.0-rc.1) without a section of
# its own gets the "Unreleased" section. Fails if the section is missing or
# empty.
set -euo pipefail

die() { echo "release-notes: $*" >&2; exit 1; }
[[ $# -ge 1 && $# -le 2 ]] || { echo "usage: $0 VERSION [CHANGELOG]" >&2; exit 2; }

version=${1#v}
changelog=${2:-CHANGELOG.md}
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
[[ $version == Unreleased || $version =~ $semver ]] || die "not a semantic version: $1"
[[ -f $changelog ]] || die "$changelog: no such file"

# section NAME: print the body of "## [NAME]" (exit 3 if there is none, 4 if
# a version section has no date), without leading and trailing blank lines.
# The body ends at the next "## " heading or at the link reference
# definitions ("[0.8.0]: https://...") at the end of the file.
section() {
  awk -v name="$1" '
    BEGIN { head = "## [" name "]" }
    /^## / {
      if (found) exit
      if (index($0, head) != 1) next
      rest = substr($0, length(head) + 1)
      if (name == "Unreleased" && rest == "") { found = 1; next }
      if (rest ~ /^ - [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/) { found = 1; next }
      if (rest == "") { undated = 1; exit }
      next
    }
    found && /^\[[^]]+\]: / { exit }
    found { lines[++n] = $0 }
    END {
      if (undated) exit 4
      if (!found) exit 3
      first = 1; while (first <= n && lines[first] ~ /^[ \t]*$/) first++
      last = n; while (last >= first && lines[last] ~ /^[ \t]*$/) last--
      for (i = first; i <= last; i++) print lines[i]
    }
  ' "$changelog"
}

status=0
notes=$(section "$version") || status=$?
if [[ $status -eq 3 && $version == *-* ]]; then
  status=0
  notes=$(section Unreleased) || status=$?
  version="$version (Unreleased)"
fi
case $status in
  0) ;;
  3) die "$changelog has no section for $version (run script/prepare-release.sh $version)" ;;
  4) die "the section of $version in $changelog has no date (\"## [$version] - YYYY-MM-DD\")" ;;
  *) die "cannot read $changelog" ;;
esac
[[ -n $notes ]] || die "the section of $version in $changelog is empty"
printf '%s\n' "$notes"

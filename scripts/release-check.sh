#!/usr/bin/env bash
# The release checks that need no network (specs/release-v0.1/SPEC.md R4),
# run by .github/workflows/release.yml and by a person preparing a release:
# the tag is v + a Semantic Versioning 2.0.0 version without build metadata,
# CHANGELOG.md has a dated section for it, and that section is not empty.
# The section's text is written to <notes-file>.
# Usage: scripts/release-check.sh <tag> <notes-file>
# The changelog is $CHANGELOG (default CHANGELOG.md in the repository root).
# Exit 0 when every check passes; otherwise one "release-check: <reason>"
# line on standard error and exit 1.
set -euo pipefail

die() {
  echo "release-check: $*" >&2
  exit 1
}

[ "$#" -eq 2 ] || die "usage: scripts/release-check.sh <tag> <notes-file>"
tag="$1"
notes="$2"
changelog="${CHANGELOG:-$(dirname "$0")/../CHANGELOG.md}"

id='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
semver="^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-${id}(\.${id})*)?$"
[[ "$tag" =~ $semver ]] || die "tag $tag is not v followed by a semantic version (vMAJOR.MINOR.PATCH[-PRERELEASE], no leading zeros)"

[ -f "$changelog" ] || die "no changelog at $changelog"

# the heading, with the tag's dots matched literally
heading="^## \\[${tag//./\\.}\\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$"
grep -Eq "$heading" "$changelog" || die "$changelog has no heading \"## [$tag] - YYYY-MM-DD\""
[ "$(grep -Ec "$heading" "$changelog")" -eq 1 ] || die "$changelog has more than one heading for $tag"

# the section: after the heading, up to the next "## " heading or the
# link-reference block
# (the heading line is matched by string equality, which needs no regular
# expression dialect shared between grep and awk)
line="$(grep -E "$heading" "$changelog")"
section="$(HEADING="$line" awk '
  found && (/^## / || /^\[[^]]+\]: /) { exit }
  found { print }
  $0 == ENVIRON["HEADING"] { found = 1 }
' "$changelog")"
[ -n "$(printf '%s' "$section" | tr -d '[:space:]')" ] || die "the $tag section of $changelog is empty"

# trim leading and trailing blank lines
printf '%s\n' "$section" | awk 'NF { started = 1 } started' | awk '{ lines[NR] = $0 } NF { last = NR } END { for (i = 1; i <= last; i++) print lines[i] }' > "$notes"
echo "release-check: $tag ok, notes in $notes"

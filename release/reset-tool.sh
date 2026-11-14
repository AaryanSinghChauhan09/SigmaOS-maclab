#!/usr/bin/env bash
# Build the reset tool from its pinned source: release/reset-tool.sh OUTDIR
#
# Reads the line of macvdmtool in deploy/pins: the commit, the sha256 of
# `git archive --format=tar <commit>`, the repository. Fetches the
# repository, refuses it unless the commit is there and its source has that
# checksum, builds it with make and leaves OUTDIR/macvdmtool. This runs on
# the release workflow's macOS runner, the one place the tool is built: the
# mini compiles nothing. release/build.sh puts the file into the server
# bundle (RESET_TOOL).
#
# RESET_TOOL_PINS names another pins file, which may name a repository on
# this machine (file:///...): the tests'.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=release/lib.sh
. "$repo/release/lib.sh"
die() { echo "release/reset-tool.sh: $*" >&2; exit 1; }

[[ $# == 1 ]] || die "usage: release/reset-tool.sh OUTDIR"
pins="${RESET_TOOL_PINS:-$repo/deploy/pins}"
[[ -f $pins ]] || die "no pins file $pins"
read -r name commit sum url rest < <(sed -e 's/#.*//' "$pins" | awk '$1 == "macvdmtool"'; echo) || true
[[ ${name:-} == macvdmtool ]] || die "$pins has no line for macvdmtool"
[[ -z ${rest:-} ]] || die "$pins: the line of macvdmtool is not: name commit sha256 url"
[[ $commit =~ ^[0-9a-f]{40}$ ]] || die "$pins: macvdmtool's version is not a whole commit id"
[[ $sum =~ ^[0-9a-f]{64}$ ]] || die "$pins: macvdmtool's checksum is not a sha256"
case "$url" in
  https://*) ;;
  file:///*) [[ -n ${RESET_TOOL_PINS:-} ]] || die "$pins: $url is not https" ;;
  *) die "$pins: $url is not https" ;;
esac

out="$(mkdir -p "$1" && cd "$1" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
src="$work/macvdmtool"

git clone --quiet --no-checkout "$url" "$src" || die "could not fetch $url"
git -C "$src" cat-file -e "$commit^{commit}" 2>/dev/null || die "$url has no commit $commit: nothing was built"
git -C "$src" -c advice.detachedHead=false checkout --quiet --detach "$commit"
[[ "$(git -C "$src" rev-parse HEAD)" == "$commit" ]] || die "the checkout is not $commit: nothing was built"
got="$(git -C "$src" -c tar.umask=0002 -c core.autocrlf=false archive --format=tar "$commit" | sha256)"
[[ $got == "$sum" ]] || die "the source at $commit has the checksum $got, not the pinned $sum: nothing was built"
[[ -z "$(git -C "$src" status --porcelain)" ]] || die "the checkout is not the commit's own files: nothing was built"

make -C "$src" macvdmtool >&2
macho_arm64 "$src/macvdmtool" || die "what was built is not a program for macOS on arm64"
rm -f "$out/macvdmtool"
cp "$src/macvdmtool" "$out/macvdmtool"
chmod 0755 "$out/macvdmtool"
echo "built macvdmtool at $commit: $out/macvdmtool, sha256 $(sha256 < "$out/macvdmtool")"

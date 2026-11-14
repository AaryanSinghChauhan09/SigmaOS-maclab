#!/usr/bin/env bash
# Make a Lab release on this machine: release/local.sh TAG
#
# Does what .github/workflows/release.yml does (build every file, sign
# lab-agent and then every file, verify as the install lines and Deploy do),
# with one difference: the key is made here in a temporary folder and thrown
# away. Nothing is tagged, nothing leaves this machine and the release key is
# never read.
#
#   dist/release/TAG/                  the Lab release: every file and its .sig
#   dist/release/TAG.allowed_signers   the public half of the key that signed
#                                      it; this build's lab-agent carries it
#
# Whoever installs this build pins that file in place of release/allowed_signers.
# Running it again signs with a new key. The test tool's tarball has no GPU
# helper unless OMARCHY_GPU_BINARY_DIR names one built on AArch64 Linux.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=release/lib.sh
. "$repo/release/lib.sh"
die() { echo "release/local.sh: $*" >&2; exit 1; }

[[ $# == 1 ]] || die "usage: release/local.sh TAG   (vX.Y.Z or vX.Y.Z-rc.N)"
tag="$1"
[[ $tag =~ $RELEASE_TAG ]] || die "$tag is not a Lab release's tag (vX.Y.Z or vX.Y.Z-rc.N)"

t="$(mktemp -d)"
trap 'rm -rf "$t"' EXIT
ssh-keygen -q -t ed25519 -N '' -C "throwaway key for a local $tag" -f "$t/key"
printf '%s namespaces="%s" %s\n' "$RELEASE_SIGNER" "$RELEASE_NAMESPACE" "$(cut -d' ' -f1,2 "$t/key.pub")" > "$t/allowed_signers"

LAB_RELEASE="$tag" RELEASE_ALLOWED_SIGNERS="$t/allowed_signers" "$repo/release/build.sh" "$t/dist"
"$repo/release/sign.sh" "$t/dist" "$t/key"
rm -f "$t/key"
"$repo/release/verify.sh" "$t/dist" "$t/allowed_signers" "$tag"

out="$repo/dist/release"
mkdir -p "$out"
rm -rf "${out:?}/$tag" "${out:?}/$tag.allowed_signers"
mv "$t/dist" "$out/$tag"
install -m 0644 "$t/allowed_signers" "$out/$tag.allowed_signers"

echo
echo "A local $tag, signed with a throwaway key:"
(cd "$out/$tag" && for f in "${RELEASE_FILES[@]}"; do printf '  %10s  %s\n' "$(wc -c < "$f" | tr -d ' ')" "$f"; done)
echo "in $out/$tag, each with its .sig"
echo "pin: $out/$tag.allowed_signers"

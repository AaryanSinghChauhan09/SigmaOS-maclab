# What a Lab release is made of and how it is signed. Sourced by the scripts
# beside it; the two install scripts and Deploy verify the same way.
# shellcheck shell=bash

RELEASE_SIGNER="release@omarchy-m-testing.org"
RELEASE_NAMESPACE="omarchy-m-test-release"
RELEASE_BUNDLE="lab-server-darwin-arm64.tar.gz"
RELEASE_FILES=(omarchy-m-test.tar.gz VERSION lab-darwin-arm64 lab-linux-arm64 lab-linux-amd64 "$RELEASE_BUNDLE")
RELEASE_AGENT="lab-server/bin/lab-agent-linux-arm64"
RELEASE_TAG='^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$'

# GNU install and tar: Homebrew keeps them out of PATH on macOS.
gnu_tools() {
  local d
  for d in coreutils gnu-tar; do
    if [[ -d /opt/homebrew/opt/$d/libexec/gnubin ]]; then PATH="/opt/homebrew/opt/$d/libexec/gnubin:$PATH"; fi
  done
  export PATH
  tar --version 2>/dev/null | grep -q 'GNU tar' || { echo "the release scripts need GNU tar and install (macOS: brew install gnu-tar coreutils)" >&2; return 1; }
}

# pack PARENT NAME EPOCH: PARENT/NAME as a tarball on stdout, the same bytes every time.
pack() { tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$3" -C "$1" -cf - "$2" | gzip -9n; }

# sha256: the checksum of standard input.
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | awk '{ print $1 }'
}

# macho_arm64 FILE: whether FILE is a program for macOS on arm64.
macho_arm64() { [[ "$(od -An -tx1 -N8 "$1" 2>/dev/null | tr -d ' \n')" == cffaedfe0c000001 ]]; }

# release_key FILE: the key of an allowed_signers file, as "type base64".
release_key() { awk '!/^#/ && NF { print $(NF-1), $NF; exit }' "$1"; }

# sign KEY FILE: FILE.sig, made new.
sign() {
  rm -f "$2.sig"
  ssh-keygen -q -Y sign -f "$1" -n "$RELEASE_NAMESPACE" "$2" </dev/null
}

# verified ALLOWED_SIGNERS FILE: whether FILE.sig is the release key's signature of FILE.
verified() {
  ssh-keygen -Y verify -f "$1" -I "$RELEASE_SIGNER" -n "$RELEASE_NAMESPACE" -s "$2.sig" < "$2" >/dev/null 2>&1
}

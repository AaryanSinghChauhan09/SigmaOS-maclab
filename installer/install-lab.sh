#!/usr/bin/env bash
# Install or update lab, the command line of Omarchy Mac Lab.
#
#   curl -fsSL https://lab.omarchy-mac.org/install/lab | bash
#
# Downloads the latest Lab release's lab for this machine (macOS on Apple
# Silicon, Linux on arm64 or x86-64), verifies its signature against the
# release key pinned below (ssh-keygen -Y verify) and installs it as
# ~/.local/bin/lab. Running it again updates in place. Nothing else changes,
# and nothing needs root.
#
# A lab already at ~/.local/bin/lab that is not Omarchy Mac Lab's (a build of
# maclab's own, for another lab) is never replaced: the install stops.
#
# OMARCHY_LAB_RELEASES overrides where the release is downloaded from; the
# signature is still checked against the pinned key.

# Everything runs inside main, so a download cut short never runs half a script.
main() {
  set -euo pipefail

  local releases="${OMARCHY_LAB_RELEASES:-https://github.com/omacom/omarchy-maclab/releases/latest/download}"
  local signer="release@omarchy-m-testing.org"
  local namespace="omarchy-m-test-release"
  # The release signing public key, the same one every file of a Lab release is checked against.
  local allowed_signer='release@omarchy-m-testing.org namespaces="omarchy-m-test-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKHA9ywzf4hxAd5Ohzw8tRg5i2lxHAxp2mDV0PWi5o0u'

  local bin_dir="$HOME/.local/bin"
  local bin="$bin_dir/lab"

  local tool
  for tool in curl ssh-keygen uname mktemp; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      echo "Installing lab needs $tool, which isn't installed." >&2
      exit 1
    fi
  done

  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "lab is built for macOS and Linux, not for $(uname -s). Nothing was installed." >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    arm64 | aarch64) arch=arm64 ;;
    x86_64 | amd64) arch=amd64 ;;
    *) echo "lab is built for arm64 and x86-64, not for $(uname -m). Nothing was installed." >&2; exit 1 ;;
  esac
  if [[ "$os-$arch" == darwin-amd64 ]]; then
    echo "lab is built for macOS on Apple Silicon only. Nothing was installed." >&2
    exit 1
  fi
  local file="lab-$os-$arch"

  # Global, not local: the EXIT trap runs after main has returned.
  OMARCHY_LAB_WORK="$(mktemp -d)"
  trap 'rm -rf "$OMARCHY_LAB_WORK"' EXIT
  local work="$OMARCHY_LAB_WORK"

  # What is there now. Ours answers `lab version` by naming Omarchy Mac Lab;
  # it is asked in an empty home with no login, so it reaches no lab.
  local previous=""
  if [[ -e "$bin" || -L "$bin" ]]; then
    mkdir -p "$work/home"
    local said=""
    said="$(env -i HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" PATH="$PATH" "$bin" version 2>/dev/null || true)"
    if [[ "$said" != "lab "* || "$said" != *"Omarchy Mac Lab"* ]]; then
      echo "$bin is not Omarchy Mac Lab's lab (it may be a lab built for another lab), so it was left alone. Nothing was installed." >&2
      echo "Move it to a name of your choosing, then run this again." >&2
      exit 1
    fi
    previous="${said%%$'\n'*}"
    previous="${previous#lab }"
  fi

  echo "Downloading lab for $os on $arch..."
  curl -fsSL --proto '=https,file' -o "$work/$file" "$releases/$file"
  curl -fsSL --proto '=https,file' -o "$work/$file.sig" "$releases/$file.sig"

  printf '%s\n' "$allowed_signer" > "$work/allowed_signers"
  if ! ssh-keygen -Y verify -f "$work/allowed_signers" -I "$signer" -n "$namespace" \
      -s "$work/$file.sig" < "$work/$file" > "$work/verify.log" 2>&1; then
    echo "The download's signature doesn't match the release key of Omarchy Mac Lab. Nothing was installed." >&2
    sed 's/^/  /' "$work/verify.log" >&2
    exit 1
  fi
  echo "Signature verified."

  chmod 755 "$work/$file"
  local version=""
  version="$(env -i HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" PATH="$PATH" "$work/$file" version 2>/dev/null || true)"
  if [[ "$version" != "lab "* ]]; then
    echo "The release doesn't look like lab. Nothing was installed." >&2
    exit 1
  fi
  version="${version%%$'\n'*}"
  version="${version#lab }"

  # One rename, so a lab is never half written.
  mkdir -p "$bin_dir"
  cp "$work/$file" "$bin_dir/.lab.new"
  chmod 755 "$bin_dir/.lab.new"
  mv -f "$bin_dir/.lab.new" "$bin"

  if [[ -n "$previous" && "$previous" != "$version" ]]; then
    echo "Updated lab $previous -> $version."
  elif [[ -n "$previous" ]]; then
    echo "Reinstalled lab $version."
  else
    echo "Installed lab $version."
  fi

  case ":$PATH:" in
    *":$bin_dir:"*) echo "Log in with: lab login https://lab.omarchy-mac.org" ;;
    *) echo "Log in with: $bin login https://lab.omarchy-mac.org (add $bin_dir to your PATH to type just lab)" ;;
  esac
}

main "$@"

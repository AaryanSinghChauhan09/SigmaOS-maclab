# What the mini's scripts share: where the lab lives on the mini and how a
# Lab release is checked there. Sourced by the scripts beside it. Needs only
# what macOS has.
# shellcheck shell=bash disable=SC2034

RELEASE_SIGNER="release@omarchy-m-testing.org"
RELEASE_NAMESPACE="omarchy-m-test-release"
RELEASE_BUNDLE="lab-server-darwin-arm64.tar.gz"
RELEASE_TAG='^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$'
RELEASE_URL="https://github.com/omacom/omarchy-maclab/releases/download"

LAB_HOST="lab.omarchy-mac.org"
LABD_LABEL="org.omarchy-mac.lab.labd"
LABD_LISTEN="127.0.0.1:7770"
OOBD_LABEL="com.maclab.oobd"
OOBD_LISTEN="127.0.0.1:7780"
RESET_TOOL_NAME="macvdmtool"

ROOT_OWNER=root
ROOT_GROUP=wheel
LAB_OWNER=lab
LAB_GROUP=lab

# The tests stand a folder in for / (LAB_SYSROOT) and the account running
# them in for root and lab. sudo drops the variable, and without it the
# scripts run only what macOS itself put on the machine.
SYSROOT="${LAB_SYSROOT:-}"
if [[ -n $SYSROOT ]]; then
  ROOT_OWNER="$(id -un)" ROOT_GROUP="$(id -gn)" LAB_OWNER="$(id -un)" LAB_GROUP="$(id -gn)"
  LABD_LISTEN="${LAB_LOOPBACK:-$LABD_LISTEN}"
  OOBD_LISTEN="${LAB_OOBD_LOOPBACK:-$OOBD_LISTEN}"
else
  PATH=/usr/bin:/bin:/usr/sbin:/sbin
  export PATH
fi

# Root's, and only root writes there: the builds, the copy of the Homebrew
# tools the daemons run from, the release key, and the secrets of what runs
# as root, which only root reads.
LAB_ROOT="$SYSROOT/usr/local/lab"
LAB_BUILDS="$LAB_ROOT/builds"
LAB_CURRENT="$LAB_ROOT/current"
LAB_TOOLS="$LAB_ROOT/tools"
LAB_SIGNERS="$LAB_ROOT/allowed_signers"
LAB_SECRETS="$LAB_ROOT/secrets"
OOBD_TOKEN="$LAB_SECRETS/oobd.token"
RESET_TOOL="$LAB_CURRENT/bin/$RESET_TOOL_NAME"
# The lab account's, and only it reads there: data, secrets, the socket, logs.
LAB_HOME="$SYSROOT/var/lab"
LABD_SOCKET="$LAB_HOME/run/labd.sock"
LABD_LOG="$LAB_HOME/log/labd.log"
LABD_OOBD_TOKEN="$LAB_HOME/secrets/oobd.token"
LAUNCHD_DIR="$SYSROOT/Library/LaunchDaemons"
OOBD_PLIST="$LAUNCHD_DIR/$OOBD_LABEL.plist"
BREW_PREFIX="$SYSROOT/opt/homebrew"

umask 022
ME="$(basename "$0")"
DRY=0
CHANGES=0

say() { echo "$ME: $*"; }
fail() { echo "$ME: $*" >&2; exit 1; }

# change WHAT COMMAND...: make one change, or in a dry run say what it would be.
change() {
  local what="$1"
  shift
  CHANGES=$((CHANGES + 1))
  if (( DRY )); then
    say "would $what"
  else
    say "$what"
    "$@"
  fi
}

need_root() {
  [[ -n $SYSROOT || "$(id -u)" == 0 ]] || fail "run it with sudo"
}

# owner_of PATH: the account PATH belongs to.
owner_of() {
  # shellcheck disable=SC2012
  ls -ld "$1" | awk '{ print $3 }'
}

# looks PATH: its kind and mode, owner and group, as ls shows them.
looks() {
  # shellcheck disable=SC2012
  ls -ld "$1" 2>/dev/null | awk '{ print substr($1, 1, 10), $3 ":" $4 }'
}

# fetch URL FILE: https, or a file on this machine.
fetch() {
  case "$1" in
    https://* | file:///*) ;;
    http://127.0.0.1:*) [[ -n $SYSROOT ]] || fail "$1 is not https" ;;
    *) fail "$1 is neither https:// nor file:///" ;;
  esac
  curl -fsSL --retry 2 --max-time 900 -o "$2" "$1"
}

# verified FILE: whether FILE.sig is the pinned release key's signature of FILE.
verified() {
  ssh-keygen -Y verify -f "$LAB_SIGNERS" -I "$RELEASE_SIGNER" -n "$RELEASE_NAMESPACE" -s "$1.sig" < "$1" >/dev/null 2>&1
}

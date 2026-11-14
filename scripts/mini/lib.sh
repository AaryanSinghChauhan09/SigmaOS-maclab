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
CLOUDFLARED_LABEL="org.omarchy-mac.lab.cloudflared"
FRONT_DOOR="https://$LAB_HOST"
SIGNAL_LABEL="org.omarchy-mac.lab.signal-cli"
LITESTREAM_LABEL="org.omarchy-mac.lab.litestream"
BACKUP_LABEL="org.omarchy-mac.lab.backup"
DRILL_LABEL="org.omarchy-mac.lab.restore-drill"
DEAD_MAN_LABEL="org.omarchy-mac.lab.dead-man"
# Where the checks kept outside the house are pinged: healthchecks.io.
CHECKS_AT="https://hc-ping.com"
# The Builder, and the daemon that starts container's server for it and
# keeps it up. The volumes are the sizes of the spec.
CONTAINER_LABEL="org.omarchy-mac.lab.container"
BUILDER_LABEL="org.omarchy-mac.lab.builder"
BUILDER_VOLUMES="maclab-cache:40G maclab-pkgcache:120G"
BUILDER_IMAGES="maclab-kbuild:1:builder maclab-pkgbuild:1:builder/pkg"
# The backup guest on pve2, by the name the set-up writes into /etc/hosts.
BACKUP_GUEST="lab-backup"
BACKUP_HOUSE="rest:https://$BACKUP_GUEST:8000/mini/"
BACKUP_HOST="lab-mini"

ROOT_OWNER=root
ROOT_GROUP=wheel
LAB_OWNER=lab
LAB_GROUP=lab
AS_LAB=(sudo -n -u "$LAB_OWNER" --)

# The tests stand a folder in for / (LAB_SYSROOT) and the account running
# them in for root and lab. sudo drops the variable, and without it the
# scripts run only what macOS itself put on the machine.
SYSROOT="${LAB_SYSROOT:-}"
if [[ -n $SYSROOT ]]; then
  ROOT_OWNER="$(id -un)" ROOT_GROUP="$(id -gn)" LAB_OWNER="$(id -un)" LAB_GROUP="$(id -gn)"
  AS_LAB=("${LAB_AS_LAB:-env}")
  LABD_LISTEN="${LAB_LOOPBACK:-$LABD_LISTEN}"
  OOBD_LISTEN="${LAB_OOBD_LOOPBACK:-$OOBD_LISTEN}"
  FRONT_DOOR="${LAB_FRONT_DOOR:-$FRONT_DOOR}"
  BACKUP_HOUSE="${LAB_BACKUP_HOUSE:-$BACKUP_HOUSE}"
  CHECKS_AT="${LAB_CHECKS_AT:-$CHECKS_AT}"
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
# The tunnel's credential, as cloudflared reads it with --token-file.
TUNNEL_TOKEN="$LAB_HOME/secrets/cloudflared.token"
LAUNCHD_DIR="$SYSROOT/Library/LaunchDaemons"
# Backups. The live databases stay out of restic: it carries the copies the
# nightly run makes in snapshots/. What the guest and R2 are reached with is
# in one folder only the lab account reads.
LAB_DATA="$LAB_HOME/data"
LAB_SNAPSHOTS="$LAB_DATA/snapshots"
LAB_DATABASES="labd.db reports.db"
BACKUP_KEYS="$LAB_HOME/secrets/backup"
BACKUP_KEYS_HOUSE="restic-password rest-password guest.crt litestream-sftp-key host-key"
BACKUP_KEYS_OFFSITE="r2-repository r2-access-key-id r2-secret-access-key"
BACKUP_OK="$LAB_HOME/log/backup.ok"
DRILL_OK="$LAB_HOME/log/restore-drill.ok"
# The checks kept outside the house: one the Dead-man switch pings each
# minute, one the nightly backup pings, one the Restore drill does. A check's
# address is its only secret, since whoever has it can ping in the lab's
# place: each is one line in a file only the lab account reads.
CHECKS="$LAB_HOME/secrets/checks"
CHECK_NAMES="dead-man backup restore-drill"
CHECK_URL="^${CHECKS_AT//./\\.}/[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}\$"
LITESTREAM_CONFIG="$LAB_ROOT/litestream.yml"
LITESTREAM="$LAB_TOOLS/current/litestream/bin/litestream"
RESTIC="$LAB_TOOLS/current/restic/bin/restic"
HOSTS_FILE="$SYSROOT/etc/hosts"
OOBD_PLIST="$LAUNCHD_DIR/$OOBD_LABEL.plist"
# Apple's container, as its package installs it; the account whose session
# its server runs in, as the set-up was told; the builder's own token, which
# only root reads; and what the watch last found.
CONTAINER="$SYSROOT/usr/local/bin/container"
CONTAINER_USER_FILE="$LAB_ROOT/container-user"
CONTAINER_STATE="$LAB_ROOT/container.state"
CONTAINER_LOG="$SYSROOT/var/log/lab-container.log"
BUILDER_TOKEN="$LAB_SECRETS/builder.token"
# The lab's Signal sender: signal-cli from the copy of the Homebrew tools,
# its account's keys and the two groups' ids as files of the lab account.
SIGNAL_PLIST="$LAUNCHD_DIR/$SIGNAL_LABEL.plist"
SIGNAL_CLI="$LAB_TOOLS/current/signal-cli/bin/signal-cli"
SIGNAL_DATA="$LAB_HOME/secrets/signal-cli"
SIGNAL_SOCKET="$LAB_HOME/run/signal-cli.sock"
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

# loaded LABEL: whether launchd has that daemon.
loaded() { launchctl print "system/$1" >/dev/null 2>&1; }

# stopped LABEL: boot the daemon out and wait until launchd has let it go.
stopped() {
  local n
  loaded "$1" || return 0
  launchctl bootout "system/$1" 2>/dev/null || true
  for n in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    loaded "$1" || return 0
    sleep 1
  done
  return 1
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

# The lab account owns /var/lab, so whoever has its rights can put a link
# there, a hard link too, or swap one in while a script runs. Root therefore
# writes, chowns and chmods nothing below /var/lab with its own hand: that
# account does it, which reaches only what it could already write, and a
# symbolic link found there is refused.

# as_lab COMMAND...: run a program as the lab account.
as_lab() { (cd / && "${AS_LAB[@]}" "$@"); }

planted() {
  fail "$1 is a symbolic link (to $(readlink "$1")), and nothing the lab keeps there is one: whoever has the $LAB_OWNER account's rights put it there. Nothing was written through it. Look at what else that account did, remove the link and run this again"
}

# no_link PATH...: refuse when PATH, or a folder between it and /var/lab, is
# a symbolic link.
no_link() {
  local p
  for p in "$@"; do
    while :; do
      [[ ! -L $p ]] || planted "$p"
      [[ $p == "$LAB_HOME"/* ]] || break
      p="$(dirname "$p")"
    done
  done
}

# no_planted_link: the same for /var/lab, its folders and its secrets, to
# refuse before anything is changed.
no_planted_link() {
  local link
  link="$(find "$LAB_HOME" "$LAB_HOME/secrets" -maxdepth 1 -type l 2>/dev/null | head -n 1 || true)"
  [[ -z $link ]] || planted "$link"
}

# lab_folder PATH MODE: a folder below /var/lab, the lab account's.
lab_folder() {
  no_link "$1"
  [[ -d $1 ]] || as_lab mkdir "$1" || fail "$LAB_OWNER could not make the folder $1"
  { as_lab chown -h "$LAB_OWNER:$LAB_GROUP" "$1" && as_lab chmod "$2" "$1"; } || fail "$1 is not $LAB_OWNER's to set: look at what it is, remove it and run this again"
}

# lab_file PATH [MODE]: standard input becomes the file PATH below /var/lab,
# written by the lab account: under PATH.new, which is made only if nothing
# has that name, then moved into place.
lab_file() {
  no_link "$1" "$1.new"
  [[ ! -d $1 ]] || fail "$1 is a folder"
  # shellcheck disable=SC2016
  as_lab /bin/sh -c 'umask 077; set -C; rm -f "$1.new" && cat > "$1.new" && chmod "$2" "$1.new" && mv -f "$1.new" "$1"' lab-file "$1" "${2:-600}" ||
    fail "$LAB_OWNER could not write $1"
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

stop_sender() { stopped "$SIGNAL_LABEL" || fail "$SIGNAL_LABEL did not stop (20 s)"; }

start_sender() {
  stop_sender
  launchctl enable "system/$SIGNAL_LABEL" || true
  launchctl bootstrap system "$SIGNAL_PLIST" || fail "launchd did not take $SIGNAL_PLIST"
  loaded "$SIGNAL_LABEL" || fail "$SIGNAL_LABEL is not loaded after its start: see $LAB_HOME/log/signal-cli.log"
}

# notify TEXT: one operations line for "Agents". A line that cannot be sent
# is still printed, so it is in the log of whatever ran.
notify() {
  local hook
  hook="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/notify"
  say "$1"
  [[ ! -x $hook ]] || "$hook" agents "$1" || say "that line was not sent"
}

# ping_check NAME: tell that check the lab did what the check waits for. The
# address reaches curl on its standard input, never as an argument, and is
# never printed. Says why and returns 1 when nothing was sent.
ping_check() {
  local url
  if [[ ! -s $CHECKS/$1 ]]; then
    say "no check to ping for $1 yet: the set-up puts its address at $CHECKS/$1 (--checks FOLDER)"
    return 1
  fi
  url="$(<"$CHECKS/$1")"
  if [[ ! $url =~ $CHECK_URL ]]; then
    say "$CHECKS/$1 is not a check's address ($CHECKS_AT/ and the check's id, on one line): nothing was sent"
    return 1
  fi
  if ! printf 'url = "%s"\n' "$url" | curl -fsS --max-time 10 --retry 2 -o /dev/null -K - 2>/dev/null; then
    say "the check $1 was not reached"
    return 1
  fi
}

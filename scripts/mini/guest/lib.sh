# What the backup guest's scripts share: where things are on the guest, and
# how a nightly job says how it went. Sourced by the scripts beside it. The
# guest is a small Debian machine on pve2; nothing here runs on the mini.
# shellcheck shell=bash disable=SC2034

RESTIC_USER=restic
SFTP_USER=litestream
MIRROR_USER=mirror
ROOT_USER=root
REPOSITORY_URL="https://github.com/omacom/omarchy-maclab.git"
RELEASES_API="https://api.github.com/repos/omacom/omarchy-maclab/releases"
RELEASES_FILES="https://github.com/omacom/omarchy-maclab/releases/download/"

# The tests stand a folder in for / (LAB_GUEST_ROOT) and the account running
# them in for every account; sudo drops the variable.
GUEST_ROOT="${LAB_GUEST_ROOT:-}"
if [[ -n $GUEST_ROOT ]]; then
  RESTIC_USER="$(id -un)" SFTP_USER="$(id -un)" MIRROR_USER="$(id -un)" ROOT_USER="$(id -un)"
  REPOSITORY_URL="${LAB_MIRROR_REPOSITORY:-$REPOSITORY_URL}"
  RELEASES_API="${LAB_MIRROR_API:-$RELEASES_API}"
  RELEASES_FILES="${LAB_MIRROR_FILES:-$RELEASES_FILES}"
else
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
  export PATH
fi

SRV="$GUEST_ROOT/srv/lab-backup"
# rest-server's folder; the mini's repository is the folder of its user name.
RESTIC_DIR="$SRV/restic"
REPOSITORY="$RESTIC_DIR/mini"
# All the SFTP account sees: Litestream's replicas in db/, and in status/ how
# the guest's nightly jobs went, which the mini reads.
SFTP_DIR="$SRV/sftp"
STATUS_DIR="$SFTP_DIR/status"
MIRROR_DIR="$SRV/mirror"
ETC="$GUEST_ROOT/etc/lab-backup"
LIB="$GUEST_ROOT/usr/local/lib/lab-backup"
BIN="$GUEST_ROOT/usr/local/bin"

# restic keeps 7 daily, 4 weekly, 6 monthly; and no copy of the lab's data is
# kept longer than 6 months, whatever those rules leave.
KEEP=(--keep-daily 7 --keep-weekly 4 --keep-monthly 6)
KEEP_DAYS=183

umask 022
ME="$(basename "$0")"

say() { echo "$ME: $*"; }
fail() { echo "$ME: $*" >&2; exit 1; }

days_ago() { date -u -d "$1 days ago" +%Y-%m-%d 2>/dev/null || date -u -v-"$1"d +%Y-%m-%d; }

# A nightly job names what it is doing in WHAT; however it ends, one line
# says so where the mini can read it: "ok|failed <seconds> <time> <what>".
JOB=""
WHAT="starting"
report() {
  local code=$? state=ok
  (( code == 0 )) || state=failed
  printf '%s %s %s %s\n' "$state" "$(date -u +%s)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$([[ $state == ok ]] || echo "$WHAT")" > "$STATUS_DIR/$JOB" || true
  (( code == 0 )) || echo "$ME: failed: $WHAT" >&2
}

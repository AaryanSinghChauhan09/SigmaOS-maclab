# What the Cutover's scripts that run on the mini share. Sourced by the
# scripts beside it. The mini's own library comes from the repository this
# folder is in or, for a copy of this folder on the mini, from the build
# Deploy switched to.
# shellcheck shell=bash disable=SC2034

cutover="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mini_lib="$cutover/../mini/lib.sh"
[[ -f $mini_lib ]] || mini_lib="${LAB_SYSROOT:-}/usr/local/lab/current/scripts/mini/lib.sh"
[[ -f $mini_lib ]] || { echo "$(basename "$0"): no Lab release is Deployed on this machine ($mini_lib is not there): this runs on the mini, after a Deploy" >&2; exit 1; }
# shellcheck source=scripts/mini/lib.sh
. "$mini_lib"

LABD="$LAB_CURRENT/bin/labd"
LABD_PLIST="$LAUNCHD_DIR/$LABD_LABEL.plist"
LITESTREAM_PLIST="$LAUNCHD_DIR/$LITESTREAM_LABEL.plist"
MACHINE_ID_KEY="$LAB_HOME/secrets/machine-id-key"

deployed() {
  [[ -x $LABD && -f $LABD_PLIST ]] || fail "no Lab release is Deployed here ($LABD): Deploy one first (scripts/mini/deploy)"
}

# in_preview: labd's LaunchDaemon file starts it closed, with --preview.
in_preview() { grep -q '<string>--preview</string>' "$LABD_PLIST" 2>/dev/null; }

labd_answers() {
  local n
  for (( n = ${LAB_VERIFY_TRIES:-30}; n > 0; n-- )); do
    if loaded "$LABD_LABEL" && [[ "$(curl -fsS --max-time 5 -A lab-cutover "http://$LABD_LISTEN/healthz" 2>/dev/null)" == ok ]]; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# stop_lab: labd and Litestream let go of the data directory, once the jobs
# that run have ended. had_litestream remembers whether Litestream ran, so
# that start_lab starts it again.
had_litestream=0
stop_lab() {
  if loaded "$LITESTREAM_LABEL"; then had_litestream=1; fi
  # As a Deploy does: no job is cut by the stop.
  if declare -F drain_jobs >/dev/null; then
    drain_jobs 30 || fail "$STILL_RUNNING still run after 30 minutes: nothing was changed. Run this again when they have ended"
  fi
  stopped "$LABD_LABEL" || fail "labd did not stop (20 s): nothing was changed"
  stopped "$LITESTREAM_LABEL" || fail "Litestream did not stop (20 s): labd is stopped, and nothing else was changed. Start labd again with: launchctl bootstrap system $LABD_PLIST"
}

start_lab() {
  launchctl enable "system/$LABD_LABEL" || true
  launchctl bootstrap system "$LABD_PLIST" || say "launchd did not take $LABD_PLIST"
  if (( had_litestream )); then
    launchctl bootstrap system "$LITESTREAM_PLIST" || say "launchd did not take $LITESTREAM_PLIST: Litestream is not running"
  fi
  labd_answers
}

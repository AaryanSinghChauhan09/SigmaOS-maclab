#!/bin/sh
# Omarchy Mac Lab: the transfer test from outside the house.
#
#   sh fd05-member.sh JOB
#
# JOB is the job id that came with this file. Run it on a machine with a
# wired connection, as yourself. It needs a shell, curl and bash (for the
# lab's install line), 1 GiB of free disk for a while, and moves about
# 3 GiB: how long that takes is what it measures.
#
# What it does, in order:
#
#   1. Installs the lab command into ~/.local/bin/lab with the lab's own
#      install line, with no root. It stops at a lab that is not Omarchy Mac
#      Lab's and changes nothing then.
#   2. Asks you to log in, if you are not: lab asks for a Lab token, which
#      you make on your own page of the lab after signing in with GitHub.
#      The token goes to lab's prompt and nowhere else: this script never
#      sees it.
#   3. Takes a Lease on m1-pro, in your name.
#   4. Uploads 1 GiB of random bytes for a job on that Mac, twice: one part
#      at a time, then four.
#   5. Downloads a file of a job on that Mac until 1 GiB has come.
#   6. Releases the Lease and deletes its own files.
#   7. Prints nine numbers: sizes, seconds and speeds. No address, no name,
#      no token. Those lines are what to send back.
#
# It talks to the lab and to nothing else. What lab itself says while it
# runs is kept in a file that is deleted at the end, and shown to you only
# when a step fails.
#
# For a rehearsal against a lab under test: LAB_ADDRESS, LAB_MAC, LAB_BYTES,
# and LAB_COMMAND to use a lab already there in place of installing one.
set -eu

ADDRESS="${LAB_ADDRESS:-https://lab.omarchy-mac.org}"
MAC="${LAB_MAC:-m1-pro}"
BYTES="${LAB_BYTES:-1073741824}"
FILE="test-transfer-file/transfer.bin"

say() { echo "fd05: $*" >&2; }
stop() { echo "fd05: $*" >&2; echo "fd05: nothing to send back." >&2; exit 1; }

[ $# = 1 ] || stop "usage: sh fd05-member.sh JOB (the job id that came with this file)"
JOB="$1"
case "$JOB" in
  j*) ;;
  *) stop "\"$JOB\" is not a job id" ;;
esac
case "$JOB" in
  *[!0-9a-z-]*) stop "\"$JOB\" is not a job id" ;;
esac
case "$BYTES" in
  ''|*[!0-9]*) stop "LAB_BYTES is not a number of bytes" ;;
esac
command -v curl >/dev/null 2>&1 || stop "this needs curl"

# A login for another lab in the environment would send the run there, and
# the Lease would carry this machine's name: neither is wanted.
unset MACLAB_URL MACLAB_TOKEN
MACLAB_HOLDER="front-door-test-5"
export MACLAB_HOLDER

work="$(mktemp -d "${TMPDIR:-/tmp}/fd05.XXXXXX")"
log="$work/lab.log"
leased=0
finish() {
  if [ "$leased" = 1 ]; then "$LAB" release "$MAC" >/dev/null 2>&1 || say "the Lease on $MAC was not released: run $LAB release $MAC"; fi
  rm -rf "$work"
}
trap finish EXIT
trap 'exit 130' INT TERM

# now: seconds since 1970, with a fraction where date can give one.
now() {
  t="$(date +%s.%N 2>/dev/null || true)"
  case "$t" in
    ''|*[!0-9.]*) date +%s ;;
    *) echo "$t" ;;
  esac
}
between() { awk -v a="$1" -v b="$2" 'BEGIN { s = b - a; if (s < 0.001) s = 0.001; printf "%.1f", s }'; }
mbit() { awk -v n="$1" -v s="$2" 'BEGIN { printf "%.1f", n * 8 / s / 1000000 }'; }
# said: what lab last said, never a web page.
said() {
  if grep -qiE '<!doctype|<html|<body' "$log" 2>/dev/null; then
    echo "(an error page from the network, not shown)"
  else
    tail -n 5 "$log" 2>/dev/null || true
  fi
}

# 1. The lab command.
if [ -n "${LAB_COMMAND:-}" ]; then
  LAB="$LAB_COMMAND"
else
  say "installing lab into ~/.local/bin with: curl -fsSL $ADDRESS/install/lab | bash"
  curl -fsSL "$ADDRESS/install/lab" > "$work/install" || stop "the lab's install line could not be fetched"
  bash "$work/install" >&2 || stop "the lab's install line did not finish; what it said is above"
  LAB="$HOME/.local/bin/lab"
fi
[ -x "$LAB" ] || stop "no lab command at $LAB"

# 2. The login.
if ! "$LAB" status > "$log" 2>&1; then
  say "log in to Omarchy Mac Lab. Open $ADDRESS in a browser, sign in with GitHub,"
  say "make a Lab token on your own page ($ADDRESS/me) and paste it at the prompt below."
  "$LAB" login "$ADDRESS" >&2 || stop "the login did not work"
  "$LAB" status > "$log" 2>&1 || stop "lab does not reach the lab after the login: $(said)"
fi

# 3. The Lease.
say "taking a Lease on $MAC"
"$LAB" lease "$MAC" > "$log" 2>&1 || stop "no Lease on $MAC: $(said)"
leased=1

# 4. Two uploads of new random bytes, for a job on that Mac.
upload() {
  say "uploading $BYTES bytes, $1 part(s) at a time"
  head -c "$BYTES" /dev/urandom > "$work/up.bin"
  t0="$(now)"
  MACLAB_PARTS_AT_ONCE="$1" "$LAB" upload "$work/up.bin" --for "$MAC" > "$log" 2>&1 || stop "the upload failed: $(said)"
  t1="$(now)"
  rm -f "$work/up.bin"
  seconds="$(between "$t0" "$t1")"
}
upload 1
up1="$seconds"
upload 4
up4="$seconds"

# 5. The download, as often as it takes.
say "downloading $BYTES bytes"
down=0
t0="$(now)"
while [ "$down" -lt "$BYTES" ]; do
  got="$("$LAB" logs "$JOB" "$FILE" 2> "$log" | wc -c | tr -d ' ')"
  [ ! -s "$log" ] || stop "the download failed: $(said)"
  [ "$got" -gt 0 ] || stop "the download brought nothing: is $JOB the job id that came with this file?"
  down=$((down + got))
done
t1="$(now)"
downs="$(between "$t0" "$t1")"

# 6. The Lease goes back.
"$LAB" release "$MAC" > "$log" 2>&1 || stop "the Lease on $MAC was not released: $(said)"
leased=0

# 7. The numbers.
say "done. Send back only the lines between the two rules:"
cat <<EOF
----------
fd05_format=1
upload_bytes=$BYTES
upload_1_seconds=$up1
upload_1_mbit=$(mbit "$BYTES" "$up1")
upload_4_seconds=$up4
upload_4_mbit=$(mbit "$BYTES" "$up4")
download_bytes=$down
download_seconds=$downs
download_mbit=$(mbit "$down" "$downs")
----------
EOF

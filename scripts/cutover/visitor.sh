# What the Alias rehearsal and the Dark recordings share: asking a name as
# a visitor does, from outside, and writing down what came back. Sourced
# after front-door/lib.sh by the scripts beside it; needs bash and curl.
#
# Nothing an answer holds is printed raw: a status, a header's value that
# labd or the edge chose, a cookie's name and never its value, and for an
# error page its status and ray id.
# shellcheck shell=bash disable=SC2034,SC2153

LAB_URL="$ADDRESS"
LAB_NAME="${ADDRESS#*://}"
UA="omarchy-mac-lab-cutover"
# Under --local the requests go to labd's own socket, as the tunnel's would.
VIA=()
STATUS=""
failed=0

# The tests stand a server on this machine in for the edge: a name is then
# asked at EDGE/NAME, over plain HTTP.
EDGE="${LAB_CUTOVER_EDGE:-}"
[[ -z $EDGE || $EDGE =~ ^http://127\.0\.0\.1:[0-9]+$ ]] || fail "LAB_CUTOVER_EDGE is the tests' and takes a loopback address only"

# at NAME: where NAME is asked.
at() {
  if [[ $MODE == local ]]; then
    echo "http://$1"
  elif [[ -n $EDGE ]]; then
    echo "$EDGE/$1"
  else
    echo "https://$1"
  fi
}

# ask [CURL OPTIONS] URL: one request, following nothing. Leaves STATUS (000
# when nothing answered), the answer's headers in $WORK/head and its body in
# $WORK/body.
ask() {
  : > "$WORK/head"
  : > "$WORK/head.raw"
  : > "$WORK/body"
  STATUS="$(curl -sS --max-time "${ASK_SECONDS:-30}" -A "$UA" ${VIA[@]+"${VIA[@]}"} -o "$WORK/body" -D "$WORK/head.raw" -w '%{http_code}' "$@" 2> "$WORK/ask.err")" || STATUS=000
  tr -d '\r' < "$WORK/head.raw" > "$WORK/head"
}

# header NAME: the value of that header in the last answer, NAME in lower case.
header() { awk -v h="$1" '{ i = index($0, ":"); if (i && tolower(substr($0, 1, i - 1)) == h) { v = substr($0, i + 1); sub(/^ +/, "", v); print v; exit } }' "$WORK/head"; }

# cookie_names: the names of the cookies the last answer set, never a value.
cookie_names() {
  awk '{ i = index($0, ":"); if (i && tolower(substr($0, 1, i - 1)) == "set-cookie") { v = substr($0, i + 1); sub(/^ +/, "", v); sub(/=.*/, "", v); print v } }' "$WORK/head" | sort -u | tr '\n' ' ' | sed 's/ $//'
}

# error_of: what an answer that is not the lab's own says of itself, fit to
# print: Cloudflare's error number and the ray id, when it has them.
error_of() {
  local code ray
  code="$(grep -oiE 'error code:? *[0-9]{3,4}|Error [0-9]{4}' "$WORK/body" 2>/dev/null | grep -oE '[0-9]{3,4}' | head -n 1 || true)"
  ray="$(header cf-ray)"
  echo "${code:+Cloudflare error $code, }ray id ${ray:-none}"
}

# record_open STEM TITLE NOTE: the dated record of a run. Through the Lab
# address it goes to docs/acceptance/; a local run shows its record and
# keeps none unless --records names a folder.
record_open() {
  local dir="$RECORDS" how
  if [[ -z $dir ]]; then
    if [[ $MODE == lab ]]; then dir="$REPO/docs/acceptance"; else dir="$WORK/records"; fi
  fi
  mkdir -p "$dir"
  RECORD="$dir/$(TZ=Australia/Brisbane date +%Y-%m-%d)-$1.md"
  if [[ $MODE == lab ]]; then
    how="through $LAB_URL"
  else
    how="against a labd started on this machine: no tunnel, no edge, no name in DNS. It proves labd's part and the script, not the route"
  fi
  {
    echo "# $2"
    echo
    echo "Run $(TZ=Australia/Brisbane date '+%Y-%m-%d %H:%M') (Brisbane) by \`scripts/cutover/$ME --$MODE\`, $how."
    [[ -z ${3:-} ]] || { echo; echo "$3"; }
    echo
  } > "$RECORD"
}

# step pass|fail WHAT WORDS: one line of the record that is a check.
step() {
  if [[ $1 == pass ]]; then
    rec "$2: pass. $3"
  else
    rec "$2: FAIL. $3"
    failed=1
  fi
}

#!/usr/bin/env bash
# A smoke test of the plain labd binary on an empty data directory, as it runs
# behind a proxy at a public URL: its own host, or a prefix on a shared host
# such as https://example.org/lab. curl plays the proxy from loopback (a
# trusted proxy sending X-Forwarded-Proto/Host). Nothing leaves this machine.
#
#   .github/scripts/labd-smoke.sh <directory holding labd and lab-agent-linux-arm64> [public URL]
set -euo pipefail

BIN=${1:?usage: labd-smoke.sh <bin directory> [public URL]}
PUB=${2:-https://lab.omarchy-mac.org}
HOST=${PUB#https://}
HOST=${HOST%%/*}
PREFIX=${PUB#https://"$HOST"}
[ -x "$BIN/labd" ] || { echo "no labd in $BIN" >&2; exit 1; }
[ -f "$BIN/lab-agent-linux-arm64" ] || { echo "no lab-agent-linux-arm64 in $BIN" >&2; exit 1; }

WORK=$(mktemp -d)
LOG=${RUNNER_TEMP:-$WORK}/labd-smoke.log
PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
ROOT=http://127.0.0.1:$PORT
B=$ROOT$PREFIX                      # labd as the proxy reaches it
FWD=(-H 'X-Forwarded-Proto: https' -H "X-Forwarded-Host: $HOST")
PID=

fail() { echo "FAIL: $*" >&2; exit 1; }
cleanup() {
	[ -z "$PID" ] || kill "$PID" 2>/dev/null || true
	[ -z "$PID" ] || wait "$PID" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT

"$BIN/labd" --listen="127.0.0.1:$PORT" --data="$WORK/data" --public-url="$PUB" \
	--trusted-proxy=127.0.0.1/32 --maintainer=ci --omt-site= >"$LOG" 2>&1 &
PID=$!

c() { curl -sS --max-time 20 "$@"; }
code() { c -o /dev/null -w '%{http_code}' "$@"; }
sha256() { if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi | cut -d' ' -f1; }
field() { python3 -c 'import json, sys; sys.stdout.write(json.load(sys.stdin)[sys.argv[1]])' "$1"; }
mask() { [ -z "${GITHUB_ACTIONS:-}" ] || echo "::add-mask::$1"; }

for _ in $(seq 60); do
	[ "$(code "${FWD[@]}" "$B/healthz" 2>/dev/null || true)" = 200 ] && break
	kill -0 "$PID" 2>/dev/null || { cat "$LOG" >&2; fail "labd stopped while starting"; }
	sleep 1
done
[ "$(c "${FWD[@]}" "$B/healthz")" = ok ] || fail "healthz"

TOK=$(cat "$WORK/data/admin.token")
mask "$TOK"
AUTH=(-H "Authorization: Bearer $TOK")

# The UI, under the prefix, with its <base> and headers.
page=$(c -D - "${FWD[@]}" "$B/")
grep -qF "<base href=\"$PREFIX/\">" <<<"$page" || fail "the UI's <base>"
grep -qi '^strict-transport-security: max-age=' <<<"$page" || fail "HSTS"
grep -qi "^content-security-policy: frame-ancestors 'none'" <<<"$page" || fail "frame-ancestors"
grep -qi '^x-content-type-options: nosniff' <<<"$page" || fail "nosniff"
[ "$(code "${FWD[@]}" "$B/ui/app.js")" = 200 ] || fail "app.js"

# The API: tokens only, under the prefix only.
[ "$(code "${FWD[@]}" "$B/api/devices")" = 401 ] || fail "the API without a token"
[ "$(c "${FWD[@]}" "${AUTH[@]}" "$B/api/devices")" = "[]" ] || fail "the API with the token"
if [ -n "$PREFIX" ]; then
	[ "$(code "${FWD[@]}" "${AUTH[@]}" "$ROOT/api/devices")" = 404 ] || fail "the API outside the prefix"
	[ "$(code "${FWD[@]}" "${AUTH[@]}" "${ROOT}${PREFIX}x/api/devices")" = 404 ] || fail "a sibling path"
	[ "$(code "${FWD[@]}" "${AUTH[@]}" "$B//api/devices")" = 404 ] || fail "a path the router would clean"
fi

# What the proxy forwards must name the public URL.
[ "$(code -H 'X-Forwarded-Proto: https' -H 'X-Forwarded-Host: evil.example' "${AUTH[@]}" "$B/api/devices")" = 421 ] || fail "another host forwarded"
[ "$(code -H 'X-Forwarded-Proto: http' -H "X-Forwarded-Host: $HOST" "${AUTH[@]}" "$B/api/devices")" = 421 ] || fail "plain http forwarded"

# A page from another origin can't change anything.
[ "$(code -X POST "${FWD[@]}" "${AUTH[@]}" -H 'Origin: https://evil.example' -d '{}' "$B/api/jobs")" = 403 ] || fail "a cross-origin POST"

# An invite: the join line, the terms and lab-agent, with the token in headers only.
inv=$(c -X POST "${FWD[@]}" "${AUTH[@]}" -H "Origin: https://$HOST" -d '{"name":"ci-mac"}' "$B/api/enroll-tokens")
ENR=$(field token <<<"$inv")
mask "$ENR"
cmd=$(field join_command <<<"$inv")
grep -qF "U='$PUB'" <<<"$cmd" || fail "the join line's URL"
grep -qF -- '-H @-' <<<"$cmd" || fail "the join line's header on stdin"
grep -qF -- '--token-stdin' <<<"$cmd" || fail "the join line's token on stdin"
want=$(sha256 <"$BIN/lab-agent-linux-arm64")
grep -qF "echo '$want  '" <<<"$cmd" || fail "the join line's lab-agent sha256"
got=$(printf 'Authorization: Bearer %s\n' "$ENR" | c "${FWD[@]}" -H @- "$B/api/join/lab-agent" | sha256)
[ "$got" = "$want" ] || fail "the downloaded lab-agent"
terms=$(printf 'Authorization: Bearer %s\n' "$ENR" | c "${FWD[@]}" -H @- "$B/api/join/terms")
[ "$(field text <<<"$terms" | sha256)" = "$(field sha256 <<<"$terms")" ] || fail "the terms' sha256"
[ "$(code "${FWD[@]}" "$B/api/join/terms?token=$ENR")" = 401 ] || fail "a token in the URL"

# labd's log holds neither token.
grep -qF "$TOK" "$LOG" && fail "labd logged the Maintainer token"
grep -qF "$ENR" "$LOG" && fail "labd logged the enrollment token"
kill -0 "$PID" 2>/dev/null || fail "labd stopped"
echo "labd smoke at $PUB: PASS"

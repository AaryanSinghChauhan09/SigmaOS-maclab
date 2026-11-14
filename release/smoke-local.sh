#!/usr/bin/env bash
# release/smoke.sh against a labd started on this machine, open (Preview off):
# install from its /install, upload a golden report, see it, see the repeat
# refused, delete it by its link.
#
# Builds the test tool's release and a labd as one Release candidate, both
# carrying a key made here in a temporary folder and thrown away, so the
# install line labd serves pins the key that signed the download. labd runs
# on a loopback port with an empty data directory and a machine-id key made
# here. Then the same labd in Preview: its install line names the Release
# candidate it was built as, and it takes no report. Nothing leaves this
# machine and the release key is never read.
#
# Needs go, curl, python3, ssh-keygen, GNU tar.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=release/lib.sh
. "$repo/release/lib.sh"
gnu_tools
tag="${LAB_RELEASE:-v0.0.0-rc.1}"
[[ $tag =~ $RELEASE_TAG ]] || { echo "release/smoke-local.sh: $tag is not a Lab release's tag" >&2; exit 1; }

t="$(mktemp -d)"
pid=
fail() { echo "FAIL: $*" >&2; [[ ! -f $t/labd.log ]] || sed 's/^/  labd: /' "$t/labd.log" >&2; exit 1; }
stop() {
  [[ -z $pid ]] || kill "$pid" 2>/dev/null || true
  [[ -z $pid ]] || wait "$pid" 2>/dev/null || true
  pid=
}
trap 'stop; rm -rf "$t"' EXIT

echo "== a Release candidate $tag signed with a key made here"
ssh-keygen -q -t ed25519 -N '' -C "throwaway key for the local smoke test" -f "$t/key"
printf '%s namespaces="%s" %s\n' "$RELEASE_SIGNER" "$RELEASE_NAMESPACE" "$(cut -d' ' -f1,2 "$t/key.pub")" > "$t/allowed_signers"
key="$(release_key "$t/allowed_signers")"
LAB_RELEASE="$tag" RELEASE_ALLOWED_SIGNERS="$t/allowed_signers" "$repo/release/build.sh" "$t/release" >/dev/null 2>"$t/build.log" || { cat "$t/build.log" >&2; fail "release/build.sh"; }
sign "$t/key" "$t/release/omarchy-m-test.tar.gz"
rm -f "$t/key"
(cd "$repo" && CGO_ENABLED=0 go build -ldflags "-X main.version=$tag -X 'main.releaseKey=$key'" -o "$t/labd" ./cmd/labd)
[[ "$("$t/labd" --version)" == "$tag" ]] || fail "labd --version"

python3 -c 'import secrets; print(secrets.token_hex(64))' > "$t/machine-id-key"
chmod 600 "$t/machine-id-key"
start() { # start [labd flags]: labd on a new loopback port, with a new data directory
  stop
  port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
  site="http://127.0.0.1:$port"
  rm -rf "$t/data"
  "$t/labd" --listen="127.0.0.1:$port" --data="$t/data" --machine-id-key="$t/machine-id-key" --omt-site= "$@" > "$t/labd.log" 2>&1 &
  pid=$!
  local _
  for _ in $(seq 60); do
    [[ "$(curl -sS --max-time 5 "$site/healthz" 2>/dev/null || true)" == ok ]] && return
    kill -0 "$pid" 2>/dev/null || fail "labd stopped while starting"
    sleep 1
  done
  fail "labd did not answer"
}

echo "== labd, open"
start
curl -fsS -D "$t/install.head" -o "$t/install.sh" "$site/install"
tr -d '\r' < "$t/install.head" | grep -qix 'cache-control: public, max-age=300' || fail "/install is not public for five minutes"
tr -d '\r' < "$t/install.head" | grep -qix 'content-type: text/x-shellscript; charset=utf-8' || fail "/install is not a shell script"
grep -qF "$key'" "$t/install.sh" || fail "/install does not pin the key this labd was built with"
grep -qF "$(release_key "$repo/release/allowed_signers")" "$t/install.sh" && fail "/install pins a second key"
grep -qF 'releases/latest/download' "$t/install.sh" || fail "an open lab's /install does not take the latest Lab release"

SITE="$site" OMARCHY_M_TEST_RELEASES="file://$t/release" "$repo/release/smoke.sh"

echo "== labd, in Preview"
start --preview
curl -fsS -o "$t/install-preview.sh" "$site/install"
grep -qF "releases/download/$tag}" "$t/install-preview.sh" || fail "in Preview /install does not take the Release candidate labd was built as"
grep -qF 'releases/latest/download' "$t/install-preview.sh" && fail "in Preview /install takes the latest Lab release"
code="$(curl -sS -o "$t/refused.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$site/api/v1/reports")"
[[ $code == 403 ]] && grep -qF 'is not open yet' "$t/refused.json" || fail "a lab in Preview took a report ($code)"

echo "local smoke ok: $tag installed from labd's /install with the key made here; uploaded, seen, refused the second time and deleted; in Preview the install line names $tag"

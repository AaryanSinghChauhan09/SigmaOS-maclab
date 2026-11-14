#!/usr/bin/env bash
# What the cloud scripts share: the names, the three ways a script runs and
# how Cloudflare and Porkbun are asked. Source it; do not run it.
#
# Every script here runs one of three ways:
#
#   --dry-run          the default: print the API calls it would make, send nothing
#   --check            read only; exit 1 when something is not as it should be
#   --apply            change what is not as it should be, then check
#
#   --token-file FILE  the Cloudflare API token, in a file only you can read.
#                      Without it the script asks for the token and does not
#                      show it. The token is never printed, never an argument
#                      of a command and never written anywhere.
#
# Needs bash, curl 7.84 or later and jq: what macOS has.
# shellcheck disable=SC2034

set -euo pipefail
set +x

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ME="$(basename "$0")"
LAB_DOMAIN="omarchy-mac.org"
LAB_HOST="lab.$LAB_DOMAIN"
DOMAIN="${DOMAIN:-$LAB_DOMAIN}"
USER_AGENT="omarchy-mac-lab-scripts"
KEYCHAIN_SERVICE="${KEYCHAIN_SERVICE:-omarchy-mac-lab}"
PORKBUN_API="https://api.porkbun.com/api/json/v3"
CLOUDFLARE_API="https://api.cloudflare.com/client/v4"

die() { printf '%s: %s\n' "$ME" "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
# would CALL...: one line of a dry run.
would() { printf '  %s\n' "$*"; }

# The tests stand a server on this machine in for Cloudflare's API.
if [[ -n ${LAB_CLOUD_API:-} ]]; then
  [[ $LAB_CLOUD_API =~ ^http://127\.0\.0\.1:[0-9]+(/.*)?$ ]] || die "LAB_CLOUD_API is the tests' and takes a loopback address only"
  CLOUDFLARE_API="$LAB_CLOUD_API"
fi

# Names with no value that is a secret (CLOUDFLARE_ACCOUNT_ID) may sit beside
# the scripts in lab.env, which is not committed: see lab.env.example.
if [[ -f "$SCRIPTS_DIR/lab.env" ]]; then
  # shellcheck disable=SC1091
  source "$SCRIPTS_DIR/lab.env"
fi

MODE=dry-run
TOKEN_FILE=""
ARGS=()

# parse_mode "$@": takes the options every script has and leaves the rest in ARGS.
parse_mode() {
  ARGS=()
  while (( $# )); do
    case "$1" in
      --dry-run) MODE=dry-run ;;
      --check) MODE=check ;;
      --apply) MODE=apply ;;
      --token-file)
        [[ $# -ge 2 ]] || die "--token-file needs a file"
        TOKEN_FILE="$2"
        shift
        ;;
      *) ARGS+=("$1") ;;
    esac
    shift
  done
}

# usage FIRST LAST: the script's own header, from line FIRST to line LAST.
usage() {
  sed -n "$1,$2p" "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

TOKEN=""

# load_token: the Cloudflare token from --token-file, or asked for at the
# terminal. Call it before the first cf, outside any $(...).
load_token() {
  [[ -z $TOKEN ]] || return 0
  if [[ -n $TOKEN_FILE ]]; then
    [[ -f $TOKEN_FILE ]] || die "no token file $TOKEN_FILE"
    # shellcheck disable=SC2012
    case "$(ls -lnL "$TOKEN_FILE" | cut -c1-10)" in
      -r-------- | -rw-------) ;;
      *) die "$TOKEN_FILE can be read by others: chmod 600 it" ;;
    esac
    IFS= read -r TOKEN < "$TOKEN_FILE" || true
  elif [[ -t 0 ]]; then
    printf 'Cloudflare API token (not shown): ' >&2
    IFS= read -rs TOKEN || true
    printf '\n' >&2
  else
    die "no token: give --token-file FILE, or run it at a terminal and be asked"
  fi
  TOKEN="${TOKEN//[[:space:]]/}"
  [[ -n $TOKEN ]] || die "the token is empty"
}

ZONE_ID=""
ACCOUNT_ID=""

# cf METHOD PATH [BODY]: one call to Cloudflare's API; {zone} and {account}
# in PATH are filled in. The answer's JSON goes to stdout. Exit 0 when
# Cloudflare says success, 4 when there is no such thing (404), 1 otherwise,
# with the status, Cloudflare's own error lines and the ray id on stderr and
# never the answer's body. The token reaches curl through a pipe.
cf() {
  local method="$1" path="$2" body="${3:-}" url out last status ray answer lines
  [[ -n $TOKEN ]] || die "cf before load_token"
  url="${2//\{zone\}/$ZONE_ID}"
  url="$CLOUDFLARE_API${url//\{account\}/$ACCOUNT_ID}"
  if [[ -n $body ]]; then
    out="$(printf '%s' "$body" | curl -sS -m 60 -A "$USER_AGENT" -X "$method" \
      -H @<(printf 'Authorization: Bearer %s\n' "$TOKEN") -H 'Content-Type: application/json' \
      --data-binary @- -w '\n%{http_code} %header{cf-ray}' "$url")" || die "$method $path: no answer"
  else
    out="$(curl -sS -m 60 -A "$USER_AGENT" -X "$method" \
      -H @<(printf 'Authorization: Bearer %s\n' "$TOKEN") \
      -w '\n%{http_code} %header{cf-ray}' "$url")" || die "$method $path: no answer"
  fi
  last="${out##*$'\n'}"
  answer="${out%$'\n'*}"
  status="${last%% *}"
  ray="${last#* }"
  if [[ $status == 2* ]] && printf '%s' "$answer" | jq -e '.success == true' >/dev/null 2>&1; then
    printf '%s\n' "$answer"
    return 0
  fi
  [[ $status != 404 ]] || return 4
  lines="$(printf '%s' "$answer" | jq -r '[.errors[]? | "[\(.code)] \(.message)"] | .[0:5] | join("; ")' 2>/dev/null || true)"
  printf '%s: %s %s: status %s%s, ray %s\n' "$ME" "$method" "$path" "$status" "${lines:+, $lines}" "${ray:-none}" >&2
  return 1
}

# find_zone NAME: sets ZONE_ID and ACCOUNT_ID, or leaves both empty when the
# account has no such zone. Prints the zone.
find_zone() {
  local out
  out="$(cf GET "/zones?name=$1")" || exit 1
  ZONE_ID="$(printf '%s' "$out" | jq -r '.result[0].id // empty')"
  ACCOUNT_ID="$(printf '%s' "$out" | jq -r '.result[0].account.id // empty')"
  ZONE="$(printf '%s' "$out" | jq -c '.result[0] // empty')"
}

# Porkbun, the registrar. Its two keys come from the environment or the login
# Keychain and reach curl as headers on stdin, never on a command line.
secret() {
  local value="${!1:-}"
  if [[ -z $value ]]; then
    value="$(security find-generic-password -s "$KEYCHAIN_SERVICE" -a "$2" -w 2>/dev/null || true)"
  fi
  [[ -n $value ]] || die "no credential: set $1 or add the Keychain item $KEYCHAIN_SERVICE/$2"
  printf '%s' "$value"
}

pb() {
  local key sec
  key="$(secret PORKBUN_API_KEY porkbun-api-key)" || return 1
  sec="$(secret PORKBUN_SECRET_KEY porkbun-secret-key)" || return 1
  printf 'X-API-Key: %s\nX-Secret-API-Key: %s\nContent-Type: application/json\n' "$key" "$sec" |
    curl -sS -m 60 -A "$USER_AGENT" -H @- "$@"
}

pb_ok() { printf '%s' "$1" | jq -e '.status == "SUCCESS"' >/dev/null; }

# Prints the domain's Porkbun record, or nothing when the account does not hold it.
pb_domain() {
  local out
  out="$(pb -X POST -d '{}' "$PORKBUN_API/domain/listAll")" || exit 1
  pb_ok "$out" || die "Porkbun's domain list failed: $(printf '%s' "$out" | jq -c '{code, message}')"
  printf '%s' "$out" | jq -c --arg d "$DOMAIN" '.domains[]? | select(.domain == $d)'
}

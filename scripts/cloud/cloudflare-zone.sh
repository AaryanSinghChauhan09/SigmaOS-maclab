#!/usr/bin/env bash
# The Cloudflare zone of $DOMAIN (omarchy-mac.org unless DOMAIN is set):
# made when missing, as a zone whose nameservers are Cloudflare's.
#
#   cloudflare-zone.sh            print the API calls; send nothing
#   cloudflare-zone.sh --check    show the zone; exit 1 when there is none
#   cloudflare-zone.sh --apply    make it when missing, then show it
#
#   --token-file FILE  the Cloudflare API token, in a file only you can read;
#                      without it the script asks, and does not show it
#
# Making a zone needs CLOUDFLARE_ACCOUNT_ID, in lab.env beside this script.

# shellcheck source=scripts/cloud/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
parse_mode "$@"
[[ ${#ARGS[@]} == 0 ]] || usage 2 11

show() { printf '%s' "$1" | jq '{id, name, status, type, plan: .plan.name, name_servers, created_on, activated_on}'; }
new_zone() { jq -nc --arg name "$DOMAIN" --arg account "$1" '{name: $name, type: "full", account: {id: $account}}'; }

if [[ $MODE == dry-run ]]; then
  say "$ME: a dry run. These are the calls --apply makes; nothing is sent."
  say
  say "the zone $DOMAIN"
  would "GET /zones?name=$DOMAIN"
  would "POST /zones $(new_zone "<CLOUDFLARE_ACCOUNT_ID>")"
  exit 0
fi

load_token
find_zone "$DOMAIN"
if [[ -n $ZONE_ID ]]; then
  say "$ME: the zone $DOMAIN exists"
  show "$ZONE"
  exit 0
fi
[[ $MODE == apply ]] || die "the account has no zone $DOMAIN"
[[ -n ${CLOUDFLARE_ACCOUNT_ID:-} ]] || die "CLOUDFLARE_ACCOUNT_ID is not set in $SCRIPTS_DIR/lab.env: see lab.env.example"
out="$(cf POST /zones "$(new_zone "$CLOUDFLARE_ACCOUNT_ID")")" || exit 1
say "$ME: the zone $DOMAIN was made"
show "$(printf '%s' "$out" | jq -c '.result')"

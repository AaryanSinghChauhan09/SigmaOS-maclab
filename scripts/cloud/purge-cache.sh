#!/usr/bin/env bash
# The one purge of the edge's cache: everything Cloudflare keeps for the
# Lab address's zone, at the lab's opening (the Cutover's step 25; spec
# section 6, "Cache"). Nothing is purged in normal running.
#
#   purge-cache.sh            print the API calls; send nothing
#   purge-cache.sh --check    find the zone and say what --apply would purge
#   purge-cache.sh --apply    purge everything, once
#
#   --token-file FILE  the Cloudflare API token, in a file only you can read;
#                      without it the script asks, and does not show it
#
# Until the purge, the edge may serve a public page as Preview made it,
# with its banner, for up to a day.

# shellcheck source=scripts/cloud/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
parse_mode "$@"
[[ ${#ARGS[@]} == 0 ]] || usage 2 14

if [[ $MODE == dry-run ]]; then
  say "$ME: a dry run. These are the calls --apply makes; nothing is sent."
  say
  say "everything the edge keeps for $LAB_DOMAIN"
  would "GET /zones?name=$LAB_DOMAIN"
  would "POST /zones/{zone}/purge_cache {\"purge_everything\":true}"
  exit 0
fi

load_token
find_zone "$LAB_DOMAIN"
[[ -n $ZONE_ID ]] || die "the account has no zone $LAB_DOMAIN"
if [[ $MODE == check ]]; then
  say "$ME: --apply would purge everything the edge keeps for $LAB_DOMAIN. Nothing was purged"
  exit 0
fi
cf POST "/zones/{zone}/purge_cache" '{"purge_everything":true}' >/dev/null || exit 1
say "$ME: purged everything the edge kept for $LAB_DOMAIN"

#!/usr/bin/env bash
# The Alias at Cloudflare: the zone's fallback origin and one custom
# hostname, which lead a name outside the zone to the lab's tunnel.
#
#   alias.sh                    print the API calls; send nothing
#   alias.sh --check            the fallback origin is the Lab address, the custom
#                               hostname is there, and both it and its
#                               certificate are active; else exit 1
#   alias.sh --apply            set the fallback origin and make the custom
#                               hostname, each only when missing
#   alias.sh --apply --remove   delete the custom hostname; with --check,
#                               exit 1 while it is still there
#
#   --name HOST        another name than the Alias: only a throwaway name
#                      under omarchy-m-testing.org, for the rehearsal
#                      (scripts/cutover/alias-rehearsal)
#   --token-file FILE  the Cloudflare API token, in a file only you can read;
#                      without it the script asks, and does not show it
#
# The Alias is made the day the other side says yes, and not before: a
# custom hostname left waiting for its record lapses in about three weeks.
# After --apply the other side adds one record: the name as a CNAME of the
# Lab address, DNS only. Once it is active, set alias.wanted in edge.json:
# zone-settings.sh then fails if the Alias drifts. Cloudflare takes none of
# these calls until the owner has entered payment details.

# shellcheck source=scripts/cloud/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
parse_mode "$@"
set -- ${ARGS[@]+"${ARGS[@]}"}

REHEARSAL_DOMAIN="omarchy-m-testing.org"
NAME="$ALIAS_HOST"
WANT=present
while (( $# )); do
  case "$1" in
    --name)
      [[ $# -ge 2 ]] || usage 2 26
      NAME="$2"
      shift
      ;;
    --remove) WANT=absent ;;
    *) usage 2 26 ;;
  esac
  shift
done
if [[ $NAME != "$ALIAS_HOST" ]]; then
  [[ $NAME =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.omarchy-m-testing\.org$ && $NAME != "www.$REHEARSAL_DOMAIN" ]] ||
    die "the lab has one Alias, $ALIAS_HOST. --name takes only a throwaway name under $REHEARSAL_DOMAIN, such as alias-rehearsal.$REHEARSAL_DOMAIN. Nothing was sent"
fi

DRIFTED=()
drifted() {
  DRIFTED+=("$1")
  say "drifted  $1: $2"
}

if [[ $MODE == dry-run ]]; then
  say "$ME: a dry run. These are the calls --apply makes; nothing is sent."
  say "A write is sent only for what is missing."
  say
  say "the zone"
  would "GET /zones?name=$LAB_DOMAIN"
  alias_plan "$NAME" "$WANT"
  if [[ $WANT == present ]]; then
    say "then the name's side adds one record, and nothing else:"
    would "$NAME CNAME $ALIAS_ORIGIN (DNS only, not proxied)"
  fi
  exit 0
fi

load_token
find_zone "$LAB_DOMAIN"
[[ -n $ZONE_ID ]] || die "the account has no zone $LAB_DOMAIN: cloudflare-zone.sh makes it"
if [[ $MODE == apply ]]; then
  [[ $WANT == absent ]] || alias_origin fix
  alias_hostname fix "$NAME" "$WANT"
fi
[[ $WANT == absent ]] || alias_origin judge
alias_hostname judge "$NAME" "$WANT"

if (( ${#DRIFTED[@]} )); then
  list="$(printf '%s; ' "${DRIFTED[@]}")"
  say "$ME: ${#DRIFTED[@]} not as it should be: ${list%; }"
  exit 1
fi
if [[ $WANT == absent ]]; then
  say "$ME: there is no custom hostname $NAME"
else
  say "$ME: the fallback origin and the custom hostname $NAME are as they should be"
fi

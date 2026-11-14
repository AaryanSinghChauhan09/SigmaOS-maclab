#!/usr/bin/env bash
# Does the registry delegate $DOMAIN (omarchy-mac.org unless DOMAIN is set)
# to the zone's pair, and has Cloudflare made the zone active? Changes nothing.
#
#   verify-delegation.sh                    print the calls; send nothing
#   verify-delegation.sh --check            report; exit 2 while it is pending
#   verify-delegation.sh --check --recheck  also ask Cloudflare to look now (hourly on Free)
#
#   --token-file FILE  the Cloudflare API token, in a file only you can read;
#                      without it the script asks, and does not show it

# shellcheck source=scripts/cloud/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
parse_mode "$@"
RECHECK=0
case "${ARGS[*]+"${ARGS[*]}"}" in
  "") ;;
  --recheck) RECHECK=1 ;;
  *) usage 2 10 ;;
esac

tld="${DOMAIN##*.}"
if [[ $MODE == dry-run ]]; then
  say "$ME: a dry run. These are the calls --check makes; nothing is sent."
  say
  say "the zone's pair and its state, from Cloudflare"
  would "GET /zones?name=$DOMAIN"
  would "PUT /zones/{zone}/activation_check    (with --recheck)"
  say "the delegation, from the .$tld registry"
  would "dig +short NS $tld."
  would "dig +norecurse @<a .$tld nameserver> $DOMAIN NS"
  exit 0
fi

load_token
find_zone "$DOMAIN"
[[ -n $ZONE_ID ]] || die "no Cloudflare zone for $DOMAIN"
want="$(printf '%s' "$ZONE" | jq -r '.name_servers | map(ascii_downcase) | sort | join(" ")')"

registry="$(dig +short NS "$tld." | head -n1)"
[[ -n $registry ]] || die "no nameserver found for .$tld"
have="$(dig +norecurse +noall +authority +answer "@$registry" "$DOMAIN" NS |
  awk '$4 == "NS" { print tolower($5) }' | sed 's/\.$//' | sort | tr '\n' ' ' | sed 's/ $//')"

say "registry ($registry): ${have:-none}"
say "zone pair:            $want"

if (( RECHECK )); then
  if cf PUT "/zones/{zone}/activation_check" >/dev/null; then say "asked Cloudflare to look now"; fi
  find_zone "$DOMAIN"
fi

status="$(printf '%s' "$ZONE" | jq -r '.status')"
say "zone status:          $status"

[[ $have == "$want" ]] || { say "$ME: not delegated yet"; exit 2; }
[[ $status == active ]] || { say "$ME: delegated; Cloudflare has not made the zone active yet"; exit 2; }
say "$ME: delegated and active"

#!/usr/bin/env bash
# The owner's part before the cloud scripts run: a short-lived Cloudflare API
# token, typed once and kept in a file only the owner reads.
#
#   owner-setup.sh                       say what the token needs; send nothing
#   owner-setup.sh --apply --token-file FILE
#                                        ask for the token, hidden, write it to
#                                        FILE (0600) and ask Cloudflare whether
#                                        it is good and when it ends
#   owner-setup.sh --check --token-file FILE
#                                        ask Cloudflare about the token in FILE
#
# The token is for a run of the scripts: give it an end date of a day or two
# when making it, and delete FILE when the run is done. The standing token
# stays in Bitwarden for emergencies.

# shellcheck source=scripts/cloud/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
parse_mode "$@"
[[ ${#ARGS[@]} == 0 ]] || usage 2 14

needs() {
  say "Make the token at https://dash.cloudflare.com/profile/api-tokens:"
  say "Create Token > Custom token. Labels may differ a little from these."
  say "  Zone, for $LAB_DOMAIN only, Edit on each of:"
  say "    Zone Settings, Bot Management, Zone WAF, Cache Rules,"
  say "    Dynamic Redirect, DNS, Email Routing Rules; and Zone: Read"
  say "  Account, Edit on each of:"
  say "    Cloudflare Tunnel, Email Routing Addresses; and Notifications: Read"
  say "  TTL: an end date a day or two away."
  say "A call the token may not make is named by the script that made it:"
  say "add that row and run the script again."
}

if [[ $MODE == dry-run ]]; then
  needs
  say
  say "$ME: a dry run. --apply asks for the token and makes this one call:"
  would "GET /user/tokens/verify"
  exit 0
fi

[[ -n $TOKEN_FILE ]] || die "give --token-file FILE: where the token is kept, such as ~/.config/omarchy-mac-lab/cloudflare.token"
if [[ $MODE == apply ]]; then
  [[ -t 0 ]] || die "--apply asks for the token at a terminal"
  needs
  printf 'Cloudflare API token (not shown): ' >&2
  IFS= read -rs TOKEN || true
  printf '\n' >&2
  TOKEN="${TOKEN//[[:space:]]/}"
  [[ -n $TOKEN ]] || die "the token is empty"
  (umask 077; mkdir -p "$(dirname "$TOKEN_FILE")" && rm -f "$TOKEN_FILE" && printf '%s\n' "$TOKEN" > "$TOKEN_FILE") || die "$TOKEN_FILE cannot be written"
  say "$ME: the token is in $TOKEN_FILE, which only you can read"
fi
load_token
out="$(cf GET /user/tokens/verify)" || exit 1
state="$(printf '%s' "$out" | jq -r '.result.status // "unknown"')"
ends="$(printf '%s' "$out" | jq -r '.result.expires_on // empty')"
[[ $state == active ]] || die "Cloudflare says the token is $state"
if [[ -z $ends ]]; then
  say "$ME: the token is good and has no end date: a routine token is short-lived. Make one with an end date"
  exit 1
fi
say "$ME: the token is good until $ends. Delete $TOKEN_FILE when the run is done"

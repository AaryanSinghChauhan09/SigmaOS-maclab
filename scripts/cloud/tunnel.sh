#!/usr/bin/env bash
# The lab's one tunnel and the Lab address's record.
#
#   tunnel.sh                                 print the API calls; send nothing
#   tunnel.sh --check                         the tunnel is there and connected, and
#                                             the Lab address points at it; else exit 1
#   tunnel.sh --apply --credential-to WHERE   make the tunnel and the record when missing
#
#   --token-file FILE      the Cloudflare API token, in a file only you can
#                          read; without it the script asks, and does not show it
#   --credential-to WHERE  where a new tunnel's credential goes, once, in the
#                          form cloudflared reads with --token-file:
#                          user@host:path sends it over ssh, so it is never a
#                          file on this machine; a plain path writes it here.
#                          Needed only when the tunnel does not exist yet.
#
# The tunnel's routes are deploy/cloudflared.yml, so it is made as one whose
# routes Cloudflare's dashboard does not hold. Whoever has the credential can
# receive the lab's traffic: on the mini, scripts/mini/setup puts it where
# only the lab account reads it. It is never printed.

# shellcheck source=scripts/cloud/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
parse_mode "$@"
set -- ${ARGS[@]+"${ARGS[@]}"}

TUNNEL_NAME="omarchy-mac-lab"
DEST=""
while (( $# )); do
  case "$1" in
    --credential-to)
      [[ $# -ge 2 ]] || usage 2 20
      DEST="$2"
      shift
      ;;
    *) usage 2 20 ;;
  esac
  shift
done

DRIFTED=()
drifted() {
  DRIFTED+=("$1")
  say "drifted  $1: $2"
}

record_body() {
  jq -nc --arg name "$LAB_HOST" --arg content "$1.cfargotunnel.com" \
    '{type: "CNAME", name: $name, content: $content, proxied: true, ttl: 1, comment: "the lab'"'"'s tunnel"}'
}

if [[ $MODE == dry-run ]]; then
  say "$ME: a dry run. These are the calls --apply makes; nothing is sent."
  say "A write is sent only for what is missing or has drifted."
  say
  say "the zone"
  would "GET /zones?name=$LAB_DOMAIN"
  say "the tunnel $TUNNEL_NAME"
  would "GET /accounts/{account}/cfd_tunnel?name=$TUNNEL_NAME&is_deleted=false"
  would "POST /accounts/{account}/cfd_tunnel {\"name\":\"$TUNNEL_NAME\",\"config_src\":\"local\",\"tunnel_secret\":\"<32 random bytes made here, never shown>\"}"
  say "the record of $LAB_HOST"
  would "GET /zones/{zone}/dns_records?name=$LAB_HOST"
  would "POST /zones/{zone}/dns_records $(record_body "<tunnel id>")"
  would "PUT /zones/{zone}/dns_records/{record} $(record_body "<tunnel id>")"
  exit 0
fi

# Where the credential goes: over ssh, or a file here.
DEST_HOST=""
DEST_PATH="$DEST"
case "$DEST" in
  *@*:*)
    DEST_HOST="${DEST%%:*}"
    DEST_PATH="${DEST#*:}"
    [[ $DEST_PATH =~ ^[A-Za-z0-9._/~-]+$ ]] || die "the path after $DEST_HOST: may hold only letters, digits and . _ / ~ -"
    ;;
esac

# Before a tunnel is made its credential must have somewhere to go, since
# it is given once: an empty file only its owner reads, which was not there.
ready_for_credential() {
  [[ -n $DEST ]] || die "there is no tunnel $TUNNEL_NAME yet, and making one needs --credential-to WHERE: user@host:path on the mini, or a path here"
  if [[ -n $DEST_HOST ]]; then
    ssh -o BatchMode=yes "$DEST_HOST" "umask 077; mkdir -p \"\$(dirname $DEST_PATH)\" && test ! -e $DEST_PATH && : > $DEST_PATH"
  else
    (umask 077; mkdir -p "$(dirname "$DEST_PATH")" && [[ ! -e $DEST_PATH ]] && : > "$DEST_PATH")
  fi || die "$DEST cannot be written, or is already there: a credential is never written over. Nothing was made"
}

# deliver: the credential on stdin, to where it goes.
deliver() {
  if [[ -n $DEST_HOST ]]; then
    ssh -o BatchMode=yes "$DEST_HOST" "umask 077; cat > $DEST_PATH"
  else
    (umask 077; cat > "$DEST_PATH")
  fi
}

load_token
find_zone "$LAB_DOMAIN"
[[ -n $ZONE_ID ]] || die "the account has no zone $LAB_DOMAIN: cloudflare-zone.sh makes it"

out="$(cf GET "/accounts/{account}/cfd_tunnel?name=$TUNNEL_NAME&is_deleted=false")" || exit 1
count="$(printf '%s' "$out" | jq '.result | length')"
[[ $count -le 1 ]] || die "$count tunnels are named $TUNNEL_NAME: the lab has one. Remove the others in the dashboard"
id="$(printf '%s' "$out" | jq -r '.result[0].id // empty')"
managed="$(printf '%s' "$out" | jq -r '.result[0].config_src // empty')"
status="$(printf '%s' "$out" | jq -r '.result[0].status // empty')"

if [[ -z $id && $MODE == apply ]]; then
  ready_for_credential
  secret="$(openssl rand -base64 32)"
  out="$(cf POST "/accounts/{account}/cfd_tunnel" "$(printf '%s' "$secret" | jq -Rsc --arg n "$TUNNEL_NAME" '{name: $n, config_src: "local", tunnel_secret: .}')")" || exit 1
  id="$(printf '%s' "$out" | jq -r '.result.id // empty')"
  out=""
  [[ -n $id ]] || die "Cloudflare made a tunnel and gave no id: look in the dashboard before running this again"
  if ! printf '%s' "$secret" | jq -Rsc --arg a "$ACCOUNT_ID" --arg t "$id" '{a: $a, t: $t, s: .}' | base64 | tr -d '\n' | deliver; then
    secret=""
    die "the tunnel $id was made and its credential did not reach $DEST: delete the tunnel in the dashboard (Zero Trust > Networks > Tunnels) and run this again"
  fi
  secret=""
  managed=local
  status=inactive
  say "set      the tunnel $TUNNEL_NAME: made as $id; its credential is at $DEST"
fi

if [[ -z $id ]]; then
  drifted "the tunnel $TUNNEL_NAME" "there is none"
elif [[ $managed != local ]]; then
  drifted "the tunnel $TUNNEL_NAME" "its routes are held by Cloudflare's dashboard, not by deploy/cloudflared.yml. Delete it in the dashboard and run this again with --apply"
else
  if [[ $status == healthy ]]; then
    say "ok       the tunnel $TUNNEL_NAME: $id, connected"
  elif [[ $MODE == check ]]; then
    drifted "the tunnel $TUNNEL_NAME" "$id is not connected (${status:-no state}): cloudflared is not running on the mini, or cannot reach Cloudflare"
  else
    say "ok       the tunnel $TUNNEL_NAME: $id, not connected yet: the mini's set-up starts cloudflared"
  fi

  out="$(cf GET "/zones/{zone}/dns_records?name=$LAB_HOST")" || exit 1
  count="$(printf '%s' "$out" | jq '.result | length')"
  record="$(printf '%s' "$out" | jq -r '.result[0].id // empty')"
  good="$(printf '%s' "$out" | jq -r --arg c "$id.cfargotunnel.com" '.result | length == 1 and .[0].type == "CNAME" and .[0].content == $c and .[0].proxied == true')"
  if [[ $good == true ]]; then
    say "ok       the record of $LAB_HOST: points at the tunnel, through Cloudflare"
  elif [[ $count -gt 1 ]]; then
    drifted "the record of $LAB_HOST" "$count records carry the name, want one: remove the others in the dashboard"
  elif [[ $MODE == apply ]]; then
    if [[ -n $record ]]; then
      cf PUT "/zones/{zone}/dns_records/$record" "$(record_body "$id")" >/dev/null || exit 1
    else
      cf POST "/zones/{zone}/dns_records" "$(record_body "$id")" >/dev/null || exit 1
    fi
    say "set      the record of $LAB_HOST: points at the tunnel, through Cloudflare"
  elif [[ $count == 0 ]]; then
    drifted "the record of $LAB_HOST" "there is none"
  else
    drifted "the record of $LAB_HOST" "it does not point at the tunnel through Cloudflare"
  fi
fi

if (( ${#DRIFTED[@]} )); then
  list="$(printf '%s; ' "${DRIFTED[@]}")"
  say "$ME: ${#DRIFTED[@]} not as it should be: ${list%; }"
  exit 1
fi
say "$ME: the tunnel and the record of $LAB_HOST are as they should be"

---
status: accepted
---

# The lab answers at one address, through one tunnel

ADR 0002 made labd an ordinary HTTPS service that serves TLS itself or sits behind a trusted proxy, with Tailscale still possible beside it. Omarchy Mac Lab runs on a Mac mini at home, where no port may be opened and the home address must not be published. We decided that everything crosses one name, the Lab address `https://lab.omarchy-mac.org`: the public results, the site, `lab`, the Macs' agents, uploads and downloads. The name reaches the mini through one Cloudflare tunnel on the Free plan, which `cloudflared` on the mini dials out. labd serves no TLS and listens only on its own machine: a loopback port for callers on the mini, and a Unix socket that only the tunnel dials. Through the socket it answers to the Lab address and forwards the one Alias, `maclab.omarchy.org`, to it. Who a caller is stays independent of the network, as ADR 0002 had it: a person's GitHub sign-in or Lab token, a Mac's device secret, the Builder's token, a one-time enrollment token.

## Considered options

- **labd's own TLS on an open port, or a reverse proxy in the house.** Rejected: both need an inbound port and publish the home address.
- **A second path for Macs or for big files** (a tailnet, a direct upload host, object storage). Rejected: two paths are two sets of limits, credentials and failures to keep right. Downloads move to object storage only if a measurement from outside the house says the link cannot carry them.
- **Tailscale for Maintainers and Lab-owned Macs.** Rejected: it would make the network part of who a caller is again, and Volunteers must need nothing but HTTPS.
- **A paid plan, or the edge's own access gate.** Rejected: the lab's sign-in is the only gate, and the Free plan's limits are ones the lab can work inside.

## Consequences

- The edge cuts a response with no body byte for 125 s and refuses a request body over 100 MB. So a held response sends a Keepalive at once and every 30 s, `lab exec` answers as a stream and is never sent again after a cut, job waits are calls of at most 50 s, a followed build log is polled from an offset, and uploads go in numbered parts of 32 MiB.
- There is no second way in. When Cloudflare, the tunnel or the home link is gone the lab is Dark for everyone, Maintainers included; Macs keep retrying, a Lab boot still returns to its Known-good kernel by itself, and no Hard reset can be asked for until the lab returns.
- The Out-of-band controller is on the mini, beside labd, so labd reaches it without crossing the tunnel.
- The caller's address is the edge's `CF-Connecting-IP`, believed only on the tunnel's socket.
- Whoever holds the Cloudflare account can point the name elsewhere: an accepted risk, held down by a security key on the account, DNSSEC and an alert when a tunnel is made or deleted.
- `--tls-cert`, `--tls-key`, `--trusted-proxy` and `--tailscale` are gone from labd, with the container image and the Kubernetes manifests.

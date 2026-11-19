# The Dark recordings

Run 2026-11-19 16:36 (Brisbane) by `scripts/cutover/dark-recordings --lab`, through https://lab.omarchy-mac.org.

Recorded, not gates: what a signed-out visitor got, as seen from the machine that ran this.

- cookies: a signed-out load of 7 public pages (/ /reports /matrix /terms /privacy /install /status) set no cookie, the edge's own included
- the cached page: / was an edge HIT at request 1 (age 1 s)
- cloudflared stopped, labd running, for 150 s (16:36:18 to 16:38:48 Brisbane), asked every 10 s:
-   the cached page /: 4 of 15 answers: status 200, cf-cache-status HIT, age 22 to 53 s, first 5 s in; 11 of 15 answers: status 200, cf-cache-status UPDATING, age 63 to 166 s, first 46 s in
-   /status, never cached: 15 of 15 answers: status 530 (Cloudflare error 1033, ray id a5cd33467e9879ff-SYD), first 5 s in
-   so: a signed-out visitor kept getting the public page for the whole 150 s
-   after: /status answered 200 again 10 s after cloudflared was started (the mini saw it back after 2 s); the cached page since: 1 of 6 answers: status 200, cf-cache-status UPDATING, age 177 s, first 10 s in; 5 of 6 answers: status 200, cf-cache-status HIT, age 10 to 51 s, first 20 s in
- the cached page: / was an edge HIT at request 2 (age 1 s)
- labd alone stopped, cloudflared running, for 150 s (16:40:13 to 16:42:43 Brisbane), asked every 10 s:
-   the cached page /: 4 of 14 answers: status 200, cf-cache-status HIT, age 21 to 52 s, first 9 s in; 10 of 14 answers: status 200, cf-cache-status UPDATING, age 63 to 157 s, first 50 s in
-   /status, never cached: 14 of 14 answers: status 502 (Cloudflare error 502, ray id a5cd391d799899a7-SYD), first 9 s in
-   so: a signed-out visitor kept getting the public page for the whole 150 s
-   after: /status answered 200 again 5 s after labd was started (the mini saw it back after 2 s); the cached page since: 1 of 6 answers: status 200, cf-cache-status UPDATING, age 167 s, first 5 s in; 5 of 6 answers: status 200, cf-cache-status HIT, age 10 to 51 s, first 15 s in

Result: recorded

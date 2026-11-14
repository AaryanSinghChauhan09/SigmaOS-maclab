# The first Deploy on the mini (Cutover step 8a)

Owed by the spec's section 17: "A binary cross-compiled on Linux under launchd on the mini" and "Full Disk Access for LaunchDaemons reading the data directory". Two runs belong here. The first is done; the second needs root on the mini and is the owner's.

## Without root, 2026-11-14 (Brisbane)

What it shows: the server bundle's `labd`, cross-compiled on macOS 27.2 (the M4 Pro) with Go 1.27.2, runs on the mini (macOS 27.2.1, arm64) and answers on a loopback port and on its Unix socket, and nowhere else. The Deploy script's own check accepts the bundle there and refuses a changed one.

What it does not show: launchd, the `lab` account, `/usr/local/lab` and `/var/lab`, or a binary cross-compiled on Linux (no Lab release has been built on GitHub yet).

The bundle is `release/local.sh v0.2.0-rc.1` at commit `1be6b16`, signed with a throwaway key. Everything is under `~/lab-overnight/` as the admin account; `~/lab-overnight/sys` stands in for `/`, so the layout is the one Deploy makes. labd was started under `nohup` with the arguments of `deploy/org.omarchy-mac.lab.labd.plist`, the paths under that folder and the port 47770 in place of 7770. In the output the admin account's name is written `admin` and its home `~`.

```
== 2026-11-14 22:03:53 AEST, macOS 27.2.1 arm64, as admin
cb8d6cd4491022dcacfc7e25269094a7fa92a82811cc7bffb12402be19427085  release/lab-server-darwin-arm64.tar.gz
== the Deploy script, dry run
deploy: lab-server-darwin-arm64.tar.gz matches its signature by the pinned release key
deploy: would unpack v0.2.0-rc.1 to ~/lab-overnight/sys/usr/local/lab/builds/v0.2.0-rc.1
deploy: would switch ~/lab-overnight/sys/usr/local/lab/current from nothing to v0.2.0-rc.1
deploy: would restart org.omarchy-mac.lab.labd under ~/lab-overnight/sys/Library/LaunchDaemons/org.omarchy-mac.lab.labd.plist
deploy: would verify labd at http://127.0.0.1:7770/healthz and on ~/lab-overnight/sys/var/lab/run/labd.sock, and put nothing back if it does not answer
exit 0
== a changed bundle
deploy: lab-server-darwin-arm64.tar.gz does not match its signature by the release key pinned on this machine (~/lab-overnight/sys/usr/local/lab/allowed_signers): nothing was changed
exit 1
== unpack
v0.2.0-rc.1
~/lab-overnight/sys/usr/local/lab/current/bin/labd: Mach-O 64-bit executable arm64
v0.2.0-rc.1
== start
pid 11501
== verify, as Deploy does (22:04:05)
loopback: ok
socket:   ok
socket, as localhost: 421
socket, the Alias page: 308 https://lab.omarchy-mac.org/install
HTTP/1.1 200 OK
Cache-Control: no-cache
X-Robots-Tag: noindex
== where it listens
COMMAND     PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
labd      11501 admin    28u  IPv4 0x568daa8fe0b69c9d      0t0  TCP 127.0.0.1:47770 (LISTEN)
srw------- 1 admin staff 0 Nov 14 22
== the process
admin 11501 00:11 ~/lab-overnight/sys/usr/local/lab/current/bin/labd --listen 127.0.0.1:47770 --socket ~/lab-overnight/sys/var/lab/run/labd.sock --public-url https://lab.omarchy-mac.org --alias maclab.omarchy.org --preview --data ~/lab-overnight/sys/var/lab/data
== its log
labd: 2026/11/14 22:03:54 first Maintainer: admin; log in with `lab login <labd-url> "$(cat ~/lab-overnight/sys/var/lab/data/admin.token)"`
2026/11/14 22:03:54 labd listening on http://127.0.0.1:47770 and the tunnel's socket ~/lab-overnight/sys/var/lab/run/labd.sock, served as https://lab.omarchy-mac.org, in Preview, data in ~/lab-overnight/sys/var/lab/data
```

The two checks are the ones `scripts/mini/deploy` makes after a switch:

```
curl -fsS http://127.0.0.1:47770/healthz
curl -fsS --unix-socket ~/lab-overnight/sys/var/lab/run/labd.sock -H 'X-Forwarded-Proto: https' http://lab.omarchy-mac.org/healthz
```

The data directory held `labd.db`, `reports.db` and the caller log's folder after the start. No `sudo`, no LaunchDaemon or LaunchAgent, no Homebrew formula, nothing outside `~/lab-overnight/`.

## Under launchd, as `lab`

Not run yet. The owner runs `scripts/mini/setup` and then `scripts/mini/deploy` with `sudo` on the mini. Wanted here, dated:

- `curl --unix-socket /var/lab/run/labd.sock` and `curl 127.0.0.1:7770` both get `/healthz`.
- `launchctl print system/org.omarchy-mac.lab.labd` shows it running, and `ps` shows it as `lab`.
- `lsof -nP -iTCP -sTCP:LISTEN` shows labd on `127.0.0.1:7770` only.
- labd made its databases in `/var/lab/data`; its log has no "operation not permitted"; no Full Disk Access prompt appeared.

If labd does not start under launchd, the mini's binaries move to the macOS runner in the release workflow and this run is made again. The same run is owed once more for the first bundle a Linux runner builds.

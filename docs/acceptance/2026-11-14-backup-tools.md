# Backups: the tools, and what is still to be written here

Written 2026-11-14 (Brisbane), before anything ran on the mini, the guest or R2. The lines marked "owed" are filled in after the owner's run (build plan, slice 45).

## What Homebrew ships (read 2026-11-14, nothing installed)

| Tool | Version | Where the formula is | Read from |
|---|---|---|---|
| Litestream | 0.5.18 | the tap `benbjohnson/litestream`, not Homebrew core (core has no `litestream` formula) | `litestream.rb` of `benbjohnson/homebrew-litestream`; `formulae.brew.sh/api/formula/litestream.json` answers 404 |
| restic | 0.19.1 | Homebrew core, with a bottle for Apple silicon | `formulae.brew.sh/api/formula/restic.json` |

- `deploy/homebrew-formulae` therefore lists `benbjohnson/litestream/litestream` and `restic`. The tap's formula downloads Litestream's own release file for the Mac and checks its sha256; it builds nothing. The root-owned copy is named `litestream`.
- `deploy/litestream.yml` is written for 0.5: its keys (`snapshot.interval`, `snapshot.retention`, and under each database's `replica`: `type`, `host`, `user`, `path`, `key-path`, `host-key`, `sync-interval`) were read in `cmd/litestream/main.go` and `sftp/replica_client.go` at the tag `v0.5.18`. Without `host-key` that version accepts any SSH host key, so the set-up always writes it.
- restic's environment (`RESTIC_REPOSITORY`, `RESTIC_REPOSITORY_FILE`, `RESTIC_PASSWORD_FILE`, `RESTIC_CACERT`, `RESTIC_REST_USERNAME`, `RESTIC_REST_PASSWORD`, `RESTIC_CACHE_DIR`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_DEFAULT_REGION`) was read in `doc/075_scripting.rst` at `v0.19.1`.

## On the guest, at their pins (not Homebrew: the guest is Debian)

| Tool | Version | sha256 of the download |
|---|---|---|
| restic | 0.19.1, `restic_0.19.1_linux_amd64.bz2` | `f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c` |
| rest-server | 0.14.0, `rest-server_0.14.0_linux_amd64.tar.gz` | `4c9c95bc079a0334e81fad379b19dc5c3353c71c2c88d652cafce2081c2b1c66` |

Both checksums are from each release's own `SHA256SUMS` on GitHub. Debian has no rest-server package, and its restic is older than the mini's (0.18.0 in trixie, 0.14.0 in bookworm).

## Owed after the owner's run

- The Litestream and restic versions Homebrew gave on the mini (`cat /usr/local/lab/tools/current/VERSIONS`).
- One nightly run by hand, and `restic snapshots` on both targets.
- Litestream's replica shown on the guest; the uplink pulled and put back, with how Litestream caught up.
- Where the PBS on `pve2` keeps its datastore; how long it keeps the guest (no longer than 6 months).
- Whether R2 asked for a payment method.
- The tests against the real tools: `go test ./internal/minitest -run RealTools` on a machine that has `restic`, `rest-server` and `litestream` (they are skipped where a tool is missing, and have not run yet).

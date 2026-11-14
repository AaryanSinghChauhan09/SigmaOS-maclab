# The old lab's data and the rehearsal import (Cutover steps 10 and 11)

Owed by the Ready list's item 4 and by the spec's section 17 ("that the mini reaches Railway's Postgres at the address the owner types"). Two records belong here: what the scripts did on made-up directories while they were built, which is below, and the dated output of the owner's runs on the mini, which is not there yet.

What the owner's runs must show: the old lab's counts before and after the upgrade, equal (2 records, 253 jobs and 90 builds when the spec was written); `m1pro` and `m1pro2` Retired, the Lease ended, the out-of-band entries cleared; the old directory on `omarchy-gpu` as it was after the copy; the dump written on the mini by the import command from the address typed at its prompt; the three checks passed. Counts only: no report, no handle, no key.

## On made-up directories, 2026-11-15 (Brisbane)

What it shows: `scripts/cutover/old-lab` and `scripts/cutover/rehearsal-import` with the real `labd`, against a folder that stands in for the mini's `/` and another for the old host, reached through a stand-in for `ssh`. The old lab's directory is made by the tests in the old labd's format (`internal/oldlabtest`: the tables of maclab's `store.go` at the fork, and the older set the labd on `omarchy-gpu` has), with the counts above. The old site is a dump of 20 made-up reports written by the import's own writer.

What it does not show: `omarchy-gpu`, its `systemctl --user`, its `tar` and `sha256sum`; `sudo`, the `lab` account and launchd on the mini; Railway's Postgres. No real directory was read.

`go test ./internal/carrytest -v`, the paths shortened (`/r` stands in for `/`, and the test's own account for `lab`):

```
$ old-lab copy --stage /home/old-lab
old-lab: the old lab's labd on omarchy-gpu still runs or is still enabled (active enabled): stop it first (old-lab stop)
exit 1
$ old-lab stop
old-lab: the old lab's labd on omarchy-gpu is: active enabled
old-lab: stop and disable the old lab's labd on omarchy-gpu (systemctl --user disable --now labd)
old-lab: stopped and disabled: inactive disabled. The host stays on, for Unlock; its data directory stays as it is
$ old-lab copy --stage /home/old-lab
old-lab: listing omarchy-gpu:.local/share/maclab
old-lab: copying 26 files
old-lab: copied: 26 files, 0.0 GB, each with the SHA-256 omarchy-gpu gave
old-lab: the old directory on omarchy-gpu is as it was: listed again after the copy, the same
old-lab: the copy holds: records 2 jobs 253 builds 90
$ old-lab upgrade --stage /home/old-lab --account-id 7 owner
old-lab: the copy holds: records 2 jobs 253 builds 90
old-lab: copying /home/old-lab/data to /r/var/lab/data.old-lab as lab
The old lab's data in /r/var/lab/data.old-lab is upgraded.
records 2 (2 before)
jobs 253 (253 before)
builds 90 (90 before)
Retired: m1pro, m1pro2
Lease ended: m1pro
out-of-band entry cleared: m1pro, m1pro2
now the work of @owner (GitHub account 7): 228 jobs, 90 builds
ended, left unfinished by the old labd: 2 jobs, 2 builds
no longer honoured: enrollment tokens 1, Maintainers' tokens 0, builders' tokens 0, every device secret; token files removed: none
old-lab: the old lab's data is the lab's: records 2 jobs 253 builds 90, as the copy held, and labd answers
$ old-lab upgrade --stage /home/old-lab --account-id 7 owner
old-lab: /r/var/lab/data.before-old-lab is there: the old lab's data went in before. It goes in once
exit 1
$ rehearsal-import run --public-key /home/m1-pro.pub --dump /r/var/lab/import/old-site.dump
rehearsal-import: /r/var/lab/import/old-site.dump is there: the old site is not read
rehearsal-import: emptying reports.db: the copy it held moves to /r/var/lab/import/reports-before
[the import's own lines, with its list]
rehearsal-import: the import and its three checks passed. For docs/acceptance/, counts only:
---- rehearsal import, 2026-11-15 08:01 Brisbane, labd dev ----
Imported the dump (taken 2026-11-15T01:02:03Z): 20 reports (2 hidden, 8 Verified runs), 4 Mac sign-ins, 19 digests; 6 people, 6 of them new to the lab.
Listed, 4 (all were carried):
reports: 20 at the old site, 20 in the dump, 20 in reports.db
hidden reports: 2 in the dump, 2 in reports.db
tester_bindings: 4 at the old site, 4 in the dump, 4 Mac sign-ins in reports.db
testers: 6 at the old site, 6 in the dump; the 4 accounts they and the sign-ins name: 4 are people. 3 handles never signed in there: each is a person if GitHub knows it
The row counts match.
That Mac's machine id, recomputed from its public key, is on 4 reports and 1 Mac sign-ins.
The old site's link deleted its report on the lab, and the report's digest is kept.
reports listed with a reason and carried all the same: 3
----
```

The 228 jobs are those a person asked for: the old labd named nobody, so all are the owner's; the other 25 it queued itself. The token files are "none" because the copy never brings them.

## On the mini

Not run yet: the owner's steps. The outputs of `old-lab copy`, `old-lab upgrade` and the block `rehearsal-import run` ends with go here, under the day's date.

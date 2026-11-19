# The rehearsal import

Run 2026-11-19 19:28 (Brisbane) by `scripts/cutover/rehearsal-import run` on the mini, labd v0.2.0-rc.16, reading the old site's Postgres through a Railway TCP proxy opened for the run and removed after it. Counts only: no report, no key, no address.

- Imported the dump (taken 2026-11-19T09:28:26Z): 146 reports (0 hidden, 22 Verified runs), 36 Mac sign-ins, 146 digests; 54 people, 52 of them new to the lab.
- Every report passes the lab's rules.
- reports: 146 at the old site, 146 in the dump, 146 in reports.db
- hidden reports: 0 in the dump, 0 in reports.db
- tester_bindings: 36 at the old site, 36 in the dump, 36 Mac sign-ins in reports.db
- testers: 32 at the old site, 32 in the dump; the 28 accounts they and the sign-ins name: 28 are people. 27 handles never signed in there: each is a person if GitHub knows it
- The row counts match.
- That Mac's machine id, recomputed from its public key, is on 3 reports and 0 Mac sign-ins.
- The old site's link deleted its report on the lab, and the report's digest is kept.
- reports listed with a reason and carried all the same: 0

Result: pass

# Acceptance records

The dated output of each gate test of the Ready list (spec, section 16), one file per run, and what was measured while building: the release files' sizes, what the organisation's Actions settings allow.

`scripts/cutover/ready` reads this folder and says which of the nine items are green. A test that keeps a results file (`out-of-band.results`, `hardening.results`, `builder-gate.results`) is green by it; any other item by its newest dated record, `DATE-NAME.md`, whose last line starting `Result:` says `pass`. A record filed by hand gets that line from the owner. The names it looks for are in the script's header. `DATE-confirmed.md` is the Confirmed checklist's record; Confirmed itself is the owner's dated line at its end.

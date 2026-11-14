-- A Release, listed from the moment its manifest is frozen: who froze it
-- and when, its kernel and packages, and its Macs as they were then. Each
-- person is kept as the account id and the handle they had then.
CREATE TABLE releases (
  name TEXT PRIMARY KEY,
  frozen_at INTEGER NOT NULL,
  frozen_by_id INTEGER NOT NULL DEFAULT 0,
  frozen_by TEXT NOT NULL DEFAULT '',
  manifest_sha256 TEXT NOT NULL,
  kernel TEXT NOT NULL,
  packages TEXT NOT NULL,
  macs TEXT NOT NULL
) STRICT;
-- The verdict history: every verdict stays, with who, when, the note and
-- the warnings the person confirmed. The newest line is the Release's state.
CREATE TABLE release_verdicts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  release TEXT NOT NULL,
  at INTEGER NOT NULL,
  state TEXT NOT NULL,
  note TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  handle TEXT NOT NULL DEFAULT '',
  warnings TEXT NOT NULL DEFAULT '[]'
) STRICT;
CREATE INDEX release_verdicts_release ON release_verdicts(release, id);
-- Who froze a Mac's installer entry for a Release, and when.
CREATE TABLE release_entries (
  release TEXT NOT NULL,
  mac TEXT NOT NULL,
  at INTEGER NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  handle TEXT NOT NULL DEFAULT '',
  entry_sha256 TEXT NOT NULL,
  PRIMARY KEY (release, mac)
) STRICT;
-- Each Promotion: the Mac (by name and by enrollment), the Installed run
-- and kernel it was promoted to, who promoted and when.
CREATE TABLE release_promotions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  release TEXT NOT NULL,
  at INTEGER NOT NULL,
  mac TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  job TEXT NOT NULL,
  kernel TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  handle TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX release_promotions_release ON release_promotions(release, id);
CREATE INDEX release_promotions_device ON release_promotions(device_id, id);

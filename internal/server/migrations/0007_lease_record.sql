-- The Lease record: every Lease taken, renewed, extended, released, expired
-- and ended, on which Mac (by name and by enrollment), whose it was (the
-- account id, and the handle the person had then), and who ended it and why
-- when that was not its holder.
CREATE TABLE lease_record (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  mac TEXT NOT NULL,
  device_id TEXT NOT NULL,
  event TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  handle TEXT NOT NULL DEFAULT '',
  holder TEXT NOT NULL DEFAULT '',
  until INTEGER NOT NULL,
  by_id INTEGER NOT NULL DEFAULT 0,
  by TEXT NOT NULL DEFAULT '',
  why TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX lease_record_device ON lease_record(device_id, id);
CREATE INDEX lease_record_account ON lease_record(account_id, id);

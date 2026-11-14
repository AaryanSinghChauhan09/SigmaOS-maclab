-- The Exec record: every lab exec and every refused attempt, on which Mac
-- (by name and by enrollment), whose it was (the account id, and the handle
-- the person had then), the command's text, its exit code and how long it
-- ran. No output. A line is deleted when it is 12 months old.
CREATE TABLE exec_record (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  mac TEXT NOT NULL,
  device_id TEXT NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  handle TEXT NOT NULL DEFAULT '',
  command TEXT NOT NULL,
  state TEXT NOT NULL,
  exit_code INTEGER,
  seconds REAL NOT NULL DEFAULT 0,
  why TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX exec_record_device ON exec_record(device_id, id);
CREATE INDEX exec_record_at ON exec_record(at);
